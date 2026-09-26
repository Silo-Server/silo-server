package scanner

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAudiobookScanDoesNotProbeIgnoredParts(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	root := t.TempDir()
	book := filepath.Join(root, "Book")
	data, err := os.ReadFile("testdata/audiobook_fixtures/single_book/book.m4b")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(book, "book.m4b"), string(data))
	writeTestFile(t, filepath.Join(book, "ignored.mp3"), "invalid audio")
	writeTestFile(t, filepath.Join(root, ".siloignore"), "Book/ignored.mp3")
	s := &Scanner{ffprobePath: ffprobe}
	err = s.ScanAudiobookFolder(t.Context(), &models.MediaFolder{ID: 1, Paths: []string{root}}, true)
	// With no repositories installed, indexing must get as far as the item
	// write. Probing the excluded corrupt file must not fail the whole book.
	if err == nil || !strings.Contains(err.Error(), "itemRepo not configured") {
		t.Fatalf("scan error = %v, want item write after probing only the kept part", err)
	}
}

func TestPodcastRootIgnoreMarkerSkipsShows(t *testing.T) {
	for _, marker := range []string{".ignore", ".nomedia"} {
		t.Run(marker, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, marker), "")
			writeTestFile(t, filepath.Join(root, "Show", "episode.mp3"), "audio")
			s := &Scanner{ffprobePath: "definitely-missing-ffprobe"}
			if err := s.ScanPodcastFolder(t.Context(), &models.MediaFolder{ID: 1, Paths: []string{root}}); err != nil {
				t.Fatalf("ignored show was scanned: %v", err)
			}
		})
	}
}

func TestAudiobookSubtreeHonorsAncestorIgnore(t *testing.T) {
	pool := newDeadRootTestPool(t)
	for _, pattern := range []string{".nomedia", ".siloignore"} {
		t.Run(pattern, func(t *testing.T) {
			root := t.TempDir()
			book := filepath.Join(root, "Author", "Book")
			writeTestFile(t, filepath.Join(book, "book.mp3"), "audio")
			writeTestFile(t, filepath.Join(root, pattern), "Author")
			folderID := seedDeadRootTestFolder(t, pool, "audiobooks", "Ignored subtree")
			s := NewScanner(NewFileRepository(pool), "definitely-missing-ffprobe", nil, 1, false, 0)
			_, err := s.ScanSubtree(t.Context(), &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}}, book)
			if err != nil {
				t.Fatalf("ignored subtree was scanned: %v", err)
			}
		})
	}
}

func TestAudiobookExplicitFileBypassesIgnoreMarker(t *testing.T) {
	pool := newDeadRootTestPool(t)
	root := t.TempDir()
	file := filepath.Join(root, "book.mp3")
	writeTestFile(t, file, "audio")
	writeTestFile(t, filepath.Join(root, ".nomedia"), "")
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", "Explicit ignored file")
	s := NewScanner(NewFileRepository(pool), "definitely-missing-ffprobe", nil, 1, false, 0)
	err := s.ScanFile(t.Context(), file, &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}})
	if err == nil || errors.Is(err, errFolderHasNoMedia) || !strings.Contains(err.Error(), "probe") {
		t.Fatalf("explicit file error = %v, want probe failure from the requested file", err)
	}
}

func TestScopedWalksInheritIgnoreRules(t *testing.T) {
	for _, kind := range []string{"series", "ebook", "audiobook"} {
		for _, ignore := range []string{"marker", "pruned ancestor", "stacked patterns"} {
			t.Run(kind+"/"+ignore, func(t *testing.T) {
				root := t.TempDir()
				scope := filepath.Join(root, "Author", "Book")
				ext := map[string]string{"series": ".mkv", "ebook": ".epub", "audiobook": ".mp3"}[kind]
				for _, name := range []string{"keep", "root-rule", "parent-rule", "local-rule"} {
					writeTestFile(t, filepath.Join(scope, name+ext), "media")
				}
				var want []string
				switch ignore {
				case "marker":
					writeTestFile(t, filepath.Join(root, ".nomedia"), "")
				case "pruned ancestor":
					writeTestFile(t, filepath.Join(root, ".siloignore"), "Author")
				case "stacked patterns":
					writeTestFile(t, filepath.Join(root, ".siloignore"), "Author/Book/root-rule*")
					writeTestFile(t, filepath.Join(root, "Author", ".siloignore"), "Book/parent-rule*")
					writeTestFile(t, filepath.Join(scope, ".siloignore"), "local-rule*")
					want = []string{filepath.Join(scope, "keep"+ext)}
				}
				var got []string
				switch kind {
				case "series":
					files, failures, err := collectLogicalFilePaths(t.Context(), []string{scope}, kind, []string{root})
					if err != nil || len(failures) != 0 {
						t.Fatalf("walk: %v, failures: %v", err, failures)
					}
					got = files
				case "ebook":
					scans, err := collectEbookRootScans(t.Context(), 1, []string{scope}, []string{root})
					if err != nil || len(scans) != 1 || scans[0].failed() {
						t.Fatalf("walk: %v, scans: %+v", err, scans)
					}
					got = scans[0].files
				case "audiobook":
					scans, err := collectAudiobookRootScans(t.Context(), 1, []string{scope}, []string{root}, true)
					if err != nil || len(scans) != 1 || scans[0].failed() {
						t.Fatalf("walk: %v, scans: %+v", err, scans)
					}
					got = slices.Sorted(maps.Keys(scans[0].seenPaths))
				}
				if !slices.Equal(got, want) {
					t.Fatalf("files = %v, want %v", got, want)
				}
			})
		}
	}
}

