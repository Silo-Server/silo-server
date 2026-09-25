package intromarkers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/migrations"
)

// TestSilenceBackfillSkipsUnchangedAttemptsPostgres runs the backfill twice
// against Postgres: a file whose refinement found nothing better, and one whose
// refinement failed, must both drop out of the next run until their inputs
// change or the failure backoff elapses.
func TestSilenceBackfillRevisitsLegacySilenceMarkersPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	fileIDs := seedSilenceBackfillFixture(t, pool)
	legacy, current := fileIDs[0], fileIDs[1]
	if _, err := pool.Exec(ctx, `UPDATE media_files SET intro_markers_algorithm = $2 WHERE id = $1`, legacy, legacyChapterSilenceAlgorithm); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET intro_markers_algorithm = $2 WHERE id = $1`, current, ChapterSilenceAlgorithm); err != nil {
		t.Fatal(err)
	}

	all, err := NewRepository(pool).ListChapterSilenceBackfillCandidates(ctx, 1_000_000, DefaultConfig("ffmpeg"), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, candidate := range all {
		if slices.Contains(fileIDs, candidate.FileID) {
			got = append(got, candidate.FileID)
		}
	}
	if want := []int{legacy, fileIDs[2]}; !slices.Equal(got, want) {
		t.Fatalf("backfill = %v, want the legacy silence marker and the chapter marker %v", got, want)
	}
}

func TestSilenceBackfillSkipsUnchangedAttemptsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	fileIDs := seedSilenceBackfillFixture(t, pool)
	noImprovement, failing, untouched := fileIDs[0], fileIDs[1], fileIDs[2]

	repo := NewRepository(pool)
	cfg := DefaultConfig("ffmpeg")
	backfillOn := func(node string, cfg Config) []Candidate {
		t.Helper()
		all, err := repo.ListChapterSilenceBackfillCandidates(ctx, 1_000_000, cfg, node)
		if err != nil {
			t.Fatal(err)
		}
		var fixture []Candidate
		for _, candidate := range all {
			if slices.Contains(fileIDs, candidate.FileID) {
				fixture = append(fixture, candidate)
			}
		}
		return fixture
	}
	backfill := func(cfg Config) []Candidate {
		t.Helper()
		return backfillOn("node-a", cfg)
	}
	ids := func(candidates []Candidate) []int {
		out := make([]int, 0, len(candidates))
		for _, candidate := range candidates {
			out = append(out, candidate.FileID)
		}
		return out
	}
	only := func(candidates []Candidate, fileIDs ...int) []Candidate {
		var out []Candidate
		for _, candidate := range candidates {
			if slices.Contains(fileIDs, candidate.FileID) {
				out = append(out, candidate)
			}
		}
		return out
	}
	refiner := &fakeBoundaryRefiner{errors: map[int]error{failing: errors.New("ffmpeg exited 1")}}
	analyzer := &Analyzer{repo: repo, refiner: refiner, config: cfg, logger: slog.New(slog.DiscardHandler), node: "node-a"}
	run := func(candidates []Candidate) RunSummary {
		t.Helper()
		_, summary := analyzer.processChapterCandidates(ctx, candidates, chapterProcessingOptions{forceExistingScanner: true})
		if len(summary.Errors) != 0 {
			t.Fatalf("backfill errors: %v", summary.Errors)
		}
		return summary
	}

	first := backfill(cfg)
	if got := ids(first); !slices.Equal(got, fileIDs) {
		t.Fatalf("first backfill = %v, want %v", got, fileIDs)
	}
	summary := run(only(first, noImprovement, failing))
	if summary.SilenceRefinementsAttempted != 2 || summary.SilenceRefinementErrors != 1 || summary.SilenceRefinementsApplied != 0 {
		t.Fatalf("unexpected first run summary: %+v", summary)
	}

	if got := ids(backfill(cfg)); !slices.Equal(got, []int{untouched}) {
		t.Fatalf("second backfill = %v, want only the never-attempted file %d", got, untouched)
	}

	changedSettings := cfg
	changedSettings.SilenceWindowAfterSeconds = 45
	if got := ids(backfill(changedSettings)); !slices.Equal(got, []int{untouched, noImprovement, failing}) {
		t.Fatalf("backfill after a settings change = %v, want never-attempted first then %d, %d", got, noImprovement, failing)
	}

	if _, err := pool.Exec(ctx, `UPDATE intro_silence_refinement_attempts SET retry_after = NOW() - interval '1 minute' WHERE media_file_id = $1`, failing); err != nil {
		t.Fatal(err)
	}
	retry := backfill(cfg)
	if got := ids(retry); !slices.Equal(got, []int{untouched, failing}) {
		t.Fatalf("backfill after the retry time = %v, want %d then %d", got, untouched, failing)
	}
	run(only(retry, failing))
	attempt, err := repo.LoadSilenceRefinementAttempt(ctx, failing)
	if err != nil {
		t.Fatal(err)
	}
	if attempt == nil || attempt.Status != silenceAttemptFailed || attempt.FailureCount != 2 || attempt.LastError != "ffmpeg exited 1" {
		t.Fatalf("unexpected failed attempt after retry: %+v", attempt)
	}
	if attempt.RetryAfter == nil || attempt.RetryAfter.Sub(attempt.AttemptedAt) != 24*time.Hour {
		t.Fatalf("second failure should back off 24h, got retry_after=%v attempted_at=%v", attempt.RetryAfter, attempt.AttemptedAt)
	}
	if got := ids(backfill(cfg)); !slices.Equal(got, []int{untouched}) {
		t.Fatalf("backfill after a second failure = %v, want only %d", got, untouched)
	}
	// The failure may be local to node-a, so another server still tries the
	// file; the no-improvement result applies everywhere.
	if got := ids(backfillOn("node-b", cfg)); !slices.Equal(got, []int{untouched, failing}) {
		t.Fatalf("backfill on another server = %v, want %d then %d", got, untouched, failing)
	}

	// A re-probe can rewrite chapters without touching the file identity or the
	// stored marker; the refinement input changed, so the file is due again.
	if _, err := pool.Exec(ctx, `UPDATE media_files SET chapters = $2::jsonb WHERE id = $1`, failing,
		`[{"index":0,"title":"Opening","start_seconds":60,"end_seconds":120},{"index":1,"title":"Part 1","start_seconds":120,"end_seconds":1300}]`); err != nil {
		t.Fatal(err)
	}
	if got := ids(backfill(cfg)); !slices.Equal(got, []int{untouched, failing}) {
		t.Fatalf("backfill after the chapters changed = %v, want %d then %d", got, untouched, failing)
	}

	if _, err := pool.Exec(ctx, `UPDATE media_files SET intro_end = 125 WHERE id = $1`, noImprovement); err != nil {
		t.Fatal(err)
	}
	if got := ids(backfill(cfg)); !slices.Equal(got, []int{untouched, noImprovement, failing}) {
		t.Fatalf("backfill after the marker moved = %v, want %d, %d, %d", got, untouched, noImprovement, failing)
	}

	if untouched <= math.MaxInt32 {
		t.Fatalf("fixture file %d should be past the int32 range", untouched)
	}
	run(only(backfill(cfg), untouched))
	if got := ids(backfill(cfg)); slices.Contains(got, untouched) {
		t.Fatalf("backfill after recording file %d = %v, want it skipped", untouched, got)
	}
}

// seedSilenceBackfillFixture creates an intro-enabled series library with three
// episode files carrying a scanner chapter:v1 intro marker.
func seedSilenceBackfillFixture(t *testing.T, pool *pgxpool.Pool) []int {
	t.Helper()
	ctx := t.Context()
	prefix := fmt.Sprintf("silence-backfill-%d-", time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled, intro_detection_enabled) VALUES ('series', $1, true, true) RETURNING id`, prefix).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	seriesID := prefix + "series"
	seasonID := prefix + "season"
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, stmt := range []string{
			`DELETE FROM media_files WHERE media_folder_id = $1`,
			`DELETE FROM media_folders WHERE id = $1`,
		} {
			if _, err := pool.Exec(cleanup, stmt, folderID); err != nil {
				t.Errorf("clean silence backfill fixture: %v", err)
			}
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID); err != nil {
			t.Errorf("clean silence backfill fixture: %v", err)
		}
	})
	exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'Silence backfill')`, seriesID)
	exec(`INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1, $2, 1)`, seasonID, seriesID)

	chapters := `[{"index":0,"title":"Opening","start_seconds":60,"end_seconds":120},{"index":1,"title":"Part 1","start_seconds":120,"end_seconds":1400}]`
	fileIDs := make([]int, 0, 3)
	for episode := 1; episode <= 3; episode++ {
		episodeID := fmt.Sprintf("%sepisode-%d", prefix, episode)
		exec(`INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number) VALUES ($1, $2, $3, 1, $4)`, episodeID, seriesID, seasonID, episode)
		// media_files.id is bigint; the last file sits past the int32 range so the
		// attempts table has to hold it.
		id := any(nil)
		if episode == 3 {
			id = int64(math.MaxInt32) + time.Now().UnixNano()%1_000_000_000 + 1
		}
		var fileID int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (
			    id, media_folder_id, file_path, episode_id, season_number, episode_number,
			    file_hash, file_size, duration, chapters,
			    intro_start, intro_end, intro_markers_source, intro_markers_confidence,
			    intro_markers_algorithm, intro_markers_detected_at
			) VALUES (COALESCE($7::bigint, nextval(pg_get_serial_sequence('media_files', 'id'))),
			        $1, $2, $3, 1, $4, $5, 1000000, 1500, $6::jsonb,
			        60, 120, 'scanner', 0.95, 'chapter:v1', '2026-08-08T03:30:00Z')
			RETURNING id`,
			folderID, fmt.Sprintf("/%s/e%d.mkv", prefix, episode), episodeID, episode,
			fmt.Sprintf("%shash-%d", prefix, episode), chapters, id,
		).Scan(&fileID); err != nil {
			t.Fatal(err)
		}
		fileIDs = append(fileIDs, fileID)
	}
	return fileIDs
}

