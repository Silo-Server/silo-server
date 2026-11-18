package notifications

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type allLibrariesScope struct{}

func (allLibrariesScope) Resolve(_ context.Context, input access.ResolveInput) (access.Scope, error) {
	return access.Scope{UserID: input.UserID, ProfileID: input.ProfileID}, nil
}

// TestRecomputeSeriesFollowsHomeRemovalsPostgres walks one profile's interest
// in one series through each Home removal against the production stores:
// per-card Next Up dismissals, then a series drop made through the tracker the
// dismissal handler uses. It starts from a row only a retired favorite flag
// kept, which a recompute deletes.
func TestRecomputeSeriesFollowsHomeRemovalsPostgres(t *testing.T) {
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

	nonce := time.Now().UnixNano()
	prefix := fmt.Sprintf("home-hides-%d", nonce)
	libraryID := 900000 + int(nonce%90000)
	seriesID := prefix + "-series"
	// E3 has no file, so Home's Next Up skips it.
	episodes := []string{prefix + "-e1", prefix + "-e2", prefix + "-e3", prefix + "-e4"}
	const profileID = "profile-home-hides"

	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`,
		prefix, prefix+"@example.test",
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		if _, err := pool.Exec(cleanup, `DELETE FROM profile_series_interest WHERE series_id = $1`, seriesID); err != nil {
			t.Errorf("clean up: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_files WHERE media_folder_id = $1`, libraryID); err != nil {
			t.Errorf("clean up: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID); err != nil {
			t.Errorf("clean up: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, libraryID); err != nil {
			t.Errorf("clean up: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Errorf("clean up: %v", err)
		}
	})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_folders (id, type, name) VALUES ($1, 'series', $2)`, []any{libraryID, prefix}},
		{`INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'series', 'Series', '{}')`, []any{seriesID}},
		{`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, []any{seriesID, libraryID}},
		{`INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1 || '-s1', $1, 1)`, []any{seriesID}},
		{`INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title)
			VALUES ($1, $5, $5 || '-s1', 1, 1, 'E1'), ($2, $5, $5 || '-s1', 1, 2, 'E2'),
			       ($3, $5, $5 || '-s1', 1, 3, 'E3'), ($4, $5, $5 || '-s1', 1, 4, 'E4')`,
			[]any{episodes[0], episodes[1], episodes[2], episodes[3], seriesID}},
		{`INSERT INTO media_files (id, media_folder_id, file_path, episode_id) VALUES
			($1, $2, $3 || '/e1.mkv', $4), ($1 + 1, $2, $3 || '/e2.mkv', $5), ($1 + 2, $2, $3 || '/e4.mkv', $6)`,
			[]any{nonce % 1_000_000_000_000, libraryID, "/" + prefix, episodes[0], episodes[1], episodes[3]}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed catalog: %v", err)
		}
	}

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Viewer"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	watchedAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	setProgress := func(episodeID string, completed bool, at time.Time) {
		t.Helper()
		position := 30.0
		if completed {
			position = 100
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
			VALUES ($1, $2, $3, $4, 100, $5, $6)
			ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE
			SET position_seconds = EXCLUDED.position_seconds, completed = EXCLUDED.completed, updated_at = EXCLUDED.updated_at`,
			userID, profileID, episodeID, position, completed, at); err != nil {
			t.Fatalf("seed progress: %v", err)
		}
	}
	system := &System{}
	updater := NewInterestUpdater(pool, NewInterestRepository(pool), provider, allLibrariesScope{})
	system.Interest = updater

	// A favorite gives no interest of its own: a row with no Next Up
	// candidate and no completed episode is deleted by the next recompute.
	if _, err := store.AddFavorite(ctx, profileID, seriesID); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO profile_series_interest (user_id, profile_id, library_id, series_id) VALUES ($1, $2, $3, $4)`,
		userID, profileID, libraryID, seriesID); err != nil {
		t.Fatalf("seed inert interest: %v", err)
	}
	if err := updater.RecomputeSeries(ctx, userID, profileID, seriesID); err != nil {
		t.Fatalf("RecomputeSeries: %v", err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM profile_series_interest WHERE series_id = $1`, seriesID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("inert interest rows = %d, %v; want the row deleted", left, err)
	}

	setProgress(episodes[0], true, watchedAt)
	setProgress(episodes[1], false, watchedAt)

	recompute := func(stage string, wantNextUp bool, wantNextExpected int) {
		t.Helper()
		if err := updater.RecomputeSeries(ctx, userID, profileID, seriesID); err != nil {
			t.Fatalf("%s: RecomputeSeries: %v", stage, err)
		}
		var nextUp bool
		var nextExpected *int
		if err := pool.QueryRow(ctx, `
			SELECT next_up_candidate, next_expected_episode_key
			FROM profile_series_interest
			WHERE profile_id = $1 AND library_id = $2 AND series_id = $3`,
			profileID, libraryID, seriesID,
		).Scan(&nextUp, &nextExpected); err != nil {
			t.Fatalf("%s: read interest: %v", stage, err)
		}
		if nextUp != wantNextUp {
			t.Fatalf("%s: next_up_candidate = %v, want %v", stage, nextUp, wantNextUp)
		}
		if nextExpected == nil || *nextExpected != wantNextExpected {
			t.Fatalf("%s: next_expected_episode_key = %v, want %d", stage, nextExpected, wantNextExpected)
		}
	}
	requireQueued := func(action string) {
		t.Helper()
		updater.mu.Lock()
		defer updater.mu.Unlock()
		if _, ok := updater.pending[interestMutation{userID: userID, profileID: profileID, itemID: seriesID}]; !ok {
			t.Fatalf("%s queued no interest recompute", action)
		}
		clear(updater.pending)
	}
	// The cursor walks past the started E2, and Home's Next Up skips the
	// file-less E3 and shows E4, so E3 is the next episode that could notify.
	afterE2, afterE4 := EpisodeKey(1, 3), EpisodeKey(1, 5)

	recompute("before any removal", true, afterE2)

	dismissedAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	// Home's Next Up card after E1 skips the started E2 and the file-less E3,
	// so it shows E4.
	if err := store.UpsertHomeDismissal(ctx, userstore.HomeItemDismissal{
		ProfileID: profileID, Surface: userstore.HomeSurfaceNextUp, MediaItemID: episodes[3],
		SeriesID: &seriesID, DismissedAt: dismissedAt,
	}); err != nil {
		t.Fatalf("dismiss from Next Up: %v", err)
	}
	recompute("removed from Next Up", false, afterE2)

	// Resuming E2: the Next Up card is still E4, so it stays dismissed, and
	// the newer progress on E2 keeps the series off Next Up anyway.
	setProgress(episodes[1], false, time.Now().Add(-3*time.Minute).UTC().Truncate(time.Second))
	recompute("resumed E2", false, afterE2)

	// Finishing E2 leaves E4 as the Next Up card.
	setProgress(episodes[1], true, time.Now().Add(-2*time.Minute).UTC().Truncate(time.Second))
	recompute("finished E2", false, afterE2)

	// Finishing E4 moves Next Up past the dismissed card.
	setProgress(episodes[3], true, time.Now().Add(-time.Minute).UTC().Truncate(time.Second))
	recompute("finished E4", true, afterE4)

	drops := TrackDroppedSeries(catalog.NewDroppedSeriesRepo(pool), system)
	if err := drops.Drop(ctx, userID, profileID, seriesID); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	requireQueued("dropping the series")
	updater.mu.Lock()
	_, deferred := updater.deferred[interestMutation{userID: userID, profileID: profileID, itemID: seriesID}]
	updater.mu.Unlock()
	if !deferred {
		t.Fatal("dropping the series deferred no second recompute")
	}
	recompute("series dropped", false, afterE4)

	if err := drops.Undrop(ctx, userID, profileID, seriesID); err != nil {
		t.Fatalf("Undrop: %v", err)
	}
	requireQueued("undropping the series")
	recompute("series undropped", true, afterE4)

	// Drops imported from, and removed by, a watch provider.
	droppedAt := time.Now().UTC().Truncate(time.Microsecond)
	if applied, err := drops.ImportDrop(ctx, userID, profileID, seriesID, droppedAt, nil); err != nil || !applied {
		t.Fatalf("ImportDrop = %v, %v", applied, err)
	}
	requireQueued("importing a drop")
	recompute("drop imported", false, afterE4)

	if removed, err := drops.DeleteIfUnchanged(ctx, userID, profileID, seriesID, droppedAt); err != nil || !removed {
		t.Fatalf("DeleteIfUnchanged = %v, %v", removed, err)
	}
	requireQueued("removing an imported drop")
	recompute("imported drop removed", true, afterE4)

	// Home anchors Next Up on the most recently completed episode. E3 gets a
	// file and the profile rewatches E1, so Home's card becomes E3 even though
	// E4 is the highest episode watched. The cursor walks from E1 past the
	// started E2 and stops at E3.
	if _, err := pool.Exec(ctx, `INSERT INTO media_files (id, media_folder_id, file_path, episode_id) VALUES ($1, $2, $3 || '/e3.mkv', $4)`,
		nonce%1_000_000_000_000+3, libraryID, "/"+prefix, episodes[2]); err != nil {
		t.Fatalf("add E3 file: %v", err)
	}
	setProgress(episodes[0], true, time.Now().UTC().Truncate(time.Second))
	recompute("Next Up moved to E3 despite the old E4 dismissal", true, afterE2)
	if err := store.UpsertHomeDismissal(ctx, userstore.HomeItemDismissal{
		ProfileID: profileID, Surface: userstore.HomeSurfaceNextUp, MediaItemID: episodes[2],
		SeriesID: &seriesID, DismissedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("dismiss E3 from Next Up: %v", err)
	}
	recompute("E3 card dismissed after a rewatch", false, afterE2)

	// Home's Next Up counts any progress row as started, even one a history
	// hide covers. Starting E3 and hiding it leaves no card after E1, and the
	// cursor walks past every started episode to after E4. E3's progress is
	// older than the E1 rewatch, so it does not keep the series off Next Up.
	startedAt := time.Now().Add(-30 * time.Second).UTC().Truncate(time.Second)
	setProgress(episodes[2], false, startedAt)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before, updated_at)
		VALUES ($1, $2, $3, $4, $4)`, userID, profileID, episodes[2], startedAt.Add(time.Second)); err != nil {
		t.Fatalf("hide E3: %v", err)
	}
	recompute("E3 started and hidden", true, afterE4)
}

