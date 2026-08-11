package catalog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type nextUpTestStoreProvider struct{}

func (nextUpTestStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return nil, nil
}

func (nextUpTestStoreProvider) Close() error { return nil }

func newNextUpTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_items')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check media_items table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied base schema")
	}

	return pool
}

func TestBuildListNextUpQuery_PrefersRecentCompletedOverOlderPartialProgress(t *testing.T) {
	t.Parallel()

	query, args := buildListNextUpQuery(NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
	}, 20, nil)

	expectedFragments := []string{
		"eligible_series AS (",
		"uwp_ip.position_seconds > 0",
		"e_ip.series_id = ce.series_id",
		"uwp_ip.updated_at > ce.updated_at",
		"FROM user_history_hidden_items hhi",
		"uwp.updated_at <= hhi.hidden_before",
		"AND (uwp2.completed = TRUE OR uwp2.position_seconds > 0)",
		"ORDER BY uwp.updated_at DESC, uwp.media_item_id DESC",
		"LIMIT $3",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected query to contain %q, got:\n%s", fragment, query)
		}
	}

	if strings.Contains(query, "uwp.media_item_id = ANY") {
		t.Fatalf("query must not cap completed progress before deriving series anchors, got:\n%s", query)
	}

	if len(args) != 3 {
		t.Fatalf("expected default arg count, got %d", len(args))
	}
}

func TestBuildListNextUpQuery_GlobalBoundsAnchorScan(t *testing.T) {
	t.Parallel()

	q := NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
	}
	query, args := buildListNextUpQuery(q, 20, nil)

	expectedFragments := []string{
		"WITH RECURSIVE",
		"(uwp.updated_at, uwp.media_item_id) < (w.updated_at, w.media_item_id)",
		"NOT (e.series_id = ANY(w.seen))",
		fmt.Sprintf("w.n < %d", nextUpAnchorMaxSeries),
		"frontier AS (",
		"LEFT JOIN (",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected global anchor walk to contain %q, got:\n%s", fragment, query)
		}
	}
	// Seed step, recursive step, and the anchor lateral attached to each.
	if got := strings.Count(query, "FROM user_history_hidden_items hhi"); got != 4 {
		t.Fatalf("expected hidden-items filter in every walk step and anchor, got %d occurrences:\n%s", got, query)
	}
	seed := strings.SplitN(query, "UNION ALL", 2)[0]
	if strings.Contains(seed, "(uwp.updated_at, uwp.media_item_id) < ($") {
		t.Fatalf("first batch seed must not contain a continuation cursor, got:\n%s", seed)
	}
	if strings.Contains(seed, "e.series_id = ANY($") {
		t.Fatalf("first batch seed must not contain a seen-series exclusion, got:\n%s", seed)
	}
	if len(args) != 3 {
		t.Fatalf("expected first batch args only, got %v", args)
	}

	cursorAt := time.Now().UTC()
	cursor := &nextUpWalkCursor{
		updatedAt:   cursorAt,
		mediaItemID: "episode-96",
		seen:        []string{"series-1", "series-2"},
	}
	continuedQuery, continuedArgs := buildListNextUpQuery(q, 7, cursor)
	continuedSeed := strings.SplitN(continuedQuery, "UNION ALL", 2)[0]
	continuedFragments := []string{
		"(uwp.updated_at, uwp.media_item_id) < ($4, $5)",
		"NOT (e.series_id = ANY($6))",
		"$6::text[] || pick.series_id AS seen",
		"frontier AS (",
		"LEFT JOIN (",
	}
	for _, fragment := range continuedFragments {
		if !strings.Contains(continuedQuery, fragment) {
			t.Fatalf("expected continued query to contain %q, got:\n%s", fragment, continuedQuery)
		}
	}
	if !strings.Contains(continuedSeed, "(uwp.updated_at, uwp.media_item_id) < ($4, $5)") ||
		!strings.Contains(continuedSeed, "NOT (e.series_id = ANY($6))") {
		t.Fatalf("continued batch seed must contain cursor and seen-series predicates, got:\n%s", continuedSeed)
	}
	if len(continuedArgs) != 6 || continuedArgs[3] != cursorAt || continuedArgs[4] != cursor.mediaItemID {
		t.Fatalf("unexpected continuation args: %v", continuedArgs)
	}
	seenArg, ok := continuedArgs[5].([]string)
	if !ok || len(seenArg) != len(cursor.seen) || seenArg[0] != cursor.seen[0] || seenArg[1] != cursor.seen[1] {
		t.Fatalf("expected cursor seen array verbatim, got %#v", continuedArgs[5])
	}
}

