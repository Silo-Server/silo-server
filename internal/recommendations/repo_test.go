package recommendations

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTasteSeedCandidateQueryOrdersByReliableColdStartSignals(t *testing.T) {
	query := strings.Join(strings.Fields(tasteSeedCandidateQuery), " ")

	assertQueryTermsInOrder(t, query,
		"ORDER BY COALESCE(wc.watch_count, 0) DESC",
		"WHEN mi.rating_imdb IS NOT NULL THEN 2",
		"WHEN mi.rating_tmdb IS NOT NULL AND mi.rating_tmdb < 9.5 THEN 1",
		"ELSE 0 END DESC",
		"mi.rating_imdb DESC NULLS LAST",
		"CASE WHEN mi.rating_tmdb < 9.5 THEN mi.rating_tmdb END DESC NULLS LAST",
		"mi.year DESC NULLS LAST",
		"mi.content_id ASC",
	)

	if strings.Contains(query, "ORDER BY COALESCE(wc.watch_count, 0) DESC, mi.rating_tmdb DESC NULLS LAST") {
		t.Fatal("taste seed query must not rank cold-start candidates by raw TMDB rating directly")
	}
}

func TestTasteSeedCandidateQueryIncludesEbooks(t *testing.T) {
	query := strings.Join(strings.Fields(tasteSeedCandidateQuery), " ")

	for _, term := range []string{
		"(mi.status = 'matched' OR mi.type = 'audiobook' OR mi.type = 'ebook')",
		"mi.type IN ('movie', 'series', 'audiobook', 'ebook')",
	} {
		if !strings.Contains(query, term) {
			t.Fatalf("taste seed candidate query missing %q: %s", term, query)
		}
	}
}

func TestItemWatchersQueryCountsDistinctWatchersPerItem(t *testing.T) {
	query := strings.Join(strings.Fields(itemWatchersQuery), " ")

	// watched_activity rolls episodes up to their parent series, so one
	// watcher can emit many rows per item. The pairs must be deduplicated
	// before ROW_NUMBER ranking and the HAVING watcher count, otherwise a
	// single binge-watcher inflates both.
	assertQueryTermsInOrder(t, query,
		"ROW_NUMBER() OVER (PARTITION BY user_id, profile_id ORDER BY MAX(updated_at) DESC) AS rn",
		"GROUP BY user_id, profile_id, watcher_id, item_id",
		"WHERE rn <= $1",
		"GROUP BY media_item_id",
		"HAVING COUNT(DISTINCT user_id) >= $2",
	)
}

func TestItemWatchersQueryMinWatchersCountsDistinctAccounts(t *testing.T) {
	query := strings.Join(strings.Fields(itemWatchersQuery), " ")

	// The minimum-watchers floor is a sparsity/privacy threshold: one login
	// account with N household profiles must not satisfy it by itself, so the
	// HAVING clause counts distinct user_id (accounts), not watcher rows. The
	// per-profile watcher_id array is still aggregated for Jaccard math.
	assertQueryTermsInOrder(t, query,
		"SELECT user_id, watcher_id, item_id AS media_item_id",
		"ARRAY_AGG(watcher_id) AS watchers",
		"HAVING COUNT(DISTINCT user_id) >= $2",
	)
	if strings.Contains(query, "HAVING COUNT(*) >= $2") {
		t.Fatal("minWatchers floor must not count per-profile watcher rows")
	}
}

func TestEbookReaderProgressForUserQueryGatesHiddenHistory(t *testing.T) {
	query := strings.Join(strings.Fields(ebookReaderProgressForUserQuery), " ")

	// Mirrors GetWatchProgressForUser: rows the user hid stay hidden while
	// updated_at <= hidden_before; reading activity after hidden_before
	// surfaces again.
	assertQueryTermsInOrder(t, query,
		"FROM ebook_reader_progress",
		"NOT EXISTS",
		"FROM user_history_hidden_items hhi",
		"hhi.media_item_id = ebook_reader_progress.content_id",
		"ebook_reader_progress.updated_at <= hhi.hidden_before",
	)
}

func TestWatchedActivityCTEIncludesEbookReaderProgress(t *testing.T) {
	query := strings.Join(strings.Fields(watchedActivityCTE), " ")

	for _, term := range []string{
		"FROM ebook_reader_progress erp",
		"erp.progress >= 0.5",
		"erp.progress >= 0.9 AS completed",
		"hhi.media_item_id = erp.content_id",
	} {
		if !strings.Contains(query, term) {
			t.Fatalf("watched activity CTE missing %q: %s", term, query)
		}
	}
}

