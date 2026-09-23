package intromarkers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	backfill := func(cfg Config) []Candidate {
		t.Helper()
		all, err := repo.ListChapterSilenceBackfillCandidates(ctx, 1_000_000, cfg)
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
	analyzer := &Analyzer{repo: repo, refiner: refiner, config: cfg, logger: slog.New(slog.DiscardHandler)}
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

	if _, err := pool.Exec(ctx, `UPDATE media_files SET intro_end = 125 WHERE id = $1`, noImprovement); err != nil {
		t.Fatal(err)
	}
	if got := ids(backfill(cfg)); !slices.Equal(got, []int{untouched, noImprovement}) {
		t.Fatalf("backfill after the marker moved = %v, want %d then %d", got, untouched, noImprovement)
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
		var fileID int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (
			    media_folder_id, file_path, episode_id, season_number, episode_number,
			    file_hash, file_size, duration, chapters,
			    intro_start, intro_end, intro_markers_source, intro_markers_confidence,
			    intro_markers_algorithm, intro_markers_detected_at
			) VALUES ($1, $2, $3, 1, $4, $5, 1000000, 1500, $6::jsonb,
			          60, 120, 'scanner', 0.95, 'chapter:v1', '2026-08-08T03:30:00Z')
			RETURNING id`,
			folderID, fmt.Sprintf("/%s/e%d.mkv", prefix, episode), episodeID, episode,
			fmt.Sprintf("%shash-%d", prefix, episode), chapters,
		).Scan(&fileID); err != nil {
			t.Fatal(err)
		}
		fileIDs = append(fileIDs, fileID)
	}
	return fileIDs
}
