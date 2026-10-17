package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A file arr confirmed deleting skips the removal grace once a scan has marked
// it missing, while a file that merely went missing still waits it out, and a
// confirmed file a scan finds on disk loses its confirmation.
func TestConfirmFilesRemovedSkipsGracePostgres(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := t.Context()
	folderID := seedDeadRootTestFolder(t, pool, "series", "Removal confirmed sweep")
	repo := NewFileRepository(pool)
	base := fmt.Sprintf("/removal-confirmed-%d/Show/Season 01", time.Now().UnixNano())

	upsert := func(name string) *models.MediaFile {
		t.Helper()
		row, err := repo.Upsert(ctx, models.MediaFile{
			MediaFolderID: folderID, FilePath: base + "/" + name, FileSize: 1024,
			SubtitleTracks: []models.SubtitleTrack{}, ExternalSubtitles: []models.ExternalSubtitle{},
		})
		if err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
		return row
	}
	exists := func(id int) bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM media_files WHERE id = $1)`, id).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	replaced := upsert("S01E01 old.mkv")
	vanished := upsert("S01E02.mkv")
	returned := upsert("S01E03.mkv")

	confirmed, err := repo.ConfirmFilesRemoved(ctx, []string{replaced.FilePath, returned.FilePath, base + "/not-cataloged.mkv"})
	if err != nil {
		t.Fatalf("ConfirmFilesRemoved: %v", err)
	}
	if confirmed != 2 {
		t.Fatalf("confirmed = %d, want 2", confirmed)
	}
	sweep := func(protected ...string) int {
		t.Helper()
		deleted, err := repo.DeleteMissingByFolder(ctx, folderID, 24*time.Hour, protected)
		if err != nil {
			t.Fatalf("DeleteMissingByFolder: %v", err)
		}
		return deleted
	}

	// A confirmation alone deletes nothing: only a scan that finds the file
	// gone marks it missing and reconciles its memberships first.
	if deleted := sweep(); deleted != 0 {
		t.Fatalf("sweep before any scan deleted %d rows", deleted)
	}

	// The scan finds the replaced file gone and the returned one on disk.
	now := time.Now().UTC()
	for _, id := range []int{replaced.ID, vanished.ID} {
		if err := repo.MarkMissing(ctx, id, now); err != nil {
			t.Fatal(err)
		}
	}
	// A confirmation newer than the scan's observation survives the clear.
	if err := repo.ClearRemovalConfirmed(ctx, returned.ID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var stillConfirmed bool
	if err := pool.QueryRow(ctx, `SELECT removal_confirmed_at IS NOT NULL FROM media_files WHERE id = $1`, returned.ID).Scan(&stillConfirmed); err != nil {
		t.Fatal(err)
	}
	if !stillConfirmed {
		t.Fatal("clear dropped a confirmation written after the scan looked")
	}
	if err := repo.ClearRemovalConfirmed(ctx, returned.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if deleted := sweep(); deleted != 1 || exists(replaced.ID) {
		t.Fatalf("deleted = %d, replaced row exists = %v; want only the confirmed row deleted", deleted, exists(replaced.ID))
	}
	if !exists(vanished.ID) {
		t.Fatal("unconfirmed missing row was deleted inside the grace period")
	}

	// A later outage marks the returned file missing; its cleared
	// confirmation must not let it skip the grace.
	if err := repo.MarkMissing(ctx, returned.ID, now); err != nil {
		t.Fatal(err)
	}
	if deleted := sweep(); deleted != 0 {
		t.Fatalf("sweep after the outage deleted %d rows, want 0", deleted)
	}

	// Seeing a confirmed file again through the upsert clears it as well.
	reupserted := upsert("S01E06.mkv")
	if _, err := repo.ConfirmFilesRemoved(ctx, []string{reupserted.FilePath}); err != nil {
		t.Fatal(err)
	}
	upsert("S01E06.mkv")
	if err := repo.MarkMissing(ctx, reupserted.ID, now); err != nil {
		t.Fatal(err)
	}
	if deleted := sweep(); deleted != 0 {
		t.Fatalf("sweep after a re-upsert deleted %d rows, want 0", deleted)
	}

	// A confirmed row holding a removed episode's first-seen date waits for
	// the replacement file to claim it; deleting it would make the episode
	// look newly added.
	holding := upsert("S01E05.mkv")
	if _, err := repo.ConfirmFilesRemoved(ctx, []string{holding.FilePath}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkMissing(ctx, holding.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET held_episode_first_seen_at = NOW() - interval '30 days' WHERE id = $1`, holding.ID); err != nil {
		t.Fatal(err)
	}
	if deleted := sweep(); deleted != 0 {
		t.Fatalf("holding sweep deleted %d rows, want 0", deleted)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET held_episode_first_seen_at = NULL WHERE id = $1`, holding.ID); err != nil {
		t.Fatal(err)
	}

	// Protected roots still win over a confirmation.
	if deleted := sweep(base); deleted != 0 || !exists(holding.ID) {
		t.Fatalf("protected sweep deleted %d rows, want 0", deleted)
	}
	if deleted := sweep(); deleted != 1 || exists(holding.ID) {
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
	pool := newDeadRootTestPool(t)
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
	pool := newDeadRootTestPool(t)
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