func TestRecentCompletedItemIDsQueryCanonicalizesBeforeLimit(t *testing.T) {
	query := strings.Join(strings.Fields(recentCompletedItemIDsQuery), " ")

	assertQueryTermsInOrder(t, query,
		"SELECT item_id",
		"FROM watched_activity",
		"WHERE user_id = $1 AND profile_id = $2 AND completed = true",
		"GROUP BY item_id",
		"ORDER BY MAX(updated_at) DESC, item_id ASC",
		"LIMIT $3",
	)
	if strings.Contains(query, "SELECT leaf_item_id") {
		t.Fatalf("automatic anchors must not use episode leaf IDs: %s", query)
	}
}

func TestResolveCanonicalItemIDsDefaultsToInputAndRollsEpisodesUp(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := context.Background()
	prefix := "t612-anchor-"
	seriesID := prefix + "series"
	episodeID := prefix + "episode"
	movieID := prefix + "movie"

	cleanupRecoMediaItems(t, pool, prefix)
	seedRecoMediaItem(t, pool, seriesID, "series", "matched")
	seedRecoMediaItem(t, pool, movieID, "movie", "matched")
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $2, 1, 1, 'Episode 1')
	`, episodeID, seriesID); err != nil {
		t.Fatalf("seed episode: %v", err)
	}

	got, err := NewRepo(pool).ResolveCanonicalItemIDs(ctx, []string{episodeID, movieID, "unknown-id"})
	if err != nil {
		t.Fatalf("ResolveCanonicalItemIDs: %v", err)
	}
	want := map[string]string{episodeID: seriesID, movieID: movieID, "unknown-id": "unknown-id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved IDs = %#v, want %#v", got, want)
	}
}

func TestEmbeddingEligibilityWhereClauseIncludesBookTypes(t *testing.T) {
	clause := embeddingEligibilityWhereClause()

	for _, term := range []string{"mi.status = 'matched'", "mi.type = 'audiobook'", "mi.type = 'ebook'"} {
		if !strings.Contains(clause, term) {
			t.Fatalf("embedding eligibility clause missing %q: %s", term, clause)
		}
	}
}

func TestEmbeddingEligibilityWhereClauseUsesMediaItemsAlias(t *testing.T) {
	clause := embeddingEligibilityWhereClause()

	if strings.Contains(clause, " type =") || strings.Contains(clause, " status =") {
		t.Fatalf("embedding eligibility clause should qualify media_items columns: %s", clause)
	}
}

func TestRecommendationItemEligibilityWhereClauseIncludesBookTypes(t *testing.T) {
	clause := recommendationItemEligibilityWhereClause("mi")

	for _, term := range []string{"mi.status = 'matched'", "mi.type = 'audiobook'", "mi.type = 'ebook'"} {
		if !strings.Contains(clause, term) {
			t.Fatalf("recommendation item eligibility clause missing %q: %s", term, clause)
		}
	}
}

func TestRecommendationItemEligibilityWhereClauseDefaultsAlias(t *testing.T) {
	clause := recommendationItemEligibilityWhereClause("")

	if !strings.Contains(clause, "media_items.status = 'matched'") {
		t.Fatalf("default eligibility alias missing media_items qualification: %s", clause)
	}
}

func TestTasteProfileRefreshSubjectsQueryIncludesEbookReaderProgress(t *testing.T) {
	query := strings.Join(strings.Fields(tasteProfileRefreshSubjectsQuery), " ")

	assertQueryTermsInOrder(t, query,
		"SELECT DISTINCT user_id, profile_id FROM user_watch_progress",
		"UNION",
		"SELECT DISTINCT user_id, profile_id FROM ebook_reader_progress",
	)
}

func TestBuildItemsNeedingEmbeddingSQLIsCheap(t *testing.T) {
	query := buildItemsNeedingEmbeddingSQL()

	// The whole point of the cheap pass is that it never pays the per-row
	// item_people LATERAL joins that the text-staleness CTE needs. If either of
	// these ever reappears here, Pass 1 has stopped being cheap.
	if strings.Contains(query, "item_people") {
		t.Fatalf("cheap embedding query must not reference item_people: %s", query)
	}
	if strings.Contains(query, "LATERAL") {
		t.Fatalf("cheap embedding query must not use LATERAL joins: %s", query)
	}

	normalized := strings.Join(strings.Fields(query), " ")
	assertQueryTermsInOrder(t, normalized,
		"LEFT JOIN media_item_embeddings e ON e.media_item_id = mi.content_id",
		"e.media_item_id IS NULL OR e.model != $1",
		"$2 = '' OR mi.content_id > $2",
		"ORDER BY mi.content_id",
		"LIMIT $3",
	)
}

func TestItemsNeedingEmbeddingMissingAndModelStaleOnly(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := context.Background()

	const (
		prefix       = "t7cheap-"
		currentModel = "model-current"
		oldModel     = "model-old"
	)
	cleanupRecoMediaItems(t, pool, prefix)

	missingID := prefix + "1-missing"
	modelStaleID := prefix + "2-modelstale"
	textStaleID := prefix + "3-textstale"

	// (i) matched movie, no embedding row at all -> cheap candidate.
	seedRecoMediaItem(t, pool, missingID, "movie", "matched")
	// (ii) embedding exists but under an OLD model -> cheap candidate.
	seedRecoMediaItem(t, pool, modelStaleID, "movie", "matched")
	seedRecoEmbedding(t, pool, modelStaleID, oldModel, modelStaleID)
	// (iii) embedding under the CURRENT model. The cheap query only looks at
	// model identity, so this row must NOT appear even though its cast changed
	// (text-staleness is Pass 2's job, detected by the expensive CTE).
	seedRecoMediaItem(t, pool, textStaleID, "movie", "matched")
	seedRecoEmbedding(t, pool, textStaleID, currentModel, "stale canonical text")

	repo := NewRepo(pool)

	got, err := repo.ItemsNeedingEmbedding(ctx, currentModel, "", 100)
	if err != nil {
		t.Fatalf("ItemsNeedingEmbedding: %v", err)
	}
	gotSet := make(map[string]bool, len(got))
	for _, id := range got {
		gotSet[id] = true
	}
	if !gotSet[missingID] {
		t.Errorf("missing-embedding item %q should be a cheap candidate; got %v", missingID, got)
	}
	if !gotSet[modelStaleID] {
		t.Errorf("model-stale item %q should be a cheap candidate; got %v", modelStaleID, got)
	}
	if gotSet[textStaleID] {
		t.Errorf("text-stale-only item %q must NOT be a cheap candidate; got %v", textStaleID, got)
	}

	// Results are ordered by content_id, so paging past the first id must skip
	// it and still surface the rest. This is what lets EmbedAll advance its
	// cursor over a failed/skipped item without re-fetching it forever.
	if len(got) < 2 {
		t.Fatalf("expected at least 2 cheap candidates to exercise the cursor, got %v", got)
	}
	first := got[0]
	after, err := repo.ItemsNeedingEmbedding(ctx, currentModel, first, 100)
	if err != nil {
		t.Fatalf("ItemsNeedingEmbedding(afterID): %v", err)
	}
	for _, id := range after {
		if id == first {
			t.Fatalf("cursor afterID=%q still returned %q", first, first)
		}
		if id <= first {
			t.Fatalf("cursor returned id %q <= afterID %q; ordering broken", id, first)
		}
	}
}

// seedRecoMediaItem inserts a minimal embed-eligible media item whose title is
// its content_id. Delegates to seedRecoMediaItemTitled so both the cheap-query
// and EmbedAll tests seed rows that survive ItemRepository.GetByIDs.
func seedRecoMediaItem(t *testing.T, pool *pgxpool.Pool, contentID, mediaType, status string) {
	t.Helper()
	seedRecoMediaItemTitled(t, pool, contentID, mediaType, status, contentID)
}

// seedRecoEmbedding inserts a zero-vector embedding row with an explicit model
// and canonical_text. The vector width matches the migrated column (3072).
func seedRecoEmbedding(t *testing.T, pool *pgxpool.Pool, contentID, model, canonicalText string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
		VALUES (
			$1,
			(SELECT array_agg(0.0::real) FROM generate_series(1, 3072))::vector,
			$2,
			$3
		)
	`, contentID, model, canonicalText); err != nil {
		t.Fatalf("seed embedding %s: %v", contentID, err)
	}
}