// TestSubtreeScanUnderSkippedLibraryRootKeepsCatalog pins the guard that
// keeps a skipped library root out of subtree reconciliation. A subtree scan
// there walks nothing; without the guard it marks the subtree missing (and,
// with trash emptying on, deletes it) outside the full scan's empty-root guard.
func TestSubtreeScanUnderSkippedLibraryRootKeepsCatalog(t *testing.T) {
	pool := newDeadRootTestPool(t)
	for _, marker := range []string{".ignore", ".nomedia"} {
		t.Run(marker, func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			episode := filepath.Join(root, "Show", "Season 01", "Episode 01.mkv")
			writeTestFile(t, episode, "media")
			writeTestFile(t, filepath.Join(root, marker), "")
			folderID := seedDeadRootTestFolder(t, pool, "series", "Skipped root subtree "+marker)
			var fileID int
			if err := pool.QueryRow(ctx,
				`INSERT INTO media_files (media_folder_id, file_path, file_size) VALUES ($1, $2, 1024) RETURNING id`,
				folderID, episode,
			).Scan(&fileID); err != nil {
				t.Fatalf("seed media file: %v", err)
			}
			folder := &models.MediaFolder{ID: folderID, Type: "series", Paths: []string{root}, Enabled: true}
			s := NewScanner(NewFileRepository(pool), "definitely-missing-ffprobe", nil, 1, true, 0)
			for _, scope := range []string{filepath.Dir(episode), root} {
				if _, err := s.ScanSubtree(ctx, folder, scope); err != nil {
					t.Fatalf("ScanSubtree(%s): %v", scope, err)
				}
			}
			var missingSince *time.Time
			if err := pool.QueryRow(ctx, `SELECT missing_since FROM media_files WHERE id = $1`, fileID).Scan(&missingSince); err != nil {
				t.Fatalf("load media file (deleted?): %v", err)
			}
			if missingSince != nil {
				t.Fatalf("missing_since = %v, want the row left for a full library scan", missingSince)
			}
		})
	}
}

func TestScanRootIgnoreRulesStayWithinLibrary(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "Library")
	scope := filepath.Join(root, "Book")
	writeTestFile(t, filepath.Join(base, ".nomedia"), "")
	writeTestFile(t, filepath.Join(scope, "book.mp3"), "media")
	_, ignored, err := scanRootIgnoreRules(scope, []string{root})
	if err != nil || ignored {
		t.Fatalf("outside marker affected library: ignored=%v err=%v", ignored, err)
	}
}

func TestAudiobookIgnoredBrokenSymlinkDoesNotProtectCleanup(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".siloignore"), "broken.mp3")
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken.mp3")); err != nil {
		t.Fatal(err)
	}
	scans, err := collectAudiobookRootScans(t.Context(), 1, []string{root}, []string{root}, true)
	if err != nil || len(scans) != 1 || scans[0].failed() {
		t.Fatalf("ignored link protected cleanup: %v, scans: %+v", err, scans)
	}
}

func TestAudiobookIgnoreUpdatesPresentation(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	root := t.TempDir()
	book := filepath.Join(root, "Book")
	data, err := os.ReadFile("testdata/audiobook_fixtures/single_book/book.m4b")
	if err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(book, "part1.m4b")
	ignored := filepath.Join(book, "part2.m4b")
	writeTestFile(t, kept, string(data))
	writeTestFile(t, ignored, string(data))
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", "Ignored audiobook part")
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`, folderID, root); err != nil {
		t.Fatal(err)
	}
	folder := &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}, Enabled: true}
	s := NewScanner(NewFileRepository(pool), ffprobe, nil, 1, true, 0)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_files WHERE media_folder_id=$1)`, folderID)
	})
	if err := s.ScanAudiobookFolder(ctx, folder, true); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, ".siloignore"), "Book/part2.m4b")
	writeTestFile(t, ignored, "corrupt excluded audio")
	// Rebuild after ignoring a part, allow an unchanged scan, and repair the
	// stale multipart fields left by a scanner that filtered only discovery.
	for scan := range 3 {
		if scan == 2 {
			if _, err := pool.Exec(ctx, `UPDATE media_files SET presentation_kind='multipart', presentation_group_key=content_id,
				presentation_part_index=1, presentation_part_total=2 WHERE media_folder_id=$1`, folderID); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.ScanAudiobookFolder(ctx, folder, true); err != nil {
			t.Fatal(err)
		}
		files, err := s.fileRepo.GetByFolderAndPathPrefix(ctx, folderID, root)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 {
			t.Fatalf("files after ignore = %d, want 1", len(files))
		}
		if files[0].FilePath != kept || files[0].MissingSince != nil || files[0].PresentationKind == "multipart" || files[0].PresentationPartTotal != 0 {
			t.Fatalf("presentation after ignore = %+v, want the kept file as a standalone book", *files[0])
		}
	}
}