func TestBuildListNextUpQuery_GlobalDateCutoffAppliesToEveryWalkStep(t *testing.T) {
	t.Parallel()

	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	query, args := buildListNextUpQuery(NextUpQuery{
		UserID:     7,
		ProfileID:  "profile-1",
		DateCutoff: &cutoff,
	}, 20, nil)

	if got := strings.Count(query, "AND uwp.updated_at >= $4"); got != 2 {
		t.Fatalf("expected date cutoff in seed and recursive step, got %d occurrences:\n%s", got, query)
	}
	if len(args) != 4 || args[3] != cutoff {
		t.Fatalf("expected one shared date-cutoff arg, got %v", args)
	}
}

func TestBuildListNextUpQuery_SeriesScopedKeepsUnboundedAnchor(t *testing.T) {
	t.Parallel()

	q := NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
		SeriesID:  "series-42",
	}
	query, args := buildListNextUpQuery(q, 20, nil)

	// The show-detail tile must anchor on the series' last completed episode
	// no matter how long ago it was watched: the recency bound would make a
	// long-idle series' tile disappear.
	if strings.Contains(query, "recent_completed AS (") {
		t.Fatalf("series-scoped query must not bound the anchor scan, got:\n%s", query)
	}
	if strings.Contains(query, "WITH RECURSIVE") {
		t.Fatalf("series-scoped query must keep its non-recursive SQL shape, got:\n%s", query)
	}
	if !strings.Contains(query, "AND e.series_id = $4") {
		t.Fatalf("series-scoped query must filter by series_id, got:\n%s", query)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args with SeriesID, got %d (%v)", len(args), args)
	}

	queryWithCursor, argsWithCursor := buildListNextUpQuery(q, 20, &nextUpWalkCursor{
		updatedAt:   time.Now().UTC(),
		mediaItemID: "ignored-episode",
		seen:        []string{"ignored-series"},
	})
	if queryWithCursor != query || fmt.Sprint(argsWithCursor) != fmt.Sprint(args) {
		t.Fatalf("series-scoped SQL and args must be byte-identical when a walk cursor is supplied")
	}
}

func TestBuildListNextUpQuery_GlobalAnchorOrdersByEpisodeNotMediaItemID(t *testing.T) {
	t.Parallel()

	// media_item_id is unique, so a season/episode tie-break appended behind it
	// can never be reached. Choosing the series and choosing which of its
	// episodes anchors the rail have to be separate orderings.
	query, _ := buildListNextUpQuery(NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
	}, 20, nil)

	if strings.Contains(query, `ORDER BY uwp.updated_at DESC, uwp.media_item_id DESC,
					e.season_number DESC, e.episode_number DESC`) {
		t.Fatalf("season/episode must not sit behind the unique media_item_id tie-break, got:\n%s", query)
	}
	if got := strings.Count(query, "ORDER BY e_a.season_number DESC, e_a.episode_number DESC"); got != 2 {
		t.Fatalf("expected episode-ordered anchor in seed and recursive step, got %d occurrences:\n%s", got, query)
	}
	if got := strings.Count(query, "AND uwp_a.updated_at = pick.updated_at"); got != 2 {
		t.Fatalf("expected anchor pinned to the series' newest completed timestamp, got %d:\n%s", got, query)
	}
}

