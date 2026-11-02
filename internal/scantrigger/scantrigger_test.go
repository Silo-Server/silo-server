package scantrigger

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeFolderRepo struct {
	folders []*models.MediaFolder
}

func (r *fakeFolderRepo) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	for _, folder := range r.folders {
		if folder.ID == id {
			return folder, nil
		}
	}
	return nil, catalog.ErrFolderNotFound
}

func (r *fakeFolderRepo) List(context.Context) ([]*models.MediaFolder, error) {
	return r.folders, nil
}

func TestResolverThemeFileEvents(t *testing.T) {
	root := t.TempDir()
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{ID: 7, Type: "series", Enabled: true, Paths: []string{root}}}}
	resolver := NewResolver(repo)
	for _, name := range []string{"Show/theme.mp3", "Show/theme-music/Opening.FLAC", "theme.mp3"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("audio"), 0600); err != nil {
			t.Fatal(err)
		}
		target, err := resolver.Resolve(t.Context(), Request{Path: path})
		if err != nil || target.Mode != ModeFile || target.Path != path {
			t.Fatalf("existing %s: target=%+v err=%v", name, target, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		target, err = resolver.ResolveVanishedPath(t.Context(), path, "autoscan")
		if err != nil || target.Mode != ModeFile || target.Path != path {
			t.Fatalf("vanished %s: target=%+v err=%v", name, target, err)
		}
	}
	for _, name := range []string{"Show/music.mp3", "Show/theme-music/nested/music.mp3"} {
		_, err := resolver.ResolveVanishedPath(t.Context(), filepath.Join(root, name), "autoscan")
		var requestErr *RequestError
		if !errors.As(err, &requestErr) || requestErr.Reason != ReasonUnsupportedExtension {
			t.Fatalf("ordinary audio accepted: %s: %v", name, err)
		}
	}
}

