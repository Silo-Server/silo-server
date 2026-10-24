package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// sweepFixture is a series folder of cataloged files with no files on disk,
// for exercising removal confirmations against the trash sweep directly.
type sweepFixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	repo     *FileRepository
	folderID int
	base     string
}

func newSweepFixture(t *testing.T, name string) sweepFixture {
	t.Helper()
	pool := newScannerTestPool(t)
	return sweepFixture{
		t:        t,
		pool:     pool,
		repo:     NewFileRepository(pool),
		folderID: seedDeadRootTestFolder(t, pool, "series", name),
		base:     fmt.Sprintf("/removal-confirmed-%d/Show/Season 01", time.Now().UnixNano()),
	}
}

func (fx sweepFixture) upsert(name string) *models.MediaFile {
	fx.t.Helper()
	row, err := fx.repo.Upsert(fx.t.Context(), models.MediaFile{
		MediaFolderID: fx.folderID, FilePath: fx.base + "/" + name, FileSize: 1024,
		SubtitleTracks: []models.SubtitleTrack{}, ExternalSubtitles: []models.ExternalSubtitle{},
	})
	if err != nil {
		fx.t.Fatalf("upsert %s: %v", name, err)
	}
	return row
}

func (fx sweepFixture) exists(id int) bool {
	fx.t.Helper()
	var ok bool
	if err := fx.pool.QueryRow(fx.t.Context(), `SELECT EXISTS(SELECT 1 FROM media_files WHERE id = $1)`, id).Scan(&ok); err != nil {
		fx.t.Fatal(err)
	}
	return ok
}

func (fx sweepFixture) confirm(paths ...string) int {
	fx.t.Helper()
	confirmed, err := fx.repo.ConfirmFilesRemoved(fx.t.Context(), paths)
	if err != nil {
		fx.t.Fatalf("ConfirmFilesRemoved: %v", err)
	}
	return confirmed
}

func (fx sweepFixture) markMissing(ids ...int) {
	fx.t.Helper()
	for _, id := range ids {
		var path string
		if err := fx.pool.QueryRow(fx.t.Context(), `SELECT file_path FROM media_files WHERE id = $1`, id).Scan(&path); err != nil {
			fx.t.Fatal(err)
		}
		if err := fx.repo.MarkMissing(fx.t.Context(), id, path, time.Now().UTC()); err != nil {
			fx.t.Fatal(err)
		}
	}
}

func (fx sweepFixture) sweep(protected ...string) int {
	fx.t.Helper()
	deleted, err := fx.repo.DeleteMissingByFolder(fx.t.Context(), fx.folderID, 24*time.Hour, protected)
	if err != nil {
		fx.t.Fatalf("DeleteMissingByFolder: %v", err)
	}
	return deleted
}