func TestNextUpRepository_BulkMarkWatchedAnchorsOnHighestEpisode(t *testing.T) {
	// A bulk mark-watched series writes every row with one timestamp. The anchor
	// must then be the highest (season, episode), not the highest content_id:
	// with production-shape IDs where season 2 was scanned before a season-1
	// backfill, season 1 sorts higher and the rail would surface s01e04 — an
	// episode after one the user already watched — instead of s02e04.
	pool := newNextUpTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("nextup-order-%d", time.Now().UnixNano())
	seriesID := prefix + "-series"

	userID, profileID, folderID := seedNextUpTestOwner(t, ctx, pool, prefix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})
	seedNextUpSeries(t, ctx, pool, seriesID, prefix+" Ordering")

	// Season 2 scanned first (lower IDs), season 1 backfilled later (higher IDs).
	const wantContentID = "1" + "00000000000000004" // s02e04
	const trapContentID = "9" + "00000000000000004" // s01e04
	episodeIDs := []string{
		"100000000000000001", "100000000000000002", "100000000000000003", wantContentID,
		"900000000000000001", "900000000000000002", "900000000000000003", trapContentID,
	}
	seasons := []int{2, 2, 2, 2, 1, 1, 1, 1}
	numbers := []int{1, 2, 3, 4, 1, 2, 3, 4}
	for i, episodeID := range episodeIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, $3, $4, 'Ordering Episode')
		`, episodeID, seriesID, seasons[i], numbers[i]); err != nil {
			t.Fatalf("seed episode %s: %v", episodeID, err)
		}
	}
	seedNextUpFiles(t, ctx, pool, folderID, episodeIDs)

	// Every completed row shares one timestamp, as a bulk mark-watched write does.
	bulkAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (
			user_id, profile_id, media_item_id, position_seconds,
			duration_seconds, completed, updated_at
		)
		SELECT $1, $2, unnest($3::text[]), 0, 1800, TRUE, $4
	`, userID, profileID, []string{
		"100000000000000001", "100000000000000002", "100000000000000003",
		"900000000000000001", "900000000000000002", "900000000000000003",
	}, bulkAt); err != nil {
		t.Fatalf("seed bulk progress: %v", err)
	}

	repo := NewNextUpRepository(pool, nextUpTestStoreProvider{})
	results, err := repo.ListNextUp(ctx, NextUpQuery{
		UserID:          userID,
		ProfileID:       profileID,
		Limit:           20,
		EnableResumable: false,
	})
	if err != nil {
		t.Fatalf("ListNextUp: %v", err)
	}
	assertNextUpContentIDs(t, results, wantContentID)
}

func TestNextUpRepository_GlobalAnchorWalkSurvivesFloodedSeries(t *testing.T) {
	pool := newNextUpTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	prefix := fmt.Sprintf("nextup-flood-%d", suffix)
	seriesA := prefix + "-series-a"
	seriesB := prefix + "-series-b"
	seriesC := prefix + "-series-c"
	seriesIDs := []string{seriesA, seriesB, seriesC}
	episodeAPrefix := prefix + "-episode-a-"
	episodeB1 := prefix + "-episode-b-1"
	episodeB2 := prefix + "-episode-b-2"
	episodeC1 := prefix + "-episode-c-1"
	episodeC2 := prefix + "-episode-c-2"

	userID, profileID, folderID := seedNextUpTestOwner(t, ctx, pool, prefix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, seriesIDs)
	})

	seedNextUpSeries(t, ctx, pool, seriesA, prefix+" Series A")
	seedNextUpSeries(t, ctx, pool, seriesB, prefix+" Series B")
	seedNextUpSeries(t, ctx, pool, seriesC, prefix+" Series C")
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		SELECT $1 || gs::text, $2, 1, gs, 'Flood Episode ' || gs::text
		FROM generate_series(1, 600) gs
	`, episodeAPrefix, seriesA); err != nil {
		t.Fatalf("seed flooded episodes: %v", err)
	}
	seedNextUpEpisodes(t, ctx, pool,
		[]string{episodeB1, episodeB2, episodeC1, episodeC2},
		[]string{seriesB, seriesB, seriesC, seriesC},
		[]int{1, 2, 1, 2},
	)
	seedNextUpFiles(t, ctx, pool, folderID, []string{episodeB2, episodeC2})

	floodedAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (
			user_id, profile_id, media_item_id, position_seconds,
			duration_seconds, completed, updated_at
		)
		SELECT $1, $2, $3 || gs::text, 0, 1800, TRUE, $4
		FROM generate_series(1, 600) gs
	`, userID, profileID, episodeAPrefix, floodedAt); err != nil {
		t.Fatalf("seed flooded progress: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (
			user_id, profile_id, media_item_id, position_seconds,
			duration_seconds, completed, updated_at
		)
		VALUES ($1, $2, $3, 0, 1800, TRUE, $5),
		       ($1, $2, $4, 0, 1800, TRUE, $6)
	`, userID, profileID, episodeB1, episodeC1, floodedAt.Add(-time.Hour), floodedAt.Add(-2*time.Hour)); err != nil {
		t.Fatalf("seed older series progress: %v", err)
	}

	repo := NewNextUpRepository(pool, nextUpTestStoreProvider{})
	results, err := repo.ListNextUp(ctx, NextUpQuery{
		UserID:          userID,
		ProfileID:       profileID,
		Limit:           20,
		EnableResumable: false,
	})
	if err != nil {
		t.Fatalf("ListNextUp: %v", err)
	}
	assertNextUpContentIDs(t, results, episodeB2, episodeC2)
}

func TestNextUpRepository_GlobalAnchorWalkKeepsSameTimestampSeries(t *testing.T) {
	pool := newNextUpTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	prefix := fmt.Sprintf("nextup-cursor-%d", suffix)
	seriesD := prefix + "-series-d"
	seriesE := prefix + "-series-e"
	seriesIDs := []string{seriesD, seriesE}
	episodeD1 := prefix + "-episode-d-1"
	episodeD2 := prefix + "-episode-d-2"
	episodeE1 := prefix + "-episode-e-1"
	episodeE2 := prefix + "-episode-e-2"

	userID, profileID, folderID := seedNextUpTestOwner(t, ctx, pool, prefix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, seriesIDs)
	})

	seedNextUpSeries(t, ctx, pool, seriesD, prefix+" Series D")
	seedNextUpSeries(t, ctx, pool, seriesE, prefix+" Series E")
	seedNextUpEpisodes(t, ctx, pool,
		[]string{episodeD1, episodeD2, episodeE1, episodeE2},
		[]string{seriesD, seriesD, seriesE, seriesE},
		[]int{1, 2, 1, 2},
	)
	seedNextUpFiles(t, ctx, pool, folderID, []string{episodeD2, episodeE2})

	completedAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (
			user_id, profile_id, media_item_id, position_seconds,
			duration_seconds, completed, updated_at
		)
		VALUES ($1, $2, $3, 0, 1800, TRUE, $5),
		       ($1, $2, $4, 0, 1800, TRUE, $5)
	`, userID, profileID, episodeD1, episodeE1, completedAt); err != nil {
		t.Fatalf("seed same-timestamp progress: %v", err)
	}

	repo := NewNextUpRepository(pool, nextUpTestStoreProvider{})
	results, err := repo.ListNextUp(ctx, NextUpQuery{
		UserID:          userID,
		ProfileID:       profileID,
		Limit:           20,
		EnableResumable: false,
	})
	if err != nil {
		t.Fatalf("ListNextUp: %v", err)
	}
	assertNextUpContentIDs(t, results, episodeD2, episodeE2)
}