func cleanupRecoMediaItems(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		// media_item_embeddings and item_people rows cascade on media_items delete.
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
}

func assertQueryTermsInOrder(t *testing.T, query string, terms ...string) {
	t.Helper()

	searchFrom := 0
	for _, term := range terms {
		idx := strings.Index(query[searchFrom:], term)
		if idx < 0 {
			t.Fatalf("query term %q missing or out of order in query:\n%s", term, query)
		}
		searchFrom += idx + len(term)
	}
}

func TestRecentCompletedItemIDsGroupsSeriesBeforeLimit(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "t612-recent-"
	const profile = "66100000-0000-4000-8000-000000000001"
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'anchor test')`, profile, userID); err != nil {
		t.Fatal(err)
	}
	cleanupRecoMediaItems(t, pool, prefix)
	seedRecoMediaItem(t, pool, prefix+"series", "series", "matched")
	seedRecoMediaItem(t, pool, prefix+"movie-a", "movie", "matched")
	seedRecoMediaItem(t, pool, prefix+"movie-b", "movie", "matched")
	for i, id := range []string{prefix + "episode-1", prefix + "episode-2"} {
		if _, err := pool.Exec(ctx, `INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,$3,'Episode')`, id, prefix+"series", i+1); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{prefix + "episode-1", prefix + "episode-2", prefix + "movie-b", prefix + "movie-a"} {
		// The movies tie; the final order must use canonical ID ascending.
		age := min(i, 2)
		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,completed,updated_at) VALUES($1,$2,$3,true,TIMESTAMPTZ '2026-08-10 12:00:00Z' - $4 * INTERVAL '1 second')`, userID, profile, id, age); err != nil {
			t.Fatal(err)
		}
	}
	got, err := NewRepo(pool).GetRecentCompletedItemIDs(ctx, userID, profile, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{prefix + "series", prefix + "movie-a", prefix + "movie-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recent completed = %v, want %v", got, want)
	}
}

