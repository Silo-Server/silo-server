package scantrigger

import (
	"context"
	"errors"

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