// TestResolverTargets covers how Resolve, ResolveMissingSubtree and
// ResolveVanishedPath classify a path, and the requests they reject. Paths are
// relative to a temp dir that is also the configured root unless root says
// otherwise.
func TestResolverTargets(t *testing.T) {
	const (
		resolve = iota
		missingSubtree
		vanished
	)
	for _, tc := range []struct {
		name       string
		call       int
		folder     models.MediaFolder
		root       string // configured root, when not the temp dir itself
		dirs       []string
		files      []string
		path       string
		wantMode   string
		wantPath   string // "" for a library target
		wantStatus int
		wantCode   string
	}{
		{name: "library root", call: resolve, folder: models.MediaFolder{ID: 7, Enabled: true},
			wantMode: ModeLibrary},
		{name: "subtree", call: resolve, folder: models.MediaFolder{ID: 8, Enabled: true},
			dirs: []string{"Show"}, path: "Show", wantMode: ModeSubtree, wantPath: "Show"},
		// Directly under the library root: widening to the directory would mean
		// a full library scan, so the exact file stays the target.
		{name: "video file under the root", call: resolve, folder: models.MediaFolder{ID: 9, Enabled: true},
			files: []string{"Movie (2024).mkv"}, path: "Movie (2024).mkv", wantMode: ModeFile, wantPath: "Movie (2024).mkv"},
		{name: "video file in a subdir scans its directory", call: resolve, folder: models.MediaFolder{ID: 10, Type: "movies", Enabled: true},
			files: []string{"Movie (2024)/Movie (2024).mkv"}, path: "Movie (2024)/Movie (2024).mkv", wantMode: ModeSubtree, wantPath: "Movie (2024)"},
		{name: "disabled library", call: resolve, folder: models.MediaFolder{ID: 10, Enabled: false},
			wantStatus: http.StatusConflict, wantCode: "conflict"},
		{name: "missing subtree without stat", call: missingSubtree, folder: models.MediaFolder{ID: 18, Enabled: true},
			path: "Missing Movie", wantMode: ModeSubtree, wantPath: "Missing Movie"},
		{name: "missing subtree at the library root", call: missingSubtree, folder: models.MediaFolder{ID: 19, Enabled: true},
			wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		// The whole movie folder is gone; a subtree scan of it could not see
		// anything, so the file itself stays the reconciling target.
		{name: "vanished video in a vanished dir", call: vanished, folder: models.MediaFolder{ID: 23, Type: "movies", Enabled: true},
			path: "Removed Movie (2026)/Removed Movie (2026).mkv", wantMode: ModeFile, wantPath: "Removed Movie (2026)/Removed Movie (2026).mkv"},
		{name: "vanished dir", call: vanished, folder: models.MediaFolder{ID: 21, Enabled: true},
			path: "Removed Movie (2026)", wantMode: ModeSubtree, wantPath: "Removed Movie (2026)"},
		{name: "vanished file directly under the root", call: vanished, folder: models.MediaFolder{ID: 22, Type: "movies", Enabled: true},
			path: "Movie (2026).mkv", wantMode: ModeFile, wantPath: "Movie (2026).mkv"},
		// Simulate an unmounted share: the configured root itself is gone.
		{name: "vanished path when the root is gone", call: vanished, folder: models.MediaFolder{ID: 23, Enabled: true},
			root: "mount", path: "mount/Movie (2026)/Movie (2026).mkv", wantStatus: http.StatusConflict, wantCode: "conflict"},
		{name: "vanished path in a disabled library", call: vanished, folder: models.MediaFolder{ID: 26, Enabled: false},
			path: "Movie (2026)/Movie (2026).mkv", wantStatus: http.StatusConflict, wantCode: "conflict"},
		// A regular file where a directory is expected makes Lstat on a child
		// path fail with ENOTDIR — a stat failure that is not ENOENT.
		{name: "vanished path on a non-ENOENT stat error", call: vanished, folder: models.MediaFolder{ID: 25, Enabled: true},
			files: []string{"Movie (2026)"}, path: "Movie (2026)/Movie (2026).mkv", wantStatus: http.StatusBadRequest},
		{name: "vanished path that still exists", call: vanished, folder: models.MediaFolder{ID: 24, Enabled: true},
			files: []string{"Movie (2026).mkv"}, path: "Movie (2026).mkv", wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			for _, dir := range tc.dirs {
				if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, file := range tc.files {
				path := filepath.Join(base, file)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			folder := tc.folder
			folder.Paths = []string{filepath.Join(base, tc.root)}
			resolver := NewResolver(&fakeFolderRepo{folders: []*models.MediaFolder{&folder}})
			path := filepath.Join(base, tc.path)

			var target *Target
			var err error
			switch tc.call {
			case resolve:
				target, err = resolver.Resolve(context.Background(), Request{Path: path})
			case missingSubtree:
				target, err = resolver.ResolveMissingSubtree(context.Background(), path, "autoscan")
			case vanished:
				target, err = resolver.ResolveVanishedPath(context.Background(), path, "autoscan")
			}

			if tc.wantStatus != 0 {
				var reqErr *RequestError
				if !errors.As(err, &reqErr) {
					t.Fatalf("expected RequestError, got %T: %v", err, err)
				}
				if reqErr.Status != tc.wantStatus || (tc.wantCode != "" && reqErr.Code != tc.wantCode) {
					t.Fatalf("unexpected error: %#v", reqErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve returned error: %v", err)
			}
			wantPath := ""
			if tc.wantPath != "" {
				wantPath = filepath.Join(base, tc.wantPath)
			}
			if target.Folder == nil || target.Folder.ID != tc.folder.ID || target.Mode != tc.wantMode || target.Path != wantPath {
				t.Fatalf("unexpected target: %#v", target)
			}
		})
	}
}

func TestResolverResolvesVanishedMediaFile(t *testing.T) {
	for _, tc := range []struct {
		name        string
		libraryType string
		wantMode    string
		wantDir     bool
	}{
		// A replaced video reconciles through its directory so the upgrade's
		// other changes (deleted version, rewritten sidecars) land too.
		{name: "Movie.mkv", libraryType: "movies", wantMode: ModeSubtree, wantDir: true},
		{name: "Episode.mkv", libraryType: "tv", wantMode: ModeSubtree, wantDir: true},
		{name: "Book.epub", libraryType: "ebooks", wantMode: ModeFile},
		{name: "Audiobook.m4b", libraryType: "audiobooks", wantMode: ModeFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mediaDir := filepath.Join(root, "Title")
			if err := os.Mkdir(mediaDir, 0o755); err != nil {
				t.Fatal(err)
			}
			vanished := filepath.Join(mediaDir, tc.name)
			repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
				ID:      20,
				Name:    "Media",
				Type:    tc.libraryType,
				Enabled: true,
				Paths:   []string{root},
			}}}

			target, err := NewResolver(repo).ResolveVanishedPath(context.Background(), vanished, "autoscan")
			if err != nil {
				t.Fatalf("ResolveVanishedPath returned error: %v", err)
			}
			wantPath := vanished
			if tc.wantDir {
				wantPath = mediaDir
			}
			if target.Folder == nil || target.Folder.ID != 20 || target.Mode != tc.wantMode || target.Path != wantPath {
				t.Fatalf("unexpected target: %#v", target)
			}
		})
	}
}

