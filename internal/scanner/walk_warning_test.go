package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestScanPartialWalkWarning(t *testing.T) {
	for _, kind := range []string{"ebooks", "audiobooks", "movies"} {
		t.Run(kind, func(t *testing.T) {
			pool := newDeadRootTestPool(t)
			ctx := context.Background()
			folderID := seedDeadRootTestFolder(t, pool, kind, "Partial walk warning")
			root := t.TempDir()
			// Dangling entries deterministically reproduce an incomplete inventory
			// even when tests run as root. Retry exhaustion is covered separately.
			for _, name := range []string{"first", "second"} {
				if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
			}
			folder := &models.MediaFolder{ID: folderID, Type: kind, Paths: []string{root}}
			scanner := NewScanner(NewFileRepository(pool), "", nil, 1, true, 0)
			if err := scanner.folderRepo.SetScanWarning(ctx, folderID, "dead_root", "Previous root outage", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			scan := func() {
				t.Helper()
				var err error
				switch kind {
				case "ebooks":
					err = scanner.ScanEbookFolder(ctx, folder)
				case "audiobooks":
					err = scanner.ScanAudiobookFolder(ctx, folder, true)
				default:
					_, err = scanner.ScanFolder(ctx, folder)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			warning := func() (code, message *string) {
				t.Helper()
				if err := pool.QueryRow(ctx, `SELECT scan_warning_code, scan_warning_message FROM media_folders WHERE id = $1`, folderID).Scan(&code, &message); err != nil {
					t.Fatal(err)
				}
				return
			}
			scan()
			code, message := warning()
			if code == nil || *code != "partial_walk" || message == nil || !strings.Contains(*message, "2 paths") {
				t.Fatalf("warning code=%v message=%v; want partial_walk with 2 failed paths", code, message)
			}
			for _, name := range []string{"first", "second"} {
				if err := os.Remove(filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
			}
			scan()
			if code, message := warning(); code != nil || message != nil {
				t.Fatalf("healthy scan retained stale warning: code=%v message=%v", code, message)
			}
		})
	}
}

func TestPartialWalkPreservesStrongerWarning(t *testing.T) {
	for _, kind := range []string{"ebooks", "audiobooks", "movies"} {
		for _, code := range []string{"empty_root", "dead_root"} {
			t.Run(kind+"/"+code, func(t *testing.T) {
				pool := newDeadRootTestPool(t)
				ctx := context.Background()
				folderID := seedDeadRootTestFolder(t, pool, kind, "Partial walk with cleanup warning")
				root := t.TempDir()
				if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "unreadable")); err != nil {
					t.Fatal(err)
				}
				scanner := NewScanner(NewFileRepository(pool), "", nil, 1, true, 0)
				if err := scanner.folderRepo.SetScanWarning(ctx, folderID, code, "Cleanup requires confirmation.", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				folder := &models.MediaFolder{ID: folderID, Type: kind, Paths: []string{root}}
				var err error
				switch kind {
				case "ebooks":
					err = scanner.scanEbookPaths(ctx, folder, folder.Paths, false)
				case "audiobooks":
					err = scanner.ScanAudiobookFolder(ctx, folder, false)
				default:
					_, err = scanner.ScanSubtree(ctx, folder, root)
				}
				if err != nil {
					t.Fatal(err)
				}
				var gotCode, message string
				if err := pool.QueryRow(ctx, `SELECT scan_warning_code, scan_warning_message FROM media_folders WHERE id = $1`, folderID).Scan(&gotCode, &message); err != nil {
					t.Fatal(err)
				}
				if gotCode != code || !strings.Contains(message, "Cleanup requires confirmation.") || !strings.Contains(message, "1 path") {
					t.Fatalf("code=%q message=%q; want stronger %s warning plus failed-path count", gotCode, message, code)
				}
			})
		}
	}
}

func TestPartialWalkRetainsCleanupGuardAndCatalog(t *testing.T) {
	for _, kind := range []string{"ebooks", "audiobooks", "movies"} {
		t.Run(kind, func(t *testing.T) {
			pool := newDeadRootTestPool(t)
			ctx := context.Background()
			folderID := seedDeadRootTestFolder(t, pool, kind, "Partial walk cleanup safety")
			partial, empty := t.TempDir(), t.TempDir()
			if err := os.Symlink(filepath.Join(partial, "missing"), filepath.Join(partial, "unreadable")); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO media_files (media_folder_id, file_path, file_size) VALUES ($1, $2, 1024)`, folderID, filepath.Join(empty, "cataloged-media")); err != nil {
				t.Fatal(err)
			}
			folder := &models.MediaFolder{ID: folderID, Type: kind, Paths: []string{partial, empty}}
			scanner := NewScanner(NewFileRepository(pool), "", nil, 1, true, 0)
			if _, err := scanner.ScanFolder(ctx, folder); err != nil {
				t.Fatal(err)
			}
			var code, message string
			if err := pool.QueryRow(ctx, `SELECT scan_warning_code, scan_warning_message FROM media_folders WHERE id = $1`, folderID).Scan(&code, &message); err != nil {
				t.Fatal(err)
			}
			if code != "empty_root" || !strings.Contains(message, "1 path") {
				t.Fatalf("code=%q message=%q; want current cleanup warning plus partial-walk count", code, message)
			}
			var live int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_files WHERE media_folder_id = $1 AND missing_since IS NULL`, folderID).Scan(&live); err != nil {
				t.Fatal(err)
			}
			if live != 1 {
				t.Fatalf("live files=%d; empty-root guard must protect the catalog", live)
			}
		})
	}
}

func TestEbookPartialWalkProtectsFilesDuringTrashSweep(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "ebooks", "Partial ebook walk trash safety")
	partial, healthy := t.TempDir(), t.TempDir()
	failedPath := filepath.Join(partial, "unreadable")
	if err := os.Symlink(filepath.Join(partial, "missing"), failedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(healthy, "readme.txt"), []byte("readable root"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []bool{false, true} {
		var missing *time.Time
		name := "live.epub"
		if stale {
			at := time.Now().Add(-48 * time.Hour)
			missing = &at
			name = "already-missing.epub"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_files (media_folder_id, file_path, file_size, missing_since) VALUES ($1, $2, 1024, $3)`, folderID, filepath.Join(failedPath, name), missing); err != nil {
			t.Fatal(err)
		}
	}
	folder := &models.MediaFolder{ID: folderID, Type: "ebooks", Paths: []string{partial, healthy}}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 1, true, 0)
	if err := scanner.ScanEbookFolder(ctx, folder); err != nil {
		t.Fatal(err)
	}
	var total, missing int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(missing_since) FROM media_files WHERE media_folder_id = $1`, folderID).Scan(&total, &missing); err != nil {
		t.Fatal(err)
	}
	if total != 2 || missing != 1 {
		t.Fatalf("files=%d missing=%d; unreadable root must preserve both live and previously missing files", total, missing)
	}
}

func TestPartialWalkWithDeadRootWarning(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Partial walk and unreachable root")
	partial := t.TempDir()
	dead := filepath.Join(t.TempDir(), "unmounted")
	if err := os.Symlink(filepath.Join(partial, "missing"), filepath.Join(partial, "unreadable")); err != nil {
		t.Fatal(err)
	}
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{partial, dead}}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 1, true, 0)
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatal(err)
	}
	var code, message string
	if err := pool.QueryRow(ctx, `SELECT scan_warning_code, scan_warning_message FROM media_folders WHERE id = $1`, folderID).Scan(&code, &message); err != nil {
		t.Fatal(err)
	}
	if code != "dead_root" || !strings.Contains(message, dead) || !strings.Contains(message, "1 path") {
		t.Fatalf("code=%q message=%q; want dead-root warning with outage path and partial-walk count", code, message)
	}
}

func TestAudiobookRecoveredWalkClearsPartialWarningSuffix(t *testing.T) {
	for _, code := range []string{"empty_root", "dead_root"} {
		t.Run(code, func(t *testing.T) {
			pool := newDeadRootTestPool(t)
			ctx := context.Background()
			folderID := seedDeadRootTestFolder(t, pool, "audiobooks", "Recovered partial audiobook walk")
			root := t.TempDir()
			unreadable := filepath.Join(root, "unreadable")
			if err := os.Symlink(filepath.Join(root, "missing"), unreadable); err != nil {
				t.Fatal(err)
			}
			scanner := NewScanner(NewFileRepository(pool), "", nil, 1, true, 0)
			const originalMessage = "Cleanup requires confirmation."
			if err := scanner.folderRepo.SetScanWarning(ctx, folderID, code, originalMessage, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			folder := &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}}
			if err := scanner.ScanAudiobookFolder(ctx, folder, false); err != nil {
				t.Fatal(err)
			}
			var gotCode, message string
			readWarning := func() {
				t.Helper()
				if err := pool.QueryRow(ctx, `SELECT scan_warning_code, scan_warning_message FROM media_folders WHERE id = $1`, folderID).Scan(&gotCode, &message); err != nil {
					t.Fatal(err)
				}
			}
			readWarning()
			if gotCode != code || !strings.Contains(message, "1 path") {
				t.Fatalf("partial scan: code=%q message=%q; want combined warning", gotCode, message)
			}
			if err := os.Remove(unreadable); err != nil {
				t.Fatal(err)
			}
			if err := scanner.ScanAudiobookFolder(ctx, folder, true); err != nil {
				t.Fatal(err)
			}
			readWarning()
			if gotCode != code || message != originalMessage {
				t.Fatalf("recovered scan: code=%q message=%q; want original warning without stale partial-walk count", gotCode, message)
			}
		})
	}
}