func TestNextUpRepository_GlobalAnchorWalkContinuesPastIneligibleSeries(t *testing.T) {
	pool := newNextUpTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("nextup-ineligible-%d", time.Now().UnixNano())

	const ineligibleCount = 100
	seriesIDs := make([]string, 0, ineligibleCount+2)
	episodeIDs := make([]string, 0, ineligibleCount+4)
	episodeSeriesIDs := make([]string, 0, ineligibleCount+4)
	episodeNumbers := make([]int, 0, ineligibleCount+4)
	completedEpisodeIDs := make([]string, 0, ineligibleCount+2)
	completedTimes := make([]time.Time, 0, ineligibleCount+2)

	userID, profileID, folderID := seedNextUpTestOwner(t, ctx, pool, prefix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, seriesIDs)
	})

	newest := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < ineligibleCount; i++ {
		seriesID := fmt.Sprintf("%s-series-%03d", prefix, i)
		episodeID := seriesID + "-episode-1"
		seriesIDs = append(seriesIDs, seriesID)
		episodeIDs = append(episodeIDs, episodeID)
		episodeSeriesIDs = append(episodeSeriesIDs, seriesID)
		episodeNumbers = append(episodeNumbers, 1)
		completedEpisodeIDs = append(completedEpisodeIDs, episodeID)
		completedTimes = append(completedTimes, newest.Add(-time.Duration(i)*time.Minute))
		seedNextUpSeries(t, ctx, pool, seriesID, fmt.Sprintf("%s Ineligible %03d", prefix, i))
	}

	olderSeriesA := prefix + "-older-series-a"
	olderSeriesB := prefix + "-older-series-b"
	olderA1 := olderSeriesA + "-episode-1"
	olderA2 := olderSeriesA + "-episode-2"
	olderB1 := olderSeriesB + "-episode-1"
	olderB2 := olderSeriesB + "-episode-2"
	seriesIDs = append(seriesIDs, olderSeriesA, olderSeriesB)
	seedNextUpSeries(t, ctx, pool, olderSeriesA, prefix+" Older A")
	seedNextUpSeries(t, ctx, pool, olderSeriesB, prefix+" Older B")
	episodeIDs = append(episodeIDs, olderA1, olderA2, olderB1, olderB2)
	episodeSeriesIDs = append(episodeSeriesIDs, olderSeriesA, olderSeriesA, olderSeriesB, olderSeriesB)
	episodeNumbers = append(episodeNumbers, 1, 2, 1, 2)
	completedEpisodeIDs = append(completedEpisodeIDs, olderA1, olderB1)
	completedTimes = append(completedTimes,
		newest.Add(-(ineligibleCount+1)*time.Minute),
		newest.Add(-(ineligibleCount+2)*time.Minute),
	)

	seedNextUpEpisodes(t, ctx, pool, episodeIDs, episodeSeriesIDs, episodeNumbers)
	seedNextUpFiles(t, ctx, pool, folderID, []string{olderA2, olderB2})
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (
			user_id, profile_id, media_item_id, position_seconds,
			duration_seconds, completed, updated_at
		)
		SELECT $1, $2, episode_id, 0, 1800, TRUE, completed_at
		FROM unnest($3::text[], $4::timestamptz[]) AS fixture(episode_id, completed_at)
	`, userID, profileID, completedEpisodeIDs, completedTimes); err != nil {
		t.Fatalf("seed ineligible-flood progress: %v", err)
	}

	repo := NewNextUpRepository(pool, nextUpTestStoreProvider{})
	results, err := repo.ListNextUp(ctx, NextUpQuery{
		UserID:          userID,
		ProfileID:       profileID,
		Limit:           20,
		EnableResumable: false,
	})
	if err != nil {
		t.Fatalf("ListNextUp: %v", err)
	}
	assertNextUpContentIDs(t, results, olderA2, olderB2)
}

func TestNextUpRepository_GlobalAnchorWalkStopsWhenHistoryExhausted(t *testing.T) {
	pool := newNextUpTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("nextup-exhausted-%d", time.Now().UnixNano())
	seriesIDs := []string{
		prefix + "-series-a",
		prefix + "-series-b",
		prefix + "-series-c",
	}
	episodeIDs := []string{
		seriesIDs[0] + "-episode-1",
		seriesIDs[1] + "-episode-1",
		seriesIDs[2] + "-episode-1",
	}

	userID, profileID, _ := seedNextUpTestOwner(t, ctx, pool, prefix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, seriesIDs)
	})
	for i, seriesID := range seriesIDs {
		seedNextUpSeries(t, ctx, pool, seriesID, fmt.Sprintf("%s Finished %d", prefix, i))
	}
	seedNextUpEpisodes(t, ctx, pool, episodeIDs, seriesIDs, []int{1, 1, 1})

	completedAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (
			user_id, profile_id, media_item_id, position_seconds,
			duration_seconds, completed, updated_at
		)
		SELECT $1, $2, episode_id, 0, 1800, TRUE, $4::timestamptz - (ordinality * INTERVAL '1 minute')
		FROM unnest($3::text[]) WITH ORDINALITY AS fixture(episode_id, ordinality)
	`, userID, profileID, episodeIDs, completedAt); err != nil {
		t.Fatalf("seed exhausted progress: %v", err)
	}

	repo := NewNextUpRepository(pool, nextUpTestStoreProvider{})
	results, err := repo.ListNextUp(ctx, NextUpQuery{
		UserID:          userID,
		ProfileID:       profileID,
		Limit:           20,
		EnableResumable: false,
	})
	if err != nil {
		t.Fatalf("ListNextUp: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("ListNextUp returned %d rows after exhausting finished history: %+v", len(results), results)
	}
}

