package scanner

import (
	"fmt"
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

// An ignore file the walk lists but cannot read must not count as absent: the
// folder would be imported now and sent to trash once the file is readable
// again. The walk records a failure there instead, which keeps the rows under
// it out of missing-file reconciliation.
func TestUnreadableIgnoreFileIsAWalkFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny access")
	}
	for _, kind := range []string{"series", "audiobook"} {
		for _, name := range []string{".ignore", ".siloignore"} {
			for _, scoped := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/scoped=%v", kind, name, scoped), func(t *testing.T) {
					root := t.TempDir()
					ext := map[string]string{"series": ".mkv", "audiobook": ".mp3"}[kind]
					hidden := filepath.Join(root, "Hidden")
					scope := filepath.Join(hidden, "Inner")
					writeTestFile(t, filepath.Join(root, "Shown", "keep"+ext), "media")
					writeTestFile(t, filepath.Join(scope, "secret"+ext), "media")
					ignoreFile := filepath.Join(hidden, name)
					writeTestFile(t, ignoreFile, "*"+ext)
					if err := os.Chmod(ignoreFile, 0o000); err != nil {
						t.Fatal(err)
					}
					walkRoot, wantFailure := root, hidden
					want := []string{filepath.Join(root, "Shown", "keep"+ext)}
					if scoped {
						// A scoped scan reads the ignore file as an ancestor's.
						walkRoot, wantFailure, want = scope, scope, nil
					}
					var got, failures []string
					switch kind {
					case "series":
						files, walkFailures, err := collectLogicalFilePaths(t.Context(), []string{walkRoot}, kind, []string{root})
						if err != nil {
							t.Fatal(err)
						}
						got, failures = files, walkFailures
					case "audiobook":
						scans, err := collectAudiobookRootScans(t.Context(), 1, []string{walkRoot}, []string{root}, true)
						if err != nil || len(scans) != 1 {
							t.Fatalf("walk: %v, scans: %+v", err, scans)
						}
						got, failures = slices.Sorted(maps.Keys(scans[0].seenPaths)), scans[0].walkFailures
						if scans[0].rootErr != nil {
							failures = append(failures, scans[0].root)
						}
					}
					if !slices.Equal(got, want) {
						t.Errorf("files = %v, want %v", got, want)
					}
					if !slices.Equal(failures, []string{wantFailure}) {
						t.Errorf("walk failures = %v, want [%s]", failures, wantFailure)
					}
				})
			}
		}
	}
}
