package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/themesongs"
)

func TestThemeRootObservationPreservesLibraryBoundary(t *testing.T) {
	for _, tc := range []struct {
		kind, root, video, wantRoot string
	}{
		{"movies", "/media/Archive {tmdb-12345}", "Arrival.2016.1080p.mkv", "Arrival.2016.1080p"},
		{"series", "/media/Archive {tvdb-12345}", "Show.S01E01.mkv", "Show.S01E01.mkv"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			observation, ok := ObserveRoot(filepath.Join(tc.root, tc.video), tc.kind, tc.root)
			if !ok || observation.RootPath != filepath.Join(tc.root, tc.wantRoot) || observation.HasProviderIDs {
				t.Fatalf("video borrowed identity from its library root: %+v, observed=%v", observation, ok)
			}
			for _, audio := range []string{"theme.mp3", "theme-music/opening.mp3"} {
				if _, ok := ObserveRoot(filepath.Join(tc.root, audio), tc.kind, tc.root); ok {
					t.Fatalf("theme %q became a video root", audio)
				}
			}
		})
	}
}

func TestThemeAncestorRulesRejectsPathsOutsideLibrary(t *testing.T) {
	root := t.TempDir()
	d := themeDiscovery{
		roots:     []string{root},
		ancestors: make(map[themeIgnoreKey]themeIgnoreState),
		readDir: func(path string) ([]os.DirEntry, error) {
			t.Fatalf("read outside library root: %s", path)
			return nil, nil
		},
	}
	for _, directory := range []string{"/", filepath.Dir(root), root + "-other"} {
		if state := d.ancestorRules(directory, root); !errors.Is(state.err, themesongs.ErrUnavailable) {
			t.Errorf("directory=%q: err=%v, want unavailable", directory, state.err)
		}
	}
}

func TestThemeDiscoveryReadsSharedAncestorsOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".siloignore"), []byte("Ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	reads := map[string]int{}
	d := themeDiscovery{roots: []string{root}, ancestors: make(map[themeIgnoreKey]themeIgnoreState), readDir: func(path string) ([]os.DirEntry, error) {
		reads[path]++
		return os.ReadDir(path)
	}}
	for i := range 200 {
		dir := filepath.Join(root, fmt.Sprintf("Title %d", i))
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := d.discover(t.Context(), dir, nil); err != nil {
			t.Fatal(err)
		}
	}
	if reads[root] != 1 {
		t.Fatalf("listed the root %d times for 200 titles", reads[root])
	}
	ignored := filepath.Join(root, "Ignored")
	if _, err := d.discover(t.Context(), ignored, nil); err != nil || reads[ignored] != 0 {
		t.Fatalf("ancestor rule was not respected: reads=%d err=%v", reads[ignored], err)
	}
}

func TestThemeDiscoveryConventionsAndIgnores(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required")
	}
	root := t.TempDir()
	owner := filepath.Join(root, "Show")
	themes := filepath.Join(owner, "theme-music")
	if err := os.MkdirAll(themes, 0755); err != nil {
		t.Fatal(err)
	}
	sample := filepath.Join(owner, "theme.mp3")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=220:duration=1", "-y", sample).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, err := os.ReadFile(sample)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(themes, "opening.MP3"), filepath.Join(themes, "ignored.mp3"), filepath.Join(owner, "ordinary.mp3")} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(themes, ".siloignore"), []byte("ignored.mp3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sample, filepath.Join(themes, "linked.mp3")); err != nil {
		t.Fatal(err)
	}
	files, err := discoverThemeSongs(context.Background(), owner, []string{root}, ffprobe, nil)
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	for _, file := range files {
		if file.Container != "mp3" || file.DurationSeconds < 1 {
			t.Fatalf("probe=%+v", file)
		}
	}
	if err := os.WriteFile(filepath.Join(themes, "broken.mp3"), []byte("not audio"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err = discoverThemeSongs(context.Background(), owner, []string{root}, ffprobe, nil)
	if err != nil || len(files) != 2 {
		t.Fatalf("one corrupt file blocked valid themes: files=%+v err=%v", files, err)
	}
	cached := map[string]themesongs.File{}
	for _, file := range files {
		cached[file.Path] = file
	}
	if err := os.WriteFile(sample, []byte("incomplete replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(themes, "opening.MP3")); err != nil {
		t.Fatal(err)
	}
	files, err = discoverThemeSongs(context.Background(), owner, []string{root}, ffprobe, cached)
	if err != nil || len(files) != 1 || files[0].Path != sample || files[0].Size != cached[sample].Size {
		t.Fatalf("failed replacement must preserve only its own record: files=%+v err=%v", files, err)
	}
	if err := os.WriteFile(filepath.Join(owner, ".nomedia"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	files, err = discoverThemeSongs(context.Background(), owner, []string{root}, ffprobe, nil)
	if err != nil || len(files) != 0 {
		t.Fatalf("ignored owner: %+v %v", files, err)
	}
}

func TestThemeDiscoveryNestedMediaDirectoryKeepsCanonicalOwner(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required")
	}
	root := t.TempDir()
	owner := filepath.Join(root, "Show")
	nested := filepath.Join(owner, "theme-music")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	theme := filepath.Join(nested, "theme.mp3")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=220:duration=1", "-y", theme).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	// A video makes the theme-music directory eligible for its own pass.
	if err := os.WriteFile(filepath.Join(nested, "episode.mkv"), []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	parentFiles, err := discoverThemeSongs(t.Context(), owner, []string{root}, ffprobe, nil)
	if err != nil || len(parentFiles) != 1 || parentFiles[0].Path != theme || parentFiles[0].OwnerPath != owner {
		t.Fatalf("parent discovery: files=%+v err=%v", parentFiles, err)
	}
	nestedFiles, err := discoverThemeSongs(t.Context(), nested, []string{root}, ffprobe, nil)
	if err != nil || len(nestedFiles) != 0 {
		t.Fatalf("nested directory claimed parent's theme: files=%+v err=%v", nestedFiles, err)
	}
}

func TestThemeDiscoveryUnreadableAndCancelledAreNotEmptySnapshots(t *testing.T) {
	root := t.TempDir()
	if _, err := discoverThemeSongs(context.Background(), filepath.Join(root, "missing"), []string{root}, "ffprobe", nil); err == nil {
		t.Fatal("missing directory treated as an empty snapshot")
	}
	if err := os.WriteFile(filepath.Join(root, "theme.mp3"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := discoverThemeSongs(ctx, root, []string{root}, "ffprobe", nil); err == nil {
		t.Fatal("canceled scan treated as authoritative")
	}
}