// TestRecomputeSeriesNextUpCardSkipsSpecialAnchorPostgres pins that the Next
// Up card notifications check is the one Home shows when the profile last
// finished a special: the next regular episode, not the next special.
func TestRecomputeSeriesNextUpCardSkipsSpecialAnchorPostgres(t *testing.T) {
	watchedAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	got := recomputeSeriesInterest(t, "special-anchor", recomputeCase{
		episodes:  [][2]int{{0, 5}, {0, 6}, {2, 4}, {2, 5}},
		progress:  []progressAt{{season: 2, number: 4, at: watchedAt}, {season: 0, number: 5, at: watchedAt.Add(time.Minute)}},
		dismissed: &[2]int{2, 5},
	})
	if got.nextUp {
		t.Fatal("next_up_candidate = true; the dismissed S02E05 is Home's Next Up card, so want false")
	}
}

// TestRecomputeSeriesNextUpAnchorKeepsSubsecondOrderPostgres pins that two
// completions within one second anchor on the later one, as Home's Next Up
// does with the full timestamps (#2878), not on the higher episode.
func TestRecomputeSeriesNextUpAnchorKeepsSubsecondOrderPostgres(t *testing.T) {
	second := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	got := recomputeSeriesInterest(t, "subsecond-anchor", recomputeCase{
		episodes: [][2]int{{2, 4}, {2, 5}, {2, 8}, {2, 9}},
		progress: []progressAt{
			{season: 2, number: 8, at: second.Add(100 * time.Millisecond)},
			{season: 2, number: 4, at: second.Add(900 * time.Millisecond)},
		},
		dismissed: &[2]int{2, 5},
	})
	if got.nextUp {
		t.Fatal("next_up_candidate = true; S02E04 was completed last, so the dismissed S02E05 is Home's Next Up card and want false")
	}
}