// A file arr confirmed deleting skips the removal grace once a scan has marked
// it missing, while a file that merely went missing still waits it out, and a
// confirmed file a scan finds on disk loses its confirmation.
func TestConfirmFilesRemovedSkipsGracePostgres(t *testing.T) {
	fx := newSweepFixture(t, "Removal confirmed sweep")
	ctx := t.Context()

	replaced := fx.upsert("S01E01 old.mkv")
	vanished := fx.upsert("S01E02.mkv")
	returned := fx.upsert("S01E03.mkv")

	if confirmed := fx.confirm(replaced.FilePath, returned.FilePath, fx.base+"/not-cataloged.mkv"); confirmed != 2 {
		t.Fatalf("confirmed = %d, want 2", confirmed)
	}

	// A confirmation alone deletes nothing: only a scan that finds the file
	// gone marks it missing and reconciles its memberships first.
	if deleted := fx.sweep(); deleted != 0 {
		t.Fatalf("sweep before any scan deleted %d rows", deleted)
	}

	// The scan finds the replaced file gone and the returned one on disk.
	now := time.Now().UTC()
	fx.markMissing(replaced.ID, vanished.ID)
	// A confirmation newer than the scan's observation survives the clear.
	if err := fx.repo.ClearRemovalConfirmed(ctx, returned.ID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var stillConfirmed bool
	if err := fx.pool.QueryRow(ctx, `SELECT removal_confirmed_at IS NOT NULL FROM media_files WHERE id = $1`, returned.ID).Scan(&stillConfirmed); err != nil {
		t.Fatal(err)
	}
	if !stillConfirmed {
		t.Fatal("clear dropped a confirmation written after the scan looked")
	}
	if err := fx.repo.ClearRemovalConfirmed(ctx, returned.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if deleted := fx.sweep(); deleted != 1 || fx.exists(replaced.ID) {
		t.Fatalf("deleted = %d, replaced row exists = %v; want only the confirmed row deleted", deleted, fx.exists(replaced.ID))
	}
	if !fx.exists(vanished.ID) {
		t.Fatal("unconfirmed missing row was deleted inside the grace period")
	}

	// A later outage marks the returned file missing; its cleared
	// confirmation must not let it skip the grace.
	fx.markMissing(returned.ID)
	if deleted := fx.sweep(); deleted != 0 {
		t.Fatalf("sweep after the outage deleted %d rows, want 0", deleted)
	}
}

// A confirmation is lost when the file is upserted again, and a confirmed row
// still waits while it holds first-seen data or sits under a protected root.
func TestConfirmedRemovalExceptionsPostgres(t *testing.T) {
	fx := newSweepFixture(t, "Removal confirmed exceptions")
	ctx := t.Context()

	// Seeing a confirmed file again through the upsert clears it as well.
	reupserted := fx.upsert("S01E06.mkv")
	fx.confirm(reupserted.FilePath)
	fx.upsert("S01E06.mkv")
	fx.markMissing(reupserted.ID)
	if deleted := fx.sweep(); deleted != 0 {
		t.Fatalf("sweep after a re-upsert deleted %d rows, want 0", deleted)
	}

	// A confirmed row holding a removed episode's first-seen date waits for
	// the replacement file to claim it; deleting it would make the episode
	// look newly added.
	holding := fx.upsert("S01E05.mkv")
	fx.confirm(holding.FilePath)
	fx.markMissing(holding.ID)
	if _, err := fx.pool.Exec(ctx, `UPDATE media_files SET held_episode_first_seen_at = NOW() - interval '30 days' WHERE id = $1`, holding.ID); err != nil {
		t.Fatal(err)
	}
	if deleted := fx.sweep(); deleted != 0 {
		t.Fatalf("holding sweep deleted %d rows, want 0", deleted)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE media_files SET held_episode_first_seen_at = NULL WHERE id = $1`, holding.ID); err != nil {
		t.Fatal(err)
	}
	// Protected roots still win over a confirmation.
	if deleted := fx.sweep(fx.base); deleted != 0 || !fx.exists(holding.ID) {
		t.Fatalf("protected sweep deleted %d rows, want 0", deleted)
	}
	if deleted := fx.sweep(); deleted != 1 || fx.exists(holding.ID) {
		t.Fatalf("sweep after the hold was claimed deleted %d rows, want the row deleted", deleted)
	}
}

// An arr upgrade through the webhook: the old release is confirmed removed,
// then the scan reconciles it. The episode keeps its added date and the old
// row leaves at the first sweep after the replacement is in, without waiting
// out the grace.
func TestConfirmedReplacedEpisodeKeepsFirstSeenAndSkipsGrace(t *testing.T) {
	for _, order := range []string{"delete then import", "import then delete"} {
		t.Run(order, func(t *testing.T) {
			ctx := t.Context()
			fx := seedPresentStateFixture(ctx, t, "confirmed-replaced-episode")
			old := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
			fx.exec(ctx, t, `UPDATE media_files SET created_at = $1 WHERE file_path = $2`, old, fx.targetPath)
			fx.exec(ctx, t, `
				INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, still_path)
				VALUES ($1, $2, 1, 2, 'Episode 2', '')
			`, fx.episodeID+"-2", fx.seriesID)
			fx.exec(ctx, t, `
				INSERT INTO media_files (content_id, episode_id, media_folder_id, file_path, file_size, season_number, episode_number, created_at)
				VALUES ($1, $2, $3, $4, 1024, 1, 2, $5)
			`, fx.seriesID, fx.episodeID+"-2", fx.folderID, fx.targetPath+".e2.mkv", old.Add(-time.Hour))

			repo := NewFileRepository(fx.pool)
			s := NewScanner(repo, "", nil, 1, true, replacedFileGrace)
			if err := s.syncPresentLibraryState(ctx, fx.folderID); err != nil {
				t.Fatalf("seed memberships: %v", err)
			}
			if _, err := repo.ConfirmFilesRemoved(ctx, []string{fx.targetPath}); err != nil {
				t.Fatalf("ConfirmFilesRemoved: %v", err)
			}

			folder := &models.MediaFolder{ID: fx.folderID, Type: "series"}
			newPath := fx.targetPath + ".upgraded.mkv"
			importNew := func() {
				fx.exec(ctx, t, `
					INSERT INTO media_files (content_id, episode_id, media_folder_id, file_path, file_size, season_number, episode_number, created_at)
					VALUES ($1, $2, $3, $4, 2048, 1, 1, NOW())
				`, fx.seriesID, fx.episodeID, fx.folderID, newPath)
				if err := s.syncPresentFileState(ctx, fx.folderID, newPath); err != nil {
					t.Fatalf("import replacement: %v", err)
				}
			}
			deleteOld := func() {
				if _, err := s.reconcileVanishedFileIfNeeded(ctx, folder, fx.targetPath); err != nil {
					t.Fatalf("reconcile deleted release: %v", err)
				}
			}
			oldRows := func() int {
				return fx.count(ctx, t, `SELECT COUNT(*) FROM media_files WHERE file_path = $1`, fx.targetPath)
			}
			if order == "delete then import" {
				deleteOld()
				if oldRows() != 1 {
					t.Fatal("old row holding the added date was deleted before the replacement claimed it")
				}
				importNew()
				if _, _, _, err := s.sweepMissingAndReconcile(ctx, folder, false); err != nil {
					t.Fatalf("sweep: %v", err)
				}
			} else {
				importNew()
				deleteOld()
			}

			if oldRows() != 0 {
				t.Fatal("confirmed old row still waits out the grace")
			}
			assertTime(t, "episode first_seen_at", fx.membershipFirstSeen(ctx, t, "episode_libraries", "episode_id", fx.episodeID), old)
		})
	}
}

// Through a real library scan: a confirmed file that is gone is deleted by
// the scan that finds it gone, and a confirmed file the scan still finds
// loses its confirmation, so a later disappearance waits out the grace.
func TestScanFolderHonorsAndClearsRemovalConfirmation(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := t.Context()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Removal confirmed scan")
	root := t.TempDir()
	movie := func(name string) string {
		t.Helper()
		path := filepath.Join(root, name, name+".mkv")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fake movie payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	kept := movie("Alpha (2020)")
	upgraded := movie("Beta (2021)")
	movie("Gamma (2022)")

	repo := NewFileRepository(pool)
	s := NewScanner(repo, "", nil, 2, true, 24*time.Hour)
	folder := &models.MediaFolder{ID: folderID, Paths: []string{root}, Type: "movies", Name: "Removal confirmed scan", Enabled: true}
	scan := func() {
		t.Helper()
		if _, err := s.ScanFolder(ctx, folder); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}
	row := func(path string) (found bool, missing bool, confirmed bool) {
		t.Helper()
		var missingSince, confirmedAt *time.Time
		err := pool.QueryRow(ctx,
			`SELECT missing_since, removal_confirmed_at FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
			folderID, path).Scan(&missingSince, &confirmedAt)
		if err != nil {
			if strings.Contains(err.Error(), "no rows") {
				return false, false, false
			}
			t.Fatal(err)
		}
		return true, missingSince != nil, confirmedAt != nil
	}

	scan()
	if _, err := repo.ConfirmFilesRemoved(ctx, []string{kept, upgraded}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(upgraded); err != nil {
		t.Fatal(err)
	}
	scan()
	if found, _, _ := row(upgraded); found {
		t.Fatal("confirmed file the scan found gone is still cataloged")
	}
	if found, missing, confirmed := row(kept); !found || missing || confirmed {
		t.Fatalf("file still on disk: found=%v missing=%v confirmed=%v, want present without confirmation", found, missing, confirmed)
	}

	if err := os.Remove(kept); err != nil {
		t.Fatal(err)
	}
	scan()
	if found, missing, _ := row(kept); !found || !missing {
		t.Fatalf("unconfirmed vanished file: found=%v missing=%v, want kept as missing for the grace", found, missing)
	}
}

// A single-file scan that finds a confirmed file unchanged on disk clears the
// confirmation, even though the unchanged file takes no upsert.
func TestScanFileClearsRemovalConfirmation(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := t.Context()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Removal confirmed single file")
	root := t.TempDir()
	path := filepath.Join(root, "Alpha (2020)", "Alpha (2020).mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fake movie payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Beta (2021).mkv"), []byte("fake movie payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := NewFileRepository(pool)
	s := NewScanner(repo, "", nil, 1, true, 24*time.Hour)
	folder := &models.MediaFolder{ID: folderID, Paths: []string{root}, Type: "movies", Name: "Removal confirmed single file", Enabled: true}
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := repo.ConfirmFilesRemoved(ctx, []string{path}); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanFile(ctx, path, folder); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	var confirmed bool
	if err := pool.QueryRow(ctx,
		`SELECT removal_confirmed_at IS NOT NULL FROM media_files WHERE file_path = $1`, path).Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed {
		t.Fatal("single-file scan of an unchanged file left its removal confirmation")
	}
}