func seedNextUpTestOwner(t *testing.T, ctx context.Context, pool *pgxpool.Pool, prefix string) (int, string, int) {
	t.Helper()

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('series', $1, TRUE)
		RETURNING id
	`, prefix+" Library").Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}

	var userID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, role)
		VALUES ($1, 'user')
		RETURNING id
	`, prefix+"-user").Scan(&userID); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
		t.Fatalf("seed user: %v", err)
	}

	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_profiles (id, user_id, name)
		VALUES ($1, $2, 'Next Up Regression')
	`, profileID, userID); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
		t.Fatalf("seed profile: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	return userID, profileID, folderID
}

func seedNextUpSeries(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seriesID, title string) {
	t.Helper()

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', $2, 'matched', '{}'::text[])
	`, seriesID, title); err != nil {
		t.Fatalf("seed series %s: %v", seriesID, err)
	}
}

func seedNextUpEpisodes(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	episodeIDs []string,
	seriesIDs []string,
	episodeNumbers []int,
) {
	t.Helper()

	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		SELECT episode_id, series_id, 1, episode_number, 'Next Up Episode ' || episode_number::text
		FROM unnest($1::text[], $2::text[], $3::int[]) AS fixture(episode_id, series_id, episode_number)
	`, episodeIDs, seriesIDs, episodeNumbers); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}
}

func seedNextUpFiles(t *testing.T, ctx context.Context, pool *pgxpool.Pool, folderID int, episodeIDs []string) {
	t.Helper()

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (episode_id, media_folder_id, file_path, duration)
		SELECT episode_id, $2, '/nextup-regression/' || episode_id || '.mkv', 1800
		FROM unnest($1::text[]) AS fixture(episode_id)
	`, episodeIDs, folderID); err != nil {
		t.Fatalf("seed media files: %v", err)
	}
}

