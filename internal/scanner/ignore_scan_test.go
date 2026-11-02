package scanner

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

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