// TestRecomputeSeriesNextUpCursorPostgres pins the Next Up cursor and
// next_up_candidate against the production stores: the cursor follows Home's
// anchor and walks past started episodes, completed history anchors a series
// with no completed progress, and newer partial progress suppresses Next Up.
func TestRecomputeSeriesNextUpCursorPostgres(t *testing.T) {
	watchedAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	at := func(minutes int) time.Time { return watchedAt.Add(time.Duration(minutes) * time.Minute) }
	for _, tc := range []struct {
		name       string
		c          recomputeCase
		wantCursor int
		wantNextUp bool
	}{
		{
			// The finale's key + 1 matches no episode; fanout lets S02E01
			// through because nothing listed lies between.
			name: "finished season",
			c: recomputeCase{
				episodes: [][2]int{{1, 1}, {1, 2}, {1, 3}},
				progress: []progressAt{{season: 1, number: 1, at: at(0)}, {season: 1, number: 2, at: at(1)}, {season: 1, number: 3, at: at(2)}},
			},
			wantCursor: EpisodeKey(1, 4), wantNextUp: true,
		},
		{
			// Caught up through S02 and rewatching S01E01: the cursor walks
			// past every started episode to after S02E02.
			name: "rewatch",
			c: recomputeCase{
				episodes: [][2]int{{1, 1}, {1, 2}, {2, 1}, {2, 2}},
				progress: []progressAt{
					{season: 1, number: 2, at: at(1)}, {season: 2, number: 1, at: at(2)},
					{season: 2, number: 2, at: at(3)}, {season: 1, number: 1, at: at(10)},
				},
			},
			wantCursor: EpisodeKey(2, 3), wantNextUp: true,
		},
		{
			// S01E02 is listed without a file. The walk stops on it although
			// S01E03 after it is started, so S01E02 notifies when it lands.
			name: "listed episode without a file",
			c: recomputeCase{
				episodes: [][2]int{{1, 1}, {1, 3}},
				fileless: [][2]int{{1, 2}},
				progress: []progressAt{{season: 1, number: 3, at: at(0)}, {season: 1, number: 1, at: at(1)}},
			},
			wantCursor: EpisodeKey(1, 2), wantNextUp: true,
		},
		{
			// The walk stops at the first listed episode not started.
			name: "rewatch with an unwatched episode",
			c: recomputeCase{
				episodes: [][2]int{{1, 1}, {1, 2}, {1, 3}, {1, 4}},
				progress: []progressAt{{season: 1, number: 2, at: at(1)}, {season: 1, number: 4, at: at(2)}, {season: 1, number: 1, at: at(10)}},
			},
			wantCursor: EpisodeKey(1, 3), wantNextUp: true,
		},
		{
			name: "history only",
			c: recomputeCase{
				episodes: [][2]int{{1, 1}, {1, 2}, {1, 3}},
				history:  [][2]int{{1, 1}, {1, 2}},
			},
			wantCursor: EpisodeKey(1, 3), wantNextUp: true,
		},
		{
			// Without a progress anchor any episode in progress suppresses.
			name: "history only with an episode in progress",
			c: recomputeCase{
				episodes: [][2]int{{1, 1}, {1, 2}, {1, 3}},
				history:  [][2]int{{1, 1}},
				progress: []progressAt{{season: 1, number: 2, at: at(0), position: 30}},
			},
			wantCursor: EpisodeKey(1, 3), wantNextUp: false,
		},
		{
			name: "partly watched after the last finish",
			c: recomputeCase{
				episodes: [][2]int{{1, 1}, {1, 2}, {1, 3}},
				progress: []progressAt{{season: 1, number: 1, at: at(0)}, {season: 1, number: 2, at: at(1), position: 30}},
			},
			wantCursor: EpisodeKey(1, 3), wantNextUp: false,
		},
		{
			// Older partial progress does not block Home's Next Up, and the
			// started episode is walked past.
			name: "partly watched before the last finish",
			c: recomputeCase{
				episodes: [][2]int{{1, 1}, {1, 2}, {1, 3}},
				progress: []progressAt{{season: 1, number: 2, at: at(0), position: 30}, {season: 1, number: 1, at: at(1)}},
			},
			wantCursor: EpisodeKey(1, 3), wantNextUp: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := recomputeSeriesInterest(t, "cursor", tc.c)
			if got.nextExpected == nil || *got.nextExpected != tc.wantCursor {
				t.Errorf("next_expected_episode_key = %v, want %d", got.nextExpected, tc.wantCursor)
			}
			if got.nextUp != tc.wantNextUp {
				t.Errorf("next_up_candidate = %v, want %v", got.nextUp, tc.wantNextUp)
			}
		})
	}
}

