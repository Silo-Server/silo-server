package subsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/migrations"
)

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(t.Context(), pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return pool
}

func seedMediaFile(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := t.Context()
	name := fmt.Sprintf("subsync-%d", time.Now().UnixNano())
	var folderID, fileID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`, name).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, stmt := range []string{`DELETE FROM media_files WHERE media_folder_id = $1`, `DELETE FROM media_folders WHERE id = $1`} {
			if _, err := pool.Exec(context.Background(), stmt, folderID); err != nil {
				t.Errorf("clean fixture: %v", err)
			}
		}
	})
	if err := pool.QueryRow(ctx, `INSERT INTO media_files (media_folder_id, file_path, file_hash, file_size)
		VALUES ($1, $2, 'aaaaaaaaaaaaaaaa', 1000) RETURNING id`, folderID, "/"+name+"/movie.mkv").Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

// A sidecar's correction lives on a row keyed by its bytes; jobs for it
// coalesce, apply under the row's revision, and vanish with the row when the
// media file is replaced.
func TestExternalTimingAndJobsPostgres(t *testing.T) {
	pool := openTestPool(t)
	ctx := t.Context()
	fileID := seedMediaFile(t, pool)
	repo := subtitles.NewPgRepository(pool, nil)
	store := NewStore(pool)
	sha := strings.Repeat("ab", 32)

	row, err := repo.EnsureExternalTiming(ctx, fileID, sha, "/media/movie.en.srt", subtitles.FormatSRT)
	if err != nil || row.Revision != 1 || !row.Timing.IsIdentity() {
		t.Fatalf("ensure: %+v %v", row, err)
	}
	// A rename records the new path without touching the revision.
	renamed, err := repo.EnsureExternalTiming(ctx, fileID, sha, "/media/movie.eng.srt", subtitles.FormatSRT)
	if err != nil || renamed.ID != row.ID || renamed.Revision != 1 || renamed.Path != "/media/movie.eng.srt" {
		t.Fatalf("rename: %+v %v", renamed, err)
	}

	job, created, err := store.CreateExternal(ctx, renamed, TriggerManual, nil)
	if err != nil || !created || job.ExternalTimingID != row.ID || job.SubtitleID != 0 || job.BaseRevision != 1 {
		t.Fatalf("create: %+v %t %v", job, created, err)
	}
	again, created, err := store.CreateExternal(ctx, renamed, TriggerManual, nil)
	if err != nil || created || again.ID != job.ID {
		t.Fatalf("coalesce: %+v %t %v", again, created, err)
	}

	found := subtitles.Timing{Scale: 1, OffsetMS: -2300}
	revision, err := store.Apply(ctx, job, found, Outcome{Status: string(StatusSynced), Result: &found})
	if err != nil || revision != 2 {
		t.Fatalf("apply: %d %v", revision, err)
	}
	applied, err := repo.ExternalTiming(ctx, fileID, sha)
	if err != nil || applied.Timing.OffsetMS != -2300 || applied.Revision != 2 {
		t.Fatalf("applied row: %+v %v", applied, err)
	}
	latest, err := store.LatestExternal(ctx, row.ID)
	if err != nil || latest.ID != job.ID || latest.Status != string(StatusSynced) {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	byRow, err := store.LatestForExternal(ctx, []int64{row.ID})
	if err != nil || byRow[row.ID] == nil || byRow[row.ID].ID != job.ID {
		t.Fatalf("latest for: %+v %v", byRow, err)
	}

	// A job captured before a manual edit loses to it.
	stale, _, err := store.CreateExternal(ctx, applied, TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetExternalTiming(ctx, fileID, sha, applied.Path, subtitles.FormatSRT, subtitles.Timing{Scale: 1, OffsetMS: 100}, 1); !errors.Is(err, subtitles.ErrExternalTimingChanged) {
		t.Fatalf("stale manual write: %v", err)
	}
	if _, err := repo.SetExternalTiming(ctx, fileID, sha, applied.Path, subtitles.FormatSRT, subtitles.Timing{Scale: 1, OffsetMS: 100}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(ctx, stale, found, Outcome{Status: string(StatusSynced), Result: &found}); !errors.Is(err, ErrSubtitleChanged) {
		t.Fatalf("stale job applied: %v", err)
	}
	if err := store.Finish(ctx, stale.ID, Outcome{Status: JobFailed}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := store.Finish(ctx, stale.ID, Outcome{Status: JobFailed}); !errors.Is(err, jobrunner.ErrJobTerminal) {
		t.Fatalf("finish a finished job: %v", err)
	}
	if err := store.Progress(ctx, stale.ID, PhaseAnalyzing, 0.5); !errors.Is(err, jobrunner.ErrJobTerminal) {
		t.Fatalf("progress of a finished job: %v", err)
	}

	// A first manual write for other bytes creates their row.
	other := strings.Repeat("cd", 32)
	created2, err := repo.SetExternalTiming(ctx, fileID, other, "/media/movie.fr.srt", subtitles.FormatSRT, subtitles.Timing{Scale: 1, OffsetMS: 400}, 0)
	if err != nil || created2.Revision != 1 || created2.Timing.OffsetMS != 400 {
		t.Fatalf("first write: %+v %v", created2, err)
	}
	// Automatic sync skips bytes that had a job; these have none yet.
	if has, err := store.HasExternalJob(ctx, row.ID); err != nil || !has {
		t.Fatalf("jobs of synced bytes: %t %v", has, err)
	}
	if has, err := store.HasExternalJob(ctx, created2.ID); err != nil || has {
		t.Fatalf("jobs of bytes never synced: %t %v", has, err)
	}
	if _, err := repo.SetExternalTiming(ctx, fileID, other, "/media/movie.fr.srt", subtitles.FormatSRT, subtitles.Timing{Scale: 1}, 0); !errors.Is(err, subtitles.ErrExternalTimingChanged) {
		t.Fatalf("racing first write: %v", err)
	}

	// A verdict that leaves the timing as it is records only while the
	// subject still has the revision the job read.
	verdict, _, err := store.CreateExternal(ctx, created2, TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetExternalTiming(ctx, fileID, other, "/media/movie.fr.srt", subtitles.FormatSRT, subtitles.Timing{Scale: 1, OffsetMS: 500}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishUnchanged(ctx, verdict, Outcome{Status: string(StatusAlreadySynced)}); !errors.Is(err, ErrSubtitleChanged) {
		t.Fatalf("verdict on a retimed sidecar: %v", err)
	}
	if active, err := store.LatestExternal(ctx, created2.ID); err != nil || active.ID != verdict.ID || active.Status != JobPending {
		t.Fatalf("job after a refused verdict: %+v %v", active, err)
	}
	if err := store.Finish(ctx, verdict.ID, Outcome{Status: JobFailed}); err != nil {
		t.Fatal(err)
	}
	retimed, err := repo.ExternalTiming(ctx, fileID, other)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := store.CreateExternal(ctx, retimed, TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishUnchanged(ctx, current, Outcome{Status: string(StatusAlreadySynced)}); err != nil {
		t.Fatalf("verdict: %v", err)
	}
	if err := store.FinishUnchanged(ctx, current, Outcome{Status: string(StatusAlreadySynced)}); !errors.Is(err, jobrunner.ErrJobTerminal) {
		t.Fatalf("second verdict: %v", err)
	}

	// Replacing the media file removes the corrections and their jobs.
	if _, err := pool.Exec(ctx, `UPDATE media_files SET file_hash = 'bbbbbbbbbbbbbbbb' WHERE id = $1`, fileID); err != nil {
		t.Fatal(err)
	}
	timings, err := repo.ExternalTimings(ctx, fileID)
	if err != nil || len(timings) != 0 {
		t.Fatalf("corrections after replace: %+v %v", timings, err)
	}
	if latest, err := store.LatestExternal(ctx, row.ID); err != nil || latest != nil {
		t.Fatalf("jobs after replace: %+v %v", latest, err)
	}
}