func TestResolverRejectsVideoFileWhoseDirectoryCannotBeInspected(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "Locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	movieDir := filepath.Join(locked, "Movie (2026)")
	if err := os.Mkdir(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	vanished := filepath.Join(movieDir, "Movie (2026).mkv")
	// Removing search permission on the parent makes stat of the movie
	// directory fail with EACCES rather than ENOENT.
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      24,
		Name:    "Movies",
		Type:    "movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).ResolveVanishedPath(context.Background(), vanished, "autoscan")
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Reason != ReasonPathPermissionDenied && reqErr.Reason != ReasonPathNotInspectable {
		t.Fatalf("unexpected reason: %#v", reqErr)
	}
}

func TestResolverClassifiesAudioAndEbookFiles(t *testing.T) {
	for _, tc := range []struct {
		name        string
		libraryType string
	}{
		{name: "Book.epub", libraryType: "ebooks"},
		{name: "Book.fb2.zip", libraryType: "ebooks"},
		{name: "Audiobook.m4b", libraryType: "audiobooks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// Book kinds keep exact-file scans even inside a subdirectory.
			bookDir := filepath.Join(root, "Title")
			if err := os.Mkdir(bookDir, 0o755); err != nil {
				t.Fatal(err)
			}
			filePath := filepath.Join(bookDir, tc.name)
			if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
				t.Fatal(err)
			}
			repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
				ID:      25,
				Name:    "Media",
				Type:    tc.libraryType,
				Enabled: true,
				Paths:   []string{root},
			}}}

			target, err := NewResolver(repo).Resolve(context.Background(), Request{Path: filePath})
			if err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}
			if target.Mode != ModeFile || target.Path != filepath.Clean(filePath) {
				t.Fatalf("unexpected target: %#v", target)
			}
		})
	}
}

func TestResolveAllIsAllOrFail(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(valid, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      11,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).ResolveAll(context.Background(), []Request{
		{Path: valid},
		{Path: filepath.Join(root, "missing.mkv")},
	})
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Message != "Path does not exist" {
		t.Fatalf("unexpected error message: %q", reqErr.Message)
	}
}