// progressAt is a seeded progress row: completed when position is zero,
// otherwise in progress at that position.
type progressAt struct {
	season, number int
	at             time.Time
	position       float64
}

// recomputeCase is one profile's state in one series: episodes with a file,
// episodes without one, progress rows, completed history entries, and an
// optional Next Up dismissal.
type recomputeCase struct {
	episodes, fileless [][2]int
	progress           []progressAt
	history            [][2]int
	dismissed          *[2]int
}

// recomputeResult is the interest row RecomputeSeries wrote.
type recomputeResult struct {
	nextUp       bool
	nextExpected *int
}

// recomputeSeriesInterest seeds the series and the profile's state described
// by c, runs RecomputeSeries, and returns the interest row it wrote.
func recomputeSeriesInterest(t *testing.T, name string, c recomputeCase) recomputeResult {
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

	nonce := time.Now().UnixNano()
	prefix := fmt.Sprintf("%s-%d", name, nonce)
	libraryID := 900000 + int(nonce%90000)
	seriesID := prefix + "-series"
	profileID := "profile-" + name
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`,
		prefix, prefix+"@example.test",
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM profile_series_interest WHERE series_id = $1`, seriesID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_files WHERE media_folder_id = $1`, libraryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, libraryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM user_watch_history WHERE user_id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
	})
	episode := func(season, number int) string { return fmt.Sprintf("%s-s%02de%02d", prefix, season, number) }
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_folders (id, type, name) VALUES ($1, 'series', $2)`, []any{libraryID, prefix}},
		{`INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'series', 'Series', '{}')`, []any{seriesID}},
		{`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, []any{seriesID, libraryID}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed catalog: %v", err)
		}
	}
	for _, e := range slices.Concat(c.episodes, c.fileless) {
		if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, $3, $4, $1)`,
			episode(e[0], e[1]), seriesID, e[0], e[1]); err != nil {
			t.Fatalf("seed episode: %v", err)
		}
	}
	for _, e := range c.episodes {
		if _, err := pool.Exec(ctx, `INSERT INTO media_files (media_folder_id, file_path, episode_id) VALUES ($1, '/' || $2::text, $2)`,
			libraryID, episode(e[0], e[1])); err != nil {
			t.Fatalf("seed file: %v", err)
		}
	}

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Viewer"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	for _, p := range c.progress {
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
			VALUES ($1, $2, $3, $4, 100, $5, $6)`, userID, profileID, episode(p.season, p.number), p.position, p.position == 0, p.at); err != nil {
			t.Fatalf("seed progress: %v", err)
		}
	}
	seedCompletedHistory(t, pool, userID, profileID, c.history, episode)
	if c.dismissed != nil {
		if err := store.UpsertHomeDismissal(ctx, userstore.HomeItemDismissal{
			ProfileID: profileID, Surface: userstore.HomeSurfaceNextUp, MediaItemID: episode(c.dismissed[0], c.dismissed[1]),
			SeriesID: &seriesID, DismissedAt: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("dismiss from Next Up: %v", err)
		}
	}

	updater := NewInterestUpdater(pool, NewInterestRepository(pool), provider, allLibrariesScope{})
	if err := updater.RecomputeSeries(ctx, userID, profileID, seriesID); err != nil {
		t.Fatalf("RecomputeSeries: %v", err)
	}
	var got recomputeResult
	if err := pool.QueryRow(ctx, `
		SELECT next_up_candidate, next_expected_episode_key FROM profile_series_interest
		WHERE profile_id = $1 AND library_id = $2 AND series_id = $3`,
		profileID, libraryID, seriesID,
	).Scan(&got.nextUp, &got.nextExpected); err != nil {
		t.Fatalf("read interest: %v", err)
	}
	return got
}

// seedCompletedHistory writes a completed watch history entry, with no
// progress row, for each episode, as a history import does.
func seedCompletedHistory(t *testing.T, pool *pgxpool.Pool, userID int, profileID string, episodes [][2]int, episode func(season, number int) string) {
	t.Helper()
	for _, e := range episodes {
		if _, err := pool.Exec(t.Context(), `
			INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed)
			VALUES ($3 || '-history', $1, $2, $3, now() - interval '1 day', true)`, userID, profileID, episode(e[0], e[1])); err != nil {
			t.Fatalf("seed history: %v", err)
		}
	}
}
