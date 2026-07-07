package reattribute

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testEnv struct {
	pool      *pgxpool.Pool
	userID    int
	profileID string
	suffix    int64
}

func newTestEnv(t *testing.T) *testEnv {
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

	suffix := time.Now().UnixNano()
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("reattr-user-%d", suffix),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	env := &testEnv{
		pool:      pool,
		userID:    userID,
		profileID: fmt.Sprintf("00000000-0000-4000-8000-%012d", suffix%1_000_000_000_000),
		suffix:    suffix,
	}
	t.Cleanup(func() {
		for _, table := range []string{
			"playback_history_admin", "user_downloads", "downloads", "user_watch_history",
			"user_watch_progress", "user_favorites", "user_watchlist", "user_ratings",
			"user_audio_preferences", "user_subtitle_preferences", "user_series_playback_preferences",
			"user_home_item_dismissals",
		} {
			_, _ = pool.Exec(ctx, `DELETE FROM `+table+` WHERE user_id = $1`, userID)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	return env
}

// seedMediaFile creates a media_folders + media_files pair so tables with a
// real FK on media_file_id (downloads) can be seeded.
func (e *testEnv) seedMediaFile(t *testing.T, contentID, path string) int {
	t.Helper()
	ctx := context.Background()
	var folderID int
	if err := e.pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("reattr-folder-%d", e.suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	var fileID int
	if err := e.pool.QueryRow(ctx, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
		VALUES ($1, $2, $3, 1000) RETURNING id
	`, contentID, folderID, path).Scan(&fileID); err != nil {
		t.Fatalf("seed media file: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID)
		_, _ = e.pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	return fileID
}

func (e *testEnv) run(t *testing.T, opts Options) *Report {
	t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	report, err := Run(ctx, tx, opts)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("Run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return report
}

func (e *testEnv) itemIDOf(t *testing.T, table, where string, args ...any) string {
	t.Helper()
	var id string
	err := e.pool.QueryRow(context.Background(),
		`SELECT media_item_id FROM `+table+` WHERE `+where, args...).Scan(&id)
	if err != nil {
		t.Fatalf("lookup %s: %v", table, err)
	}
	return id
}

func TestRun_FileSubsetExactAndProgress(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	from := fmt.Sprintf("movie-src-%d", env.suffix)
	to := fmt.Sprintf("movie-dst-%d", env.suffix)
	movedFile, stayFile := 910001, 910002

	// Managed offline download on a real media_files row (FK) keyed to the
	// moved file: its content_id must follow the split.
	managedFile := env.seedMediaFile(t, from, fmt.Sprintf("/reattr/%d/a.mkv", env.suffix))
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO downloads (id, user_id, media_file_id, content_id, kind, status)
		VALUES ($1, $2, $3, $4, 'queued', 'completed')
	`, fmt.Sprintf("mdl-%d", env.suffix), env.userID, managedFile, from); err != nil {
		t.Fatalf("seed managed download: %v", err)
	}

	// Session log rows on both files; download on the moved file; progress
	// resuming on the moved file.
	for i, fileID := range []int{movedFile, stayFile} {
		if _, err := env.pool.Exec(ctx, `
			INSERT INTO playback_history_admin (session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at)
			VALUES ($1, $2, $3, $4, $5, 'direct', now(), now())
		`, fmt.Sprintf("sess-%d-%d", env.suffix, i), env.userID, env.profileID, from, fileID); err != nil {
			t.Fatalf("seed session: %v", err)
		}
	}
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO user_downloads (id, user_id, profile_id, media_item_id, media_file_id)
		VALUES ($1, $2, $3, $4, $5)
	`, fmt.Sprintf("dl-%d", env.suffix), env.userID, env.profileID, from, movedFile); err != nil {
		t.Fatalf("seed download: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, last_file_id)
		VALUES ($1, $2, $3, 100, 7200, $4)
	`, env.userID, env.profileID, from, movedFile); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	report := env.run(t, Options{
		FromContentID: from,
		ToContentID:   to,
		MovedFileIDs:  []int{movedFile, managedFile},
		Mode:          HistoryModeEvidence,
	})

	if report.PlaybackSessionLog != 1 || report.Downloads != 2 || report.ProgressMoved != 1 {
		t.Fatalf("report = %+v, want 1 session log, 2 downloads (user+managed), 1 progress moved", report)
	}
	if got := env.itemIDOf(t, "user_downloads", "user_id = $1", env.userID); got != to {
		t.Fatalf("download item = %q, want %q", got, to)
	}
	var managedContentID string
	if err := env.pool.QueryRow(ctx,
		`SELECT content_id FROM downloads WHERE user_id = $1`, env.userID).Scan(&managedContentID); err != nil {
		t.Fatalf("load managed download: %v", err)
	}
	if managedContentID != to {
		t.Fatalf("managed download content_id = %q, want %q", managedContentID, to)
	}
	if got := env.itemIDOf(t, "user_watch_progress", "user_id = $1", env.userID); got != to {
		t.Fatalf("progress item = %q, want %q", got, to)
	}
	// The session on the unmoved file stays.
	if got := env.itemIDOf(t, "playback_history_admin", "user_id = $1 AND media_file_id = $2", env.userID, stayFile); got != from {
		t.Fatalf("stayed session item = %q, want %q", got, from)
	}
}

func TestRun_HistoryEvidenceClassification(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	from := fmt.Sprintf("movie-src-%d", env.suffix)
	to := fmt.Sprintf("movie-dst-%d", env.suffix)
	movedFile, stayFile := 920001, 920002

	// Profile A: only played the moved file → unanimous, history moves.
	// Profile B: played both files → mixed, history stays and is ambiguous.
	profileA := env.profileID
	profileB := fmt.Sprintf("00000000-0000-4000-8000-%012d", (env.suffix+1)%1_000_000_000_000)

	seedSession := func(id, profile string, fileID int) {
		t.Helper()
		if _, err := env.pool.Exec(ctx, `
			INSERT INTO playback_history_admin (session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at)
			VALUES ($1, $2, $3, $4, $5, 'direct', now(), now())
		`, id, env.userID, profile, from, fileID); err != nil {
			t.Fatalf("seed session %s: %v", id, err)
		}
	}
	seedHistory := func(id, profile string) {
		t.Helper()
		if _, err := env.pool.Exec(ctx, `
			INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed)
			VALUES ($1, $2, $3, $4, now(), true)
		`, id, env.userID, profile, from); err != nil {
			t.Fatalf("seed history %s: %v", id, err)
		}
	}
	seedSession(fmt.Sprintf("sa-%d", env.suffix), profileA, movedFile)
	seedSession(fmt.Sprintf("sb1-%d", env.suffix), profileB, movedFile)
	seedSession(fmt.Sprintf("sb2-%d", env.suffix), profileB, stayFile)
	seedHistory(fmt.Sprintf("ha-%d", env.suffix), profileA)
	seedHistory(fmt.Sprintf("hb-%d", env.suffix), profileB)

	report := env.run(t, Options{
		FromContentID: from,
		ToContentID:   to,
		MovedFileIDs:  []int{movedFile},
		Mode:          HistoryModeEvidence,
	})

	if report.HistoryMoved != 1 || report.HistoryAmbiguous != 1 {
		t.Fatalf("report = %+v, want 1 moved + 1 ambiguous history row", report)
	}
	if got := env.itemIDOf(t, "user_watch_history", "user_id = $1 AND profile_id = $2", env.userID, profileA); got != to {
		t.Fatalf("profile A history = %q, want moved to %q", got, to)
	}
	if got := env.itemIDOf(t, "user_watch_history", "user_id = $1 AND profile_id = $2", env.userID, profileB); got != from {
		t.Fatalf("profile B history = %q, want kept on %q", got, from)
	}
	if len(report.AmbiguousHistory) != 1 || report.AmbiguousHistory[0].ProfileID != profileB {
		t.Fatalf("ambiguous sample = %+v, want profile B", report.AmbiguousHistory)
	}
}

func TestRun_MoveAllMovesIntent(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	from := fmt.Sprintf("movie-src-%d", env.suffix)
	to := fmt.Sprintf("movie-dst-%d", env.suffix)
	movedFile := 930001

	if _, err := env.pool.Exec(ctx, `
		INSERT INTO user_favorites (user_id, profile_id, media_item_id) VALUES ($1, $2, $3)
	`, env.userID, env.profileID, from); err != nil {
		t.Fatalf("seed favorite: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at)
		VALUES ($1, $2, $3, $4, now())
	`, fmt.Sprintf("hma-%d", env.suffix), env.userID, env.profileID, from); err != nil {
		t.Fatalf("seed history: %v", err)
	}

	report := env.run(t, Options{
		FromContentID: from,
		ToContentID:   to,
		MovedFileIDs:  []int{movedFile},
		Mode:          HistoryModeMoveAll,
	})

	if report.HistoryMoved != 1 || report.IntentMoved != 1 {
		t.Fatalf("report = %+v, want 1 history + 1 intent moved", report)
	}
	if got := env.itemIDOf(t, "user_favorites", "user_id = $1", env.userID); got != to {
		t.Fatalf("favorite item = %q, want %q", got, to)
	}
}

func TestRun_WholeItemPairsAndConflicts(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	from := fmt.Sprintf("series-src-%d", env.suffix)
	to := fmt.Sprintf("series-dst-%d", env.suffix)
	epFrom := fmt.Sprintf("ep-src-%d", env.suffix)
	epTo := fmt.Sprintf("ep-dst-%d", env.suffix)

	// Favorite on both source and destination: destination wins, source drops.
	for _, item := range []string{from, to} {
		if _, err := env.pool.Exec(ctx, `
			INSERT INTO user_favorites (user_id, profile_id, media_item_id) VALUES ($1, $2, $3)
		`, env.userID, env.profileID, item); err != nil {
			t.Fatalf("seed favorite %s: %v", item, err)
		}
	}
	// Progress on both sides of an episode pair; source is newer and must win.
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at)
		VALUES ($1, $2, $3, 500, 3600, now()),
		       ($1, $2, $4, 100, 3600, now() - interval '1 day')
	`, env.userID, env.profileID, epFrom, epTo); err != nil {
		t.Fatalf("seed progress: %v", err)
	}
	// Series-scoped subtitle preference and a dismissal denormalizing the
	// series id: both must follow the merge.
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO user_subtitle_preferences (user_id, profile_id, series_id, subtitle_language)
		VALUES ($1, $2, $3, 'en')
	`, env.userID, env.profileID, from); err != nil {
		t.Fatalf("seed subtitle preference: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO user_home_item_dismissals (user_id, profile_id, surface, media_item_id, series_id)
		VALUES ($1, $2, 'next_up', $3, $4)
	`, env.userID, env.profileID, epFrom, from); err != nil {
		t.Fatalf("seed dismissal: %v", err)
	}

	report := env.run(t, Options{
		FromContentID: from,
		ToContentID:   to,
		WholeItem:     true,
		EpisodePairs:  []IDPair{{From: epFrom, To: epTo}},
	})

	var prefSeriesID string
	if err := env.pool.QueryRow(ctx,
		`SELECT series_id FROM user_subtitle_preferences WHERE user_id = $1`, env.userID).Scan(&prefSeriesID); err != nil {
		t.Fatalf("load subtitle preference: %v", err)
	}
	if prefSeriesID != to {
		t.Fatalf("subtitle preference series_id = %q, want %q", prefSeriesID, to)
	}
	var dismissItem, dismissSeries string
	if err := env.pool.QueryRow(ctx, `
		SELECT media_item_id, series_id FROM user_home_item_dismissals WHERE user_id = $1
	`, env.userID).Scan(&dismissItem, &dismissSeries); err != nil {
		t.Fatalf("load dismissal: %v", err)
	}
	if dismissItem != epTo || dismissSeries != to {
		t.Fatalf("dismissal = (%q, %q), want (%q, %q)", dismissItem, dismissSeries, epTo, to)
	}

	var favCount int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM user_favorites WHERE user_id = $1`, env.userID).Scan(&favCount); err != nil {
		t.Fatalf("count favorites: %v", err)
	}
	if favCount != 1 {
		t.Fatalf("favorites = %d, want 1 (deduped)", favCount)
	}
	if got := env.itemIDOf(t, "user_favorites", "user_id = $1", env.userID); got != to {
		t.Fatalf("favorite item = %q, want %q", got, to)
	}

	var position float64
	if err := env.pool.QueryRow(ctx, `
		SELECT position_seconds FROM user_watch_progress WHERE user_id = $1 AND media_item_id = $2
	`, env.userID, epTo).Scan(&position); err != nil {
		t.Fatalf("load progress: %v", err)
	}
	if position != 500 {
		t.Fatalf("progress position = %v, want newer source row (500)", position)
	}
	if report.ProgressConflicts == 0 {
		t.Fatalf("report = %+v, want progress conflict recorded", report)
	}
}

func TestRun_PreviewRollsBack(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	from := fmt.Sprintf("movie-src-%d", env.suffix)
	to := fmt.Sprintf("movie-dst-%d", env.suffix)

	if _, err := env.pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, last_file_id)
		VALUES ($1, $2, $3, 100, 7200, 940001)
	`, env.userID, env.profileID, from); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	tx, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	report, err := Run(ctx, tx, Options{
		FromContentID: from,
		ToContentID:   to,
		MovedFileIDs:  []int{940001},
		Mode:          HistoryModeEvidence,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("rollback: %v", err)
	}
	if report.ProgressMoved != 1 {
		t.Fatalf("preview report = %+v, want 1 progress moved", report)
	}
	if got := env.itemIDOf(t, "user_watch_progress", "user_id = $1", env.userID); got != from {
		t.Fatalf("after rollback progress item = %q, want untouched %q", got, from)
	}
}
