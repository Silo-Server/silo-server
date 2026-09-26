package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
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
			if !ok || observation.RootPath != filepath.Join(tc.root, tc.wantRoot) || observation.HasFolderIDs {
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

func TestThemeScanAtLibraryRoot(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required")
	}
	root := t.TempDir()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Theme at library root")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=220:duration=1", "-y", filepath.Join(root, "theme.mp3")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_files(media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3)`, folderID, filepath.Join(root, "movie.mkv"), root); err != nil {
		t.Fatal(err)
	}
	s := NewScanner(NewFileRepository(pool), ffprobe, nil, 1, false, 0)
	if err := s.scanThemeSongs(t.Context(), &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}, "", false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM item_theme_songs WHERE media_folder_id=$1`, folderID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("discovered %d themes at library root, want 1", count)
	}
	var before, after string
	if err := pool.QueryRow(t.Context(), `SELECT xmin::text FROM item_theme_songs WHERE media_folder_id=$1`, folderID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.scanThemeSongs(t.Context(), &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}, "", false); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT xmin::text FROM item_theme_songs WHERE media_folder_id=$1`, folderID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("unchanged theme was rewritten")
	}
	if err := os.Remove(filepath.Join(root, "theme.mp3")); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanFile(t.Context(), filepath.Join(root, "theme.mp3"), &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM item_theme_songs WHERE media_folder_id=$1`, folderID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("removed theme was not reconciled by its file event: count=%d err=%v", count, err)
	}
}

func TestThemeScanSkipsPodcastAndUnknownKinds(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required")
	}
	root := t.TempDir()
	theme := filepath.Join(root, "theme.mp3")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=220:duration=1", "-y", theme).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	s := NewScanner(NewFileRepository(pool), ffprobe, nil, 1, false, 0)
	for _, kind := range []string{"podcasts", "future-audio-kind"} {
		t.Run(kind, func(t *testing.T) {
			folderID := seedDeadRootTestFolder(t, pool, kind, "Non-video theme gate")
			folder := &models.MediaFolder{ID: folderID, Type: kind, Paths: []string{root}}
			if _, err := pool.Exec(t.Context(), `INSERT INTO media_files(media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3)`, folderID, filepath.Join(root, "episode.mkv"), root); err != nil {
				t.Fatal(err)
			}
			if err := s.scanThemeSongs(t.Context(), folder, "", false); err != nil {
				t.Fatal(err)
			}
			var themes int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM item_theme_songs WHERE media_folder_id=$1`, folderID).Scan(&themes); err != nil {
				t.Fatal(err)
			}
			if themes != 0 {
				t.Errorf("%s library indexed %d themes during discovery", kind, themes)
			}
			if err := s.ScanFile(t.Context(), theme, folder); err == nil || !strings.Contains(err.Error(), "unrecognized video extension") {
				t.Errorf("non-video ScanFile theme route returned %v", err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM item_theme_songs WHERE media_folder_id=$1`, folderID).Scan(&themes); err != nil {
				t.Fatal(err)
			}
			if themes != 0 {
				t.Errorf("%s library kept %d themes after file event", kind, themes)
			}
		})
	}
}

func TestOptionalThemeFailurePreservesCompletedMediaScan(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required")
	}
	root := t.TempDir()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Optional theme failure")
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}
	video, err := os.ReadFile("testdata/test.mp4")
	if err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(videoPath, video, 0600); err != nil {
		t.Fatal(err)
	}
	themePath := filepath.Join(root, "theme.mp3")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=220:duration=1", "-y", themePath).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	function := fmt.Sprintf("scanner_theme_failure_%d", folderID)
	trigger := fmt.Sprintf("scanner_theme_failure_trigger_%d", folderID)
	if _, err := pool.Exec(t.Context(), fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.media_folder_id = %d THEN RAISE EXCEPTION 'theme write blocked'; END IF; RETURN NEW; END $$`, function, folderID)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON item_theme_songs", trigger))
		_, _ = pool.Exec(context.Background(), fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", function))
	})
	if _, err := pool.Exec(t.Context(), fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON item_theme_songs FOR EACH ROW EXECUTE FUNCTION %s()", trigger, function)); err != nil {
		t.Fatal(err)
	}
	s := NewScanner(NewFileRepository(pool), ffprobe, nil, 1, false, 0)
	result, err := s.ScanFolder(t.Context(), folder)
	if err != nil || result == nil || result.New != 1 {
		t.Fatalf("completed folder media scan discarded: result=%+v err=%v", result, err)
	}
	result, err = s.ScanSubtree(t.Context(), folder, root)
	if err != nil || result == nil || result.Unchanged != 1 {
		t.Fatalf("completed subtree media scan discarded: result=%+v err=%v", result, err)
	}
	if err := s.ScanFile(t.Context(), videoPath, folder); err != nil {
		t.Fatalf("video event gated by optional theme failure: %v", err)
	}
	if err := s.ScanFile(t.Context(), themePath, folder); err == nil {
		t.Fatal("theme-only event swallowed its theme write failure")
	}
}

func TestOptionalThemeScanPreservesCancellation(t *testing.T) {
	pool := newDeadRootTestPool(t)
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Canceled optional theme")
	s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, kind := range []string{"movies", "podcasts"} {
		if err := s.scanOptionalThemeSongs(ctx, &models.MediaFolder{ID: folderID, Type: kind, Paths: []string{t.TempDir()}}, "", false); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancellation = %v, want context.Canceled", kind, err)
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

func TestThemeFileEventStaysInOwnerDirectory(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required")
	}
	root := t.TempDir()
	folder := seedDeadRootTestFolder(t, pool, "movies", "Exact theme scan")
	for i := range 200 {
		dir := filepath.Join(root, fmt.Sprintf("Title %d", i))
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO media_files(media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3)`, folder, filepath.Join(dir, "movie.mkv"), dir); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_files(media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3)`, folder, filepath.Join(root, "movie.mkv"), root); err != nil {
		t.Fatal(err)
	}
	theme := filepath.Join(root, "theme.mp3")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=220:duration=1", "-y", theme).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, err := os.ReadFile(theme)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Title 0", "theme.mp3"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s := NewScanner(NewFileRepository(pool), ffprobe, nil, 1, false, 0)
	if err := s.ScanFile(t.Context(), theme, &models.MediaFolder{ID: folder, Type: "movies", Paths: []string{root}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM item_theme_songs WHERE media_folder_id=$1`, folder).Scan(&count); err != nil || count != 1 {
		t.Fatalf("one root theme event imported descendant themes: count=%d err=%v", count, err)
	}
	reads := map[string]int{}
	d := themeDiscovery{roots: []string{root}, ffprobe: ffprobe, ancestors: make(map[themeIgnoreKey]themeIgnoreState), readDir: func(path string) ([]os.DirEntry, error) {
		reads[path]++
		return os.ReadDir(path)
	}}
	if err := d.scan(t.Context(), themesongs.NewRepository(pool), folder, root, true); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 1 || reads[root] != 1 {
		t.Fatalf("exact theme pass listed descendant directories: %v", reads)
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