// An edited .nfo, a replaced image, or an added subtitle in a video library
// scans the directory of the videos it belongs to, where change detection
// re-applies the items whose sidecars changed. A subtitle in a Subs/ folder
// belongs to the videos beside that folder.
func TestResolverRoutesLocalSidecarsToTheirDirectory(t *testing.T) {
	root := t.TempDir()
	movie := filepath.Join(root, "Film (2020)")
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{
		{ID: 3, Type: "movies", Enabled: true, Paths: []string{root}},
	}}
	resolver := NewResolver(repo)
	for _, name := range []string{
		"movie.nfo", "poster.jpg", "Film (2020)-fanart.PNG", "clearlogo.webp",
		"Film (2020).en.srt", "Film (2020).idx",
		"Subs/English.srt", "Subtitles/Film (2020)/2_English.srt",
	} {
		path := filepath.Join(movie, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("sidecar"), 0o600); err != nil {
			t.Fatal(err)
		}
		target, err := resolver.Resolve(t.Context(), Request{Path: path, Trigger: "realtime_monitor", SidecarFolder: true})
		if err != nil || target.Mode != ModeSubtree || target.Path != movie || target.Trigger != "realtime_monitor" {
			t.Fatalf("existing %s: target=%+v err=%v", name, target, err)
		}
		// A change publisher that reports the file's directory gets the same
		// scan.
		target, err = resolver.Resolve(t.Context(), Request{Path: filepath.Dir(path), Trigger: "realtime_monitor"})
		if err != nil || target.Mode != ModeSubtree || target.Path != movie {
			t.Fatalf("directory of %s: target=%+v err=%v", name, target, err)
		}
		// The scan API still rejects a path that is not media.
		var requestErr *RequestError
		if _, err := resolver.Resolve(t.Context(), Request{Path: path}); !errors.As(err, &requestErr) || requestErr.Reason != ReasonUnsupportedExtension {
			t.Fatalf("scan API request for %s: err=%v, want unsupported_extension", name, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		target, err = resolver.ResolveVanishedPath(t.Context(), path, "realtime_monitor")
		if err != nil || target.Mode != ModeSubtree || target.Path != movie {
			t.Fatalf("vanished %s: target=%+v err=%v", name, target, err)
		}
	}
	// A removed subtitle folder reconciles its videos' directory too.
	subs := filepath.Join(movie, "Subs")
	if err := os.RemoveAll(subs); err != nil {
		t.Fatal(err)
	}
	target, err := resolver.ResolveVanishedPath(t.Context(), subs, "realtime_monitor")
	if err != nil || target.Mode != ModeSubtree || target.Path != movie {
		t.Fatalf("vanished Subs folder: target=%+v err=%v", target, err)
	}
	// The library monitor and autoscan subtree changes resolve through
	// ResolveMissingSubtree, which must widen the same way.
	for _, path := range []string{subs, filepath.Join(subs, "Film (2020)")} {
		target, err = resolver.ResolveMissingSubtree(t.Context(), path, "realtime_monitor")
		if err != nil || target.Mode != ModeSubtree || target.Path != movie {
			t.Fatalf("missing subtree %s: target=%+v err=%v", path, target, err)
		}
	}
}

func TestResolverDoesNotRouteRootOrNonVideoSidecars(t *testing.T) {
	root := t.TempDir()
	books := t.TempDir()
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{
		{ID: 3, Type: "movies", Enabled: true, Paths: []string{root}},
		{ID: 4, Type: "audiobooks", Enabled: true, Paths: []string{books}},
	}}
	resolver := NewResolver(repo)
	for _, path := range []string{
		// A sidecar in a flat library's root would turn into a library scan.
		filepath.Join(root, "Film (2020).nfo"),
		// Book libraries read their own cover files.
		filepath.Join(books, "Author", "cover.jpg"),
		// Not a sidecar at all.
		filepath.Join(root, "Film (2020)", "notes.txt"),
		// Subtitles in a Subs/ folder directly under a flat library's root.
		filepath.Join(root, "Subs", "English.srt"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := resolver.Resolve(t.Context(), Request{Path: path, SidecarFolder: true})
		var requestErr *RequestError
		if !errors.As(err, &requestErr) || requestErr.Reason != ReasonUnsupportedExtension {
			t.Fatalf("%s: err=%v, want unsupported_extension", path, err)
		}
	}
}

// Library-monitor events and scan requests carry fspath-encoded paths; a
// non-UTF-8 file that exists must classify as present, not as missing or
// vanished.
func TestResolverStatsEncodedNonUTF8Path(t *testing.T) {
	root := t.TempDir()
	raw := filepath.Join(root, "B\xe5rd", "B\xe5rd.mkv")
	if err := os.MkdirAll(filepath.Dir(raw), 0o755); err != nil {
		t.Skipf("file system rejects non-UTF-8 names: %v", err)
	}
	if err := os.WriteFile(raw, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	encoded := fspath.Encode(raw)
	resolver := NewResolver(&fakeFolderRepo{folders: []*models.MediaFolder{{ID: 7, Type: "movies", Enabled: true, Paths: []string{root}}}})

	// A movie file resolves to its folder's subtree.
	target, err := resolver.Resolve(t.Context(), Request{Path: encoded})
	if err != nil || target.Mode != ModeSubtree || target.Path != filepath.Dir(encoded) {
		t.Fatalf("Resolve file: target=%+v err=%v", target, err)
	}
	target, err = resolver.Resolve(t.Context(), Request{Path: filepath.Dir(encoded)})
	if err != nil || target.Mode != ModeSubtree {
		t.Fatalf("Resolve dir: target=%+v err=%v", target, err)
	}
	_, err = resolver.ResolveVanishedPath(t.Context(), encoded, "autoscan")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Reason != ReasonPathStillExists {
		t.Fatalf("ResolveVanishedPath on existing file: %v", err)
	}
}