func assertNextUpContentIDs(t *testing.T, results []NextUpResult, want ...string) {
	t.Helper()

	if len(results) != len(want) {
		t.Fatalf("ListNextUp returned %d rows, want %d: %+v", len(results), len(want), results)
	}
	got := make(map[string]bool, len(results))
	for _, result := range results {
		got[result.ContentID] = true
	}
	for _, contentID := range want {
		if !got[contentID] {
			t.Errorf("ListNextUp missing %s: %+v", contentID, results)
		}
	}
}

func TestBuildListNextUpQuery_EnableResumableSkipsSeriesSuppressionCTE(t *testing.T) {
	t.Parallel()

	query, _ := buildListNextUpQuery(NextUpQuery{
		UserID:          7,
		ProfileID:       "profile-1",
		EnableResumable: true,
	}, 20, nil)

	if strings.Contains(query, "eligible_series AS (") {
		t.Fatalf("expected resumable query to skip eligible_series suppression CTE, got:\n%s", query)
	}
	if !strings.Contains(query, "FROM completed_episodes es") {
		t.Fatalf("expected resumable query to read directly from completed_episodes, got:\n%s", query)
	}
}

func TestBuildListResumableFirstEpisodesQuery_GlobalKeepsCompletedSeriesGate(t *testing.T) {
	t.Parallel()

	// Global /Shows/NextUp?enableResumable=true: the resumable branch must
	// still skip series the user has completed any episode of, otherwise it
	// would double-fire alongside buildListNextUpQuery's main path.
	query, args := buildListResumableFirstEpisodesQuery(NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
	}, []string{"ep-1", "ep-2"})

	if !strings.Contains(query, "uwp_c.completed = TRUE") {
		t.Fatalf("global query must keep the completed-series gate, got:\n%s", query)
	}
	if strings.Contains(query, "AND e.series_id =") {
		t.Fatalf("global query must not have a series filter, got:\n%s", query)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args without SeriesID, got %d (%v)", len(args), args)
	}
}

func TestBuildListResumableFirstEpisodesQuery_SeriesScopedDropsCompletedGate(t *testing.T) {
	t.Parallel()

	// /Shows/Upcoming for a single series: the completed-series gate must
	// be dropped so a user who finished S01E01 and is mid-watching S01E02
	// still gets E02 back. Without the gate-drop the endpoint silently
	// returns the next aired episode (S01E03) — the Codex P2 finding
	// flagged on PR #64.
	query, args := buildListResumableFirstEpisodesQuery(NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
		SeriesID:  "series-42",
	}, []string{"ep-1", "ep-2"})

	if strings.Contains(query, "uwp_c.completed = TRUE") {
		t.Fatalf("series-scoped query must drop the completed-series gate, got:\n%s", query)
	}
	if !strings.Contains(query, "AND e.series_id = $4") {
		t.Fatalf("series-scoped query must filter by series_id at SQL level, got:\n%s", query)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args with SeriesID, got %d (%v)", len(args), args)
	}
	if got, want := args[3], "series-42"; got != want {
		t.Fatalf("expected SeriesID arg %q, got %v", want, got)
	}
}