func TestRecommendationCacheQueriesStoreGlobalRowsAsNull(t *testing.T) {
	// GlobalCacheUserID is the sentinel the worker/reader pass for global
	// (non-personalized) rows. It must reach the row as NULL so the users(id)
	// foreign key added in 20260912231023_user_fk_integrity does not reject it
	// (issue #1261): no users row has id 0, so a literal 0 fails with 23503.
	if GlobalCacheUserID != 0 {
		t.Fatalf("GlobalCacheUserID = %d, expected 0", GlobalCacheUserID)
	}

	upsert := strings.Join(strings.Fields(upsertRecommendationCacheQuery), " ")
	// The sentinel is mapped to NULL, and the conflict target is the identity
	// index (NULLS NOT DISTINCT) so a NULL-owned row still upserts in place.
	for _, want := range []string{
		"VALUES (NULLIF($1, 0), $2, $3, $4, $5, $6::timestamptz, NOW())",
		"ON CONFLICT (user_id, profile_id, rec_type, source_item_id) DO UPDATE",
	} {
		if !strings.Contains(upsert, want) {
			t.Fatalf("upsert query missing %q: %s", want, upsert)
		}
	}
	if strings.Contains(upsert, "VALUES ($1,") {
		t.Fatalf("upsert must not insert the raw sentinel into user_id: %s", upsert)
	}

	// Reads must match the stored NULL for global rows and keep an indexable
	// predicate: IS NOT DISTINCT FROM forces a sequential scan.
	for _, tc := range []struct {
		name      string
		userID    int
		predicate string
		args      []any
	}{
		{"global", GlobalCacheUserID, "AND user_id IS NULL", []any{"", RecTypePopular, ""}},
		{"account", 7, "AND user_id = $4", []any{"", RecTypePopular, "", 7}},
	} {
		query, args := recommendationCacheLookup(tc.userID, "", RecTypePopular, "")
		get := strings.Join(strings.Fields(query), " ")
		if !strings.HasSuffix(get, tc.predicate) || strings.Contains(get, "DISTINCT") {
			t.Fatalf("%s lookup has wrong owner predicate: %s", tc.name, get)
		}
		if fmt.Sprint(args) != fmt.Sprint(tc.args) {
			t.Fatalf("%s lookup args = %v, want %v", tc.name, args, tc.args)
		}
	}

	samplers := strings.Join(strings.Fields(listCachedGenreSamplersQuery), " ")
	if !strings.Contains(samplers, "WHERE user_id IS NULL AND profile_id = $1") {
		t.Fatalf("genre sampler query must read NULL-owned global rows: %s", samplers)
	}
}
