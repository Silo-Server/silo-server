package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestStaleReconciliationPreservesConcurrentUpsert(t *testing.T) {
	ctx := t.Context()
	pool := newDeadRootTestPool(t)
	repo := NewFileRepository(pool)
	folderID := seedDeadRootTestFolder(t, pool, "movies", "reconcile-fence")

	root := t.TempDir()
	filePath := filepath.Join(root, "subtree", "movie.mkv")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("create media directory: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("old"), 0o644); err != nil {
		t.Fatalf("create initial media file: %v", err)
	}

	initial, err := repo.Upsert(ctx, models.MediaFile{
		MediaFolderID: folderID,
		FilePath:      filePath,
		FileSize:      3,
	})
	if err != nil {
		t.Fatalf("seed media file: %v", err)
	}

	// Full scan A snapshots the row, then observes the path as absent.
	snapshot, err := repo.GetScanStateByFolder(ctx, folderID)
	if err != nil {
		t.Fatalf("snapshot scan state: %v", err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("snapshot rows = %d, want 1", len(snapshot))
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatalf("remove media file for stale walk: %v", err)
	}

	// Scoped scan B sees the path return and refreshes the same row.
	if err := os.WriteFile(filePath, []byte("fresh"), 0o644); err != nil {
		t.Fatalf("restore media file: %v", err)
	}
	refreshed, err := repo.Upsert(ctx, models.MediaFile{
		MediaFolderID: folderID,
		FilePath:      filePath,
		FileSize:      5,
	})
	if err != nil {
		t.Fatalf("refresh media file: %v", err)
	}
	if refreshed.ID != initial.ID {
		t.Fatalf("refreshed row id = %d, want stable id %d", refreshed.ID, initial.ID)
	}

	// Full scan A reconciles from its stale snapshot after B committed.
	result := &ScanResult{}
	scanner := &Scanner{fileRepo: repo}
	scanner.markMissingExcludingProtected(ctx, folderID, snapshot, map[string]bool{}, nil, result)
	if result.Missing != 0 {
		t.Fatalf("stale reconciliation missing count = %d, want 0", result.Missing)
	}

	got, err := repo.GetByPath(ctx, filePath)
	if err != nil {
		t.Fatalf("reload media file: %v", err)
	}
	if got.MissingSince != nil {
		t.Fatalf("refreshed media file was marked missing at %v", got.MissingSince)
	}
	deleted, err := repo.DeleteMissingByFolder(ctx, folderID, 0, nil)
	if err != nil {
		t.Fatalf("sweep missing files: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted files = %d, want refreshed row retained", deleted)
	}
}

func TestReconciliationMarksAndSweepsGenuinelyMissingFile(t *testing.T) {
	ctx := t.Context()
	pool := newDeadRootTestPool(t)
	repo := NewFileRepository(pool)
	folderID := seedDeadRootTestFolder(t, pool, "movies", "reconcile-normal-missing")
	filePath := filepath.Join(t.TempDir(), "deleted.mkv")

	if _, err := repo.Upsert(ctx, models.MediaFile{
		MediaFolderID: folderID,
		FilePath:      filePath,
		FileSize:      7,
	}); err != nil {
		t.Fatalf("seed media file: %v", err)
	}
	snapshot, err := repo.GetScanStateByFolder(ctx, folderID)
	if err != nil {
		t.Fatalf("snapshot scan state: %v", err)
	}

	result := &ScanResult{}
	scanner := &Scanner{fileRepo: repo}
	scanner.markMissingExcludingProtected(ctx, folderID, snapshot, map[string]bool{}, nil, result)
	if result.Missing != 1 {
		t.Fatalf("missing count = %d, want 1", result.Missing)
	}

	deleted, err := repo.DeleteMissingByFolder(ctx, folderID, time.Hour, nil)
	if err != nil {
		t.Fatalf("sweep recent missing files: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("recently missing files deleted = %d, want 0 during grace", deleted)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE media_files SET missing_since = $1 WHERE media_folder_id = $2 AND file_path = $3`,
		time.Now().UTC().Add(-2*time.Hour), folderID, filePath,
	); err != nil {
		t.Fatalf("age missing file beyond grace: %v", err)
	}

	deleted, err = repo.DeleteMissingByFolder(ctx, folderID, time.Hour, nil)
	if err != nil {
		t.Fatalf("sweep expired missing files: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted files = %d, want 1", deleted)
	}
	if _, err := repo.GetByPath(ctx, filePath); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("reload deleted file error = %v, want ErrFileNotFound", err)
	}
}

func TestCompletedPresenceObservationFencesStaleConfirmedCleanup(t *testing.T) {
	ctx := t.Context()
	pool := newDeadRootTestPool(t)
	repo := NewFileRepository(pool)
	folderID := seedDeadRootTestFolder(t, pool, "movies", "reconcile-confirmed-empty")
	root := t.TempDir()
	filePath := filepath.Join(root, "movie.mkv")

	if _, err := repo.Upsert(ctx, models.MediaFile{MediaFolderID: folderID, FilePath: filePath, FileSize: 1}); err != nil {
		t.Fatalf("seed media file: %v", err)
	}
	snapshot, err := repo.GetScanStateByFolder(ctx, folderID)
	if err != nil {
		t.Fatalf("snapshot stale full scan: %v", err)
	}

	// Scoped scan B completes an unchanged-file observation. No upsert is
	// required for this fast path, so the explicit presence refresh must carry
	// the freshness fence.
	refreshed, err := repo.RefreshPresentPaths(ctx, folderID, []string{filePath})
	if err != nil {
		t.Fatalf("refresh unchanged present path: %v", err)
	}
	if refreshed != 1 {
		t.Fatalf("refreshed rows = %d, want 1", refreshed)
	}

	// Full scan A resumes from its empty snapshot and takes the production
	// confirmed-cleanup path. Both its missing mark and hard delete must lose
	// to B's completed presence observation.
	scanner := &Scanner{fileRepo: repo}
	scope := &scopedScan{
		walkRoots:      []string{root},
		reconcileRoots: []string{root},
		existingFiles:  snapshot,
		seenPaths:      map[string]bool{},
		result:         &ScanResult{},
	}
	if err := scanner.applyScopedScan(ctx, &models.MediaFolder{ID: folderID}, scope, true, nil); err != nil {
		t.Fatalf("apply stale confirmed cleanup: %v", err)
	}
	if scope.result.Missing != 0 || scope.result.FilesDeleted != 0 {
		t.Fatalf("stale cleanup result = missing %d deleted %d, want both 0", scope.result.Missing, scope.result.FilesDeleted)
	}

	got, err := repo.GetByPath(ctx, filePath)
	if err != nil {
		t.Fatalf("reload refreshed file: %v", err)
	}
	if got.MissingSince != nil {
		t.Fatalf("refreshed file marked missing at %v", got.MissingSince)
	}
}

func TestStaleRemovedPathCleanupPreservesConcurrentRefresh(t *testing.T) {
	ctx := t.Context()
	pool := newDeadRootTestPool(t)
	repo := NewFileRepository(pool)
	folderID := seedDeadRootTestFolder(t, pool, "movies", "reconcile-removed-root")
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	filePath := filepath.Join(oldRoot, "movie.mkv")

	if _, err := repo.Upsert(ctx, models.MediaFile{MediaFolderID: folderID, FilePath: filePath, FileSize: 1}); err != nil {
		t.Fatalf("seed media file: %v", err)
	}
	fences, err := repo.ListFencesOutsideRoots(ctx, folderID, []string{newRoot})
	if err != nil {
		t.Fatalf("snapshot production outside-root fences: %v", err)
	}
	if _, err := repo.RefreshPresentPaths(ctx, folderID, []string{filePath}); err != nil {
		t.Fatalf("refresh file in overlapping scan: %v", err)
	}

	deleted, err := repo.DeleteIfUnchanged(ctx, fences)
	if err != nil {
		t.Fatalf("apply stale removed-root cleanup: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("stale removed-root cleanup deleted %d rows, want 0", deleted)
	}
	if _, err := repo.GetByPath(ctx, filePath); err != nil {
		t.Fatalf("refreshed file did not survive removed-root cleanup: %v", err)
	}
}

func TestStaleReconciliationRechecksGenerationAfterRefreshLock(t *testing.T) {
	tests := []struct {
		name        string
		waitPattern string
		reconcile   func(context.Context, *FileRepository, *scanStateFile) (int, error)
	}{
		{
			name:        "mark missing",
			waitPattern: "%WHERE id = $2 AND missing_since IS NULL%",
			reconcile: func(ctx context.Context, repo *FileRepository, snapshot *scanStateFile) (int, error) {
				marked, err := repo.MarkMissingIfUnchanged(ctx, snapshot.ID, snapshot.ScanGeneration, time.Now().UTC())
				if marked {
					return 1, err
				}
				return 0, err
			},
		},
		{
			name:        "delete",
			waitPattern: "%JOIN unnest($1::int[], $2::bigint[])%",
			reconcile: func(ctx context.Context, repo *FileRepository, snapshot *scanStateFile) (int, error) {
				return repo.DeleteIfUnchanged(ctx, []fileScanFence{{
					ID:             snapshot.ID,
					ScanGeneration: snapshot.ScanGeneration,
				}})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			pool := newDeadRootTestPool(t)
			repo := NewFileRepository(pool)
			folderID := seedDeadRootTestFolder(t, pool, "movies", "reconcile-lock-recheck-"+tt.name)
			filePath := filepath.Join(t.TempDir(), "movie.mkv")

			if _, err := repo.Upsert(ctx, models.MediaFile{MediaFolderID: folderID, FilePath: filePath, FileSize: 1}); err != nil {
				t.Fatalf("seed media file: %v", err)
			}
			snapshot, err := repo.GetScanStateByFolderAndPath(ctx, folderID, filePath)
			if err != nil {
				t.Fatalf("take stale snapshot: %v", err)
			}

			refreshTx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin refresh transaction: %v", err)
			}
			refreshCommitted := false
			defer func() {
				if !refreshCommitted {
					_ = refreshTx.Rollback(ctx)
				}
			}()

			// Execute the locking and generation-bump portion of
			// RefreshPresentPaths, but hold the transaction open. This puts stale
			// reconciliation A behind an uncommitted presence observation B.
			var refreshed int
			if err := refreshTx.QueryRow(ctx, `
				WITH present AS MATERIALIZED (
					SELECT id
					FROM media_files
					WHERE media_folder_id = $1 AND file_path = $2
					FOR UPDATE
				), bumped AS (
					UPDATE media_file_scan_generations AS generation
					SET scan_generation = generation.scan_generation + 1
					FROM present
					WHERE generation.media_file_id = present.id
					RETURNING generation.media_file_id
				)
				SELECT count(*) FROM bumped
			`, folderID, filePath).Scan(&refreshed); err != nil {
				t.Fatalf("run uncommitted refresh: %v", err)
			}
			if refreshed != 1 {
				t.Fatalf("uncommitted refresh count = %d, want 1", refreshed)
			}

			type reconcileResult struct {
				count int
				err   error
			}
			done := make(chan reconcileResult, 1)
			go func() {
				count, err := tt.reconcile(ctx, repo, snapshot)
				done <- reconcileResult{count: count, err: err}
			}()

			waitDeadline := time.Now().Add(10 * time.Second)
			for {
				var waiting int
				if err := pool.QueryRow(ctx, `
					SELECT count(*)
					FROM pg_stat_activity
					WHERE datname = current_database()
					  AND wait_event_type = 'Lock'
					  AND query LIKE $1
				`, tt.waitPattern).Scan(&waiting); err != nil {
					t.Fatalf("poll stale reconciliation lock: %v", err)
				}
				if waiting > 0 {
					break
				}
				if time.Now().After(waitDeadline) {
					t.Fatal("stale reconciliation never blocked behind refresh")
				}
				time.Sleep(20 * time.Millisecond)
			}

			if err := refreshTx.Commit(ctx); err != nil {
				t.Fatalf("commit refresh transaction: %v", err)
			}
			refreshCommitted = true

			select {
			case result := <-done:
				if result.err != nil {
					t.Fatalf("stale reconciliation after refresh commit: %v", result.err)
				}
				if result.count != 0 {
					t.Fatalf("stale reconciliation changed %d rows, want 0", result.count)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("stale reconciliation remained blocked after refresh commit")
			}

			got, err := repo.GetByPath(ctx, filePath)
			if err != nil {
				t.Fatalf("reload refreshed file: %v", err)
			}
			if got.MissingSince != nil {
				t.Fatalf("refreshed file marked missing at %v", got.MissingSince)
			}
		})
	}
}

func TestLiteraryAndPodcastReconciliationUsesPreWalkFence(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		reconcile func(context.Context, *Scanner, *models.MediaFolder, string, map[string]bool, []*scanStateFile) error
	}{
		{
			name:      "audiobook",
			mediaType: "audiobooks",
			reconcile: func(ctx context.Context, scanner *Scanner, folder *models.MediaFolder, root string, seenPaths map[string]bool, snapshot []*scanStateFile) error {
				return scanner.reconcileAudiobookMissingFiles(ctx, folder, []string{root}, seenPaths, snapshot, false)
			},
		},
		{
			name:      "ebook",
			mediaType: "ebooks",
			reconcile: func(ctx context.Context, scanner *Scanner, folder *models.MediaFolder, root string, seenPaths map[string]bool, snapshot []*scanStateFile) error {
				return scanner.reconcileEbookScan(ctx, folder, []ebookRootScan{{root: root}}, seenPaths, snapshot, false)
			},
		},
		{
			name:      "manga",
			mediaType: "manga",
			reconcile: func(ctx context.Context, scanner *Scanner, folder *models.MediaFolder, root string, seenPaths map[string]bool, snapshot []*scanStateFile) error {
				return scanner.reconcileMangaScan(ctx, folder, []ebookRootScan{{root: root}}, seenPaths, snapshot, false)
			},
		},
		{
			name:      "podcast",
			mediaType: "podcasts",
			reconcile: func(ctx context.Context, scanner *Scanner, folder *models.MediaFolder, root string, seenPaths map[string]bool, snapshot []*scanStateFile) error {
				return scanner.reconcilePodcastMissingFiles(ctx, folder, []string{root}, seenPaths, snapshot)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			pool := newDeadRootTestPool(t)
			repo := NewFileRepository(pool)
			folderID := seedDeadRootTestFolder(t, pool, tt.mediaType, "reconcile-"+tt.name)
			root := t.TempDir()
			stalePath := filepath.Join(root, "stale.dat")
			seenPath := filepath.Join(root, "seen.dat")
			if err := os.WriteFile(seenPath, []byte("present"), 0o644); err != nil {
				t.Fatalf("create seen control file: %v", err)
			}

			for _, path := range []string{stalePath, seenPath} {
				if _, err := repo.Upsert(ctx, models.MediaFile{MediaFolderID: folderID, FilePath: path, FileSize: 1}); err != nil {
					t.Fatalf("seed %s: %v", path, err)
				}
			}
			snapshot, err := repo.GetScanStateByFolderAndPathPrefix(ctx, folderID, root)
			if err != nil {
				t.Fatalf("take pre-walk snapshot: %v", err)
			}

			// Overlapping scan B observes the path that A's filesystem walk
			// missed, without needing to change any media metadata.
			if _, err := repo.RefreshPresentPaths(ctx, folderID, []string{stalePath}); err != nil {
				t.Fatalf("refresh stale candidate concurrently: %v", err)
			}

			scanner := NewScanner(repo, "", nil, 1, false, time.Hour)
			folder := &models.MediaFolder{ID: folderID, Type: tt.mediaType, Paths: []string{root}}
			if err := tt.reconcile(ctx, scanner, folder, root, map[string]bool{seenPath: true}, snapshot); err != nil {
				t.Fatalf("run stale %s reconciliation: %v", tt.name, err)
			}

			got, err := repo.GetByPath(ctx, stalePath)
			if err != nil {
				t.Fatalf("reload concurrently refreshed path: %v", err)
			}
			if got.MissingSince != nil {
				t.Fatalf("concurrently refreshed path marked missing at %v", got.MissingSince)
			}
		})
	}
}

func TestRepeatedUpsertAdvancesReconciliationGeneration(t *testing.T) {
	ctx := t.Context()
	pool := newDeadRootTestPool(t)
	repo := NewFileRepository(pool)
	folderID := seedDeadRootTestFolder(t, pool, "movies", "reconcile-repeated-upsert")
	filePath := filepath.Join(t.TempDir(), "movie.mkv")

	if _, err := repo.Upsert(ctx, models.MediaFile{MediaFolderID: folderID, FilePath: filePath, FileSize: 1}); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	initial, err := repo.GetScanStateByFolderAndPath(ctx, folderID, filePath)
	if err != nil {
		t.Fatalf("load initial generation: %v", err)
	}
	for size := int64(2); size <= 3; size++ {
		if _, err := repo.Upsert(ctx, models.MediaFile{MediaFolderID: folderID, FilePath: filePath, FileSize: size}); err != nil {
			t.Fatalf("repeat upsert size %d: %v", size, err)
		}
	}
	current, err := repo.GetScanStateByFolderAndPath(ctx, folderID, filePath)
	if err != nil {
		t.Fatalf("load current generation: %v", err)
	}
	if current.ScanGeneration != initial.ScanGeneration+2 {
		t.Fatalf("scan generation = %d, want %d", current.ScanGeneration, initial.ScanGeneration+2)
	}

	for _, staleGeneration := range []int64{initial.ScanGeneration, initial.ScanGeneration + 1} {
		marked, err := repo.MarkMissingIfUnchanged(ctx, current.ID, staleGeneration, time.Now().UTC())
		if err != nil {
			t.Fatalf("mark with generation %d: %v", staleGeneration, err)
		}
		if marked {
			t.Fatalf("stale generation %d marked current row missing", staleGeneration)
		}
	}
	marked, err := repo.MarkMissingIfUnchanged(ctx, current.ID, current.ScanGeneration, time.Now().UTC())
	if err != nil {
		t.Fatalf("mark current generation: %v", err)
	}
	if !marked {
		t.Fatal("current generation was not marked missing")
	}
}

func TestReconciliationFenceCoversFolderSubtreeAndFileSnapshots(t *testing.T) {
	tests := []struct {
		name     string
		snapshot func(*FileRepository, int, string, string) (*scanStateFile, error)
	}{
		{
			name: "folder",
			snapshot: func(repo *FileRepository, folderID int, _, _ string) (*scanStateFile, error) {
				files, err := repo.GetScanStateByFolder(t.Context(), folderID)
				if err != nil || len(files) != 1 {
					return nil, err
				}
				return files[0], nil
			},
		},
		{
			name: "subtree",
			snapshot: func(repo *FileRepository, folderID int, root, _ string) (*scanStateFile, error) {
				files, err := repo.GetScanStateByFolderAndPathPrefix(t.Context(), folderID, filepath.Join(root, "subtree"))
				if err != nil || len(files) != 1 {
					return nil, err
				}
				return files[0], nil
			},
		},
		{
			name: "file",
			snapshot: func(repo *FileRepository, folderID int, _, filePath string) (*scanStateFile, error) {
				return repo.GetScanStateByFolderAndPath(t.Context(), folderID, filePath)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			pool := newDeadRootTestPool(t)
			repo := NewFileRepository(pool)
			folderID := seedDeadRootTestFolder(t, pool, "movies", "reconcile-overlap-"+tt.name)
			root := t.TempDir()
			filePath := filepath.Join(root, "subtree", "movie.mkv")

			if _, err := repo.Upsert(ctx, models.MediaFile{MediaFolderID: folderID, FilePath: filePath, FileSize: 1}); err != nil {
				t.Fatalf("initial upsert: %v", err)
			}
			snapshot, err := tt.snapshot(repo, folderID, root, filePath)
			if err != nil {
				t.Fatalf("take %s snapshot: %v", tt.name, err)
			}
			if snapshot == nil {
				t.Fatalf("take %s snapshot: no row", tt.name)
			}
			if _, err := repo.Upsert(ctx, models.MediaFile{MediaFolderID: folderID, FilePath: filePath, FileSize: 2}); err != nil {
				t.Fatalf("overlapping refresh: %v", err)
			}

			marked, err := repo.MarkMissingIfUnchanged(ctx, snapshot.ID, snapshot.ScanGeneration, time.Now().UTC())
			if err != nil {
				t.Fatalf("stale %s reconciliation: %v", tt.name, err)
			}
			if marked {
				t.Fatalf("stale %s reconciliation marked refreshed row", tt.name)
			}
		})
	}
}