func TestSeasonStateRoundTripsAnalyzedAtPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	fileIDs := seedSilenceBackfillFixture(t, pool)
	var state SeasonState
	if err := pool.QueryRow(ctx, `
		SELECT e.season_id, mf.media_folder_id
		FROM media_files mf JOIN episodes e ON e.content_id = mf.episode_id
		WHERE mf.id = $1`, fileIDs[0]).Scan(&state.SeasonID, &state.MediaFolderID); err != nil {
		t.Fatal(err)
	}
	state.AnalysisGroupKey = "default|default|und"
	state.InputSignature = "signature"
	state.Status = seasonStatusPartial
	state.LastError = "1 fingerprint extraction(s) failed"

	repo := NewRepository(pool)
	cfg := DefaultConfig("ffmpeg")
	before := time.Now().Add(-time.Minute)
	if err := repo.UpsertSeasonState(ctx, state, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.LoadSeasonState(ctx, state, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.Status != seasonStatusPartial || loaded.AnalyzedAt.Before(before) {
		t.Fatalf("loaded state = %+v, want partial with a fresh analyzed_at", loaded)
	}
	if !loaded.settled(time.Now()) || loaded.settled(time.Now().Add(partialSeasonRetryInterval)) {
		t.Fatalf("partial state should hold for the retry interval and lapse after it: %+v", loaded)
	}
}
