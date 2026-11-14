package metadata

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The server's root resolver must list cataloged files, or symlinked title
// folders quietly lose their artwork again.
var _ catalogedFileLister = (*catalog.FolderRepository)(nil)

// catalogedRootResolver is a library root resolver that also lists
// cataloged files, as *catalog.FolderRepository does.
type catalogedRootResolver struct {
	roots []string
	files []string
	err   error
	asked []string
}

func (r *catalogedRootResolver) LibraryRootsForContent(context.Context, string) ([]string, error) {
	return r.roots, nil
}

func (r *catalogedRootResolver) CatalogedFilesUnder(_ context.Context, contentID, dir, after string, limit int) ([]string, error) {
	r.asked = append(r.asked, contentID)
	if r.err != nil {
		return nil, r.err
	}
	files := slices.Sorted(slices.Values(r.files))
	var out []string
	for _, file := range files {
		if strings.HasPrefix(file, dir+string(filepath.Separator)) && file > after && len(out) < limit {
			out = append(out, file)
		}
	}
	return out, nil
}

// symlinkedTitle lays out a library whose title folder is a symlink into a
// store tree, as download managers and debrid mounts do, and returns the
// library root and the title folder's path through the library.
func symlinkedTitle(t *testing.T, files map[string]string) (library, title string) {
	t.Helper()
	library = t.TempDir()
	store := filepath.Join(t.TempDir(), "Example (2020)")
	for rel, body := range files {
		path := filepath.Join(store, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	title = filepath.Join(library, "Example (2020)")
	if err := os.Symlink(store, title); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return library, title
}

func runLocalJob(t *testing.T, job *models.MetadataImageCacheJob, resolver LibraryRootResolver) (*fakeImageCacheJobs, *fakeByteImageCacher) {
	t.Helper()
	jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{job}}
	cacher := &fakeByteImageCacher{thumbhash: "th"}
	p := NewImageCacheProcessorWithTargets(jobs, cacher, nil, ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})
	p.SetLibraryRootResolver(resolver)
	if _, err := p.RunOnce(context.Background(), "w1", 10, 1); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return jobs, cacher
}

// A title folder that is a symlink out of the library, with a video the
// scanner cataloged through it, has its artwork cached, including artwork in
// a subfolder. Without the cataloged file the same layout is refused.
func TestProcessLocalImageAcceptsSymlinkedTitleFolderWithCatalogedVideo(t *testing.T) {
	library, title := symlinkedTitle(t, map[string]string{
		"Example (2020).mkv":      "video",
		"poster.jpg":              "poster-bytes",
		"extrafanart/fanart1.jpg": "fanart-bytes",
	})
	video := filepath.Join(title, "Example (2020).mkv")

	for _, tc := range []struct{ name, image, want string }{
		{"poster beside the video", "poster.jpg", "poster-bytes"},
		{"backdrop in a subfolder", "extrafanart/fanart1.jpg", "fanart-bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := localArtworkJob("file://" + filepath.Join(title, tc.image))
			resolver := &catalogedRootResolver{roots: []string{library}, files: []string{video}}
			jobs, cacher := runLocalJob(t, job, resolver)
			if len(cacher.byteReqs) != 1 || string(cacher.byteReqs[0]) != tc.want {
				t.Fatalf("cached %d images (failed text %q), want %q", len(cacher.byteReqs), jobs.failedText, tc.want)
			}
			if len(resolver.asked) == 0 || resolver.asked[0] != "movie-1" {
				t.Fatalf("cataloged files asked for %v, want the job's title", resolver.asked)
			}
		})
	}

	t.Run("no cataloged file", func(t *testing.T) {
		job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
		jobs, cacher := runLocalJob(t, job, &catalogedRootResolver{roots: []string{library}})
		if len(cacher.byteReqs) != 0 || !strings.Contains(jobs.failedText, "local image path outside library roots") {
			t.Fatalf("cached %d images, failed text %q; want the outside-roots refusal", len(cacher.byteReqs), jobs.failedText)
		}
		if !isStableProviderImageFailure(jobs.failedText) {
			t.Fatalf("refusal %q must stay a stable failure", jobs.failedText)
		}
	})

	t.Run("listing fails", func(t *testing.T) {
		job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
		resolver := &catalogedRootResolver{roots: []string{library}, err: errors.New("database unavailable")}
		jobs, cacher := runLocalJob(t, job, resolver)
		if len(cacher.byteReqs) != 0 || !strings.Contains(jobs.failedText, "local image read failed") {
			t.Fatalf("cached %d images, failed text %q; want a read failure", len(cacher.byteReqs), jobs.failedText)
		}
		if isStableProviderImageFailure(jobs.failedText) {
			t.Fatalf("a listing failure %q must retry as usual, not park", jobs.failedText)
		}
	})

	t.Run("resolver that cannot list files", func(t *testing.T) {
		job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
		jobs, cacher := runLocalJob(t, job, &fakeLibraryRootResolver{roots: []string{library}})
		if len(cacher.byteReqs) != 0 || !strings.Contains(jobs.failedText, "local image path outside library roots") {
			t.Fatalf("cached %d images, failed text %q; want the outside-roots refusal", len(cacher.byteReqs), jobs.failedText)
		}
	})
}

// A series folder linked out of the library proves itself with an episode in
// a season folder below it, so the series poster beside the seasons is read.
func TestProcessLocalImageAcceptsSymlinkedSeriesFolderWithCatalogedEpisode(t *testing.T) {
	library, title := symlinkedTitle(t, map[string]string{
		"Season 01/Example S01E01.mkv": "video",
		"poster.jpg":                   "series-poster",
	})
	job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
	job.TargetType = ImageCacheTargetItem
	job.TargetContentID = "series-1"
	job.SeriesID = "series-1"
	resolver := &catalogedRootResolver{roots: []string{library}, files: []string{filepath.Join(title, "Season 01", "Example S01E01.mkv")}}
	jobs, cacher := runLocalJob(t, job, resolver)
	if len(cacher.byteReqs) != 1 || string(cacher.byteReqs[0]) != "series-poster" {
		t.Fatalf("cached %d images (failed text %q), want the series poster", len(cacher.byteReqs), jobs.failedText)
	}
}

// A linked title folder whose name needs fspath encoding reaches the processor
// encoded, as the scanner stores it, and its cataloged video still vouches for
// it, so every check decodes the path before it reaches the file system.
func TestProcessLocalImageAcceptsSymlinkedTitleFolderWithAnEncodedName(t *testing.T) {
	library, storeRoot := t.TempDir(), t.TempDir()
	// A Latin-1 name, as older tools write one. A file system that takes only
	// UTF-8 names, such as APFS, gets a name holding a code point from
	// fspath's reserved range instead, which is escaped the same way.
	store := ""
	for _, name := range []string{"B\xe5rd (2020)", "B\U0010FE80rd (2020)"} {
		if err := os.Mkdir(filepath.Join(storeRoot, name), 0o755); err == nil {
			store = filepath.Join(storeRoot, name)
			break
		}
	}
	if store == "" {
		t.Skip("file system takes no name that needs encoding")
	}
	for name, body := range map[string]string{"Example (2020).mkv": "video", "poster.jpg": "poster-bytes"} {
		if err := os.WriteFile(filepath.Join(store, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rawTitle := filepath.Join(library, filepath.Base(store))
	if err := os.Symlink(store, rawTitle); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	title := fspath.Encode(rawTitle)
	if title == rawTitle {
		t.Fatalf("%q needs no encoding", rawTitle)
	}

	job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
	resolver := &catalogedRootResolver{roots: []string{library}, files: []string{filepath.Join(title, "Example (2020).mkv")}}
	jobs, cacher := runLocalJob(t, job, resolver)
	if len(cacher.byteReqs) != 1 || string(cacher.byteReqs[0]) != "poster-bytes" {
		t.Fatalf("cached %d images (failed text %q), want the poster", len(cacher.byteReqs), jobs.failedText)
	}
}

// A symlink planted in the library that points somewhere without a cataloged
// file of the title stays refused, even when the title has cataloged files
// elsewhere in the library.
func TestProcessLocalImageRefusesPlantedSymlinkWithoutCatalogedFile(t *testing.T) {
	library := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "poster.jpg"), []byte("outside-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	realTitle := filepath.Join(library, "Example (2020)")
	if err := os.Mkdir(realTitle, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(realTitle, "Example (2020).mkv")
	if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(realTitle, "extras")
	if err := os.Symlink(outside, planted); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	job := localArtworkJob("file://" + filepath.Join(planted, "poster.jpg"))
	jobs, cacher := runLocalJob(t, job, &catalogedRootResolver{roots: []string{library}, files: []string{video}})
	if len(cacher.byteReqs) != 0 || !strings.Contains(jobs.failedText, "local image path outside library roots") {
		t.Fatalf("cached %d images, failed text %q; want the outside-roots refusal", len(cacher.byteReqs), jobs.failedText)
	}
}

// A cataloged path that is itself a symlink out of the title folder proves
// nothing about the folder: the check goes through the opened folder, which
// will not follow it out.
func TestProcessLocalImageRefusesCatalogedFileLinkedOutOfTheFolder(t *testing.T) {
	library := t.TempDir()
	outside := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "real.mkv")
	if err := os.WriteFile(elsewhere, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "poster.jpg"), []byte("outside-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(outside, "Example (2020).mkv")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	title := filepath.Join(library, "Example (2020)")
	if err := os.Symlink(outside, title); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
	resolver := &catalogedRootResolver{roots: []string{library}, files: []string{filepath.Join(title, "Example (2020).mkv")}}
	jobs, cacher := runLocalJob(t, job, resolver)
	if len(cacher.byteReqs) != 0 || !strings.Contains(jobs.failedText, "local image path outside library roots") {
		t.Fatalf("cached %d images, failed text %q; want the outside-roots refusal", len(cacher.byteReqs), jobs.failedText)
	}
}

// An absolute link inside the title folder that stays in it doesn't stop the
// read: the path through the opened folder comes from resolved paths, as
// os.Root won't follow an absolute link.
func TestProcessLocalImageFollowsAbsoluteLinkInsideTheTitleFolder(t *testing.T) {
	library, title := symlinkedTitle(t, map[string]string{
		"Example (2020).mkv": "video",
		"art/fanart1.jpg":    "fanart-bytes",
	})
	store, err := filepath.EvalSymlinks(title)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(store, "art"), filepath.Join(store, "extrafanart")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	job := localArtworkJob("file://" + filepath.Join(title, "extrafanart", "fanart1.jpg"))
	resolver := &catalogedRootResolver{roots: []string{library}, files: []string{filepath.Join(title, "Example (2020).mkv")}}
	jobs, cacher := runLocalJob(t, job, resolver)
	if len(cacher.byteReqs) != 1 || string(cacher.byteReqs[0]) != "fanart-bytes" {
		t.Fatalf("cached %d images (failed text %q), want the fanart", len(cacher.byteReqs), jobs.failedText)
	}
}

// A cataloged file that vouches for the folder is found even when more than
// a page of cataloged files that can't vouch for it sort before it, and a
// folder none of whose cataloged files vouch for it stays refused.
func TestProcessLocalImageTriesEveryCatalogedFile(t *testing.T) {
	library, title := symlinkedTitle(t, map[string]string{
		"poster.jpg":   "poster-bytes",
		"zz-video.mkv": "video",
	})
	files := make([]string, 0, 2*catalogedFilePageSize+1)
	for i := range 2 * catalogedFilePageSize {
		files = append(files, filepath.Join(title, fmt.Sprintf("gone-%02d.mkv", i)))
	}
	job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
	jobs, cacher := runLocalJob(t, job, &catalogedRootResolver{roots: []string{library}, files: append(slices.Clone(files), filepath.Join(title, "zz-video.mkv"))})
	if len(cacher.byteReqs) != 1 || string(cacher.byteReqs[0]) != "poster-bytes" {
		t.Fatalf("cached %d images (failed text %q), want the poster", len(cacher.byteReqs), jobs.failedText)
	}

	job = localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
	jobs, cacher = runLocalJob(t, job, &catalogedRootResolver{roots: []string{library}, files: files})
	if len(cacher.byteReqs) != 0 || !isStableProviderImageFailure(jobs.failedText) {
		t.Fatalf("cached %d images, failed text %q; want the stable outside-roots refusal", len(cacher.byteReqs), jobs.failedText)
	}
}

// Storage failures keep a job retrying; a missing or forbidden path, or one an
// opened folder refuses to follow out of it, is an answer about the path.
func TestLocalFileIOErrorSeparatesStorageFailuresFromPathAnswers(t *testing.T) {
	if !localFileIOError(&fs.PathError{Op: "stat", Path: "x", Err: syscall.EIO}) {
		t.Fatal("EIO is a storage failure")
	}
	for _, errno := range []syscall.Errno{syscall.ENOENT, syscall.EACCES, syscall.ENOTDIR, syscall.ELOOP, syscall.ENAMETOOLONG} {
		if localFileIOError(&fs.PathError{Op: "stat", Path: "x", Err: errno}) {
			t.Fatalf("%v is an answer about the path, not a storage failure", errno)
		}
	}
	dir := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "out")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Stat("out"); err == nil || localFileIOError(err) {
		t.Fatalf("escape refusal %v must not count as a storage failure", err)
	}
}

// A cataloged video that is itself a link to another file inside the title
// folder, absolute, relative, or through another link and "..", still
// vouches for the folder; the link is read, not followed, so a target
// outside it is never touched.
func TestProcessLocalImageAcceptsCatalogedVideoLinkedInsideTheFolder(t *testing.T) {
	for _, link := range []string{"absolute", "relative", "through a link and .."} {
		t.Run(link, func(t *testing.T) {
			library, title := symlinkedTitle(t, map[string]string{
				"payload/Example (2020).mkv": "video",
				"payload/sub/keep":           "",
				"poster.jpg":                 "poster-bytes",
			})
			store, err := filepath.EvalSymlinks(title)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join("payload", "Example (2020).mkv")
			switch link {
			case "absolute":
				target = filepath.Join(store, target)
			case "through a link and ..":
				// alias/.. is payload, not the title folder, once alias is followed.
				if err := os.Symlink(filepath.Join("payload", "sub"), filepath.Join(store, "alias")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				target = "alias/../Example (2020).mkv"
			}
			if err := os.Symlink(target, filepath.Join(store, "Example (2020).mkv")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
			resolver := &catalogedRootResolver{roots: []string{library}, files: []string{filepath.Join(title, "Example (2020).mkv")}}
			jobs, cacher := runLocalJob(t, job, resolver)
			if len(cacher.byteReqs) != 1 || string(cacher.byteReqs[0]) != "poster-bytes" {
				t.Fatalf("cached %d images (failed text %q), want the poster", len(cacher.byteReqs), jobs.failedText)
			}
		})
	}
}

// Links that loop inside the folder end the check instead of spinning.
func TestPathInsideFolderStopsOnALinkLoop(t *testing.T) {
	dir := t.TempDir()
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b", filepath.Join(resolvedDir, "a")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("a", filepath.Join(resolvedDir, "b")); err != nil {
		t.Fatal(err)
	}
	if _, inFolder, err := pathInsideFolder(resolvedDir, "a"); inFolder || err != nil {
		t.Fatalf("loop resolved inFolder=%v err=%v, want neither", inFolder, err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(resolvedDir, "out")); err != nil {
		t.Fatal(err)
	}
	if _, inFolder, err := pathInsideFolder(resolvedDir, filepath.Join("out", "video.mkv")); inFolder || err != nil {
		t.Fatalf("link out resolved inFolder=%v err=%v, want neither", inFolder, err)
	}
	if err := os.Symlink(filepath.Join("..", filepath.Base(outside)), filepath.Join(resolvedDir, "up")); err != nil {
		t.Fatal(err)
	}
	if _, inFolder, err := pathInsideFolder(resolvedDir, filepath.Join("up", "video.mkv")); inFolder || err != nil {
		t.Fatalf("link up and out resolved inFolder=%v err=%v, want neither", inFolder, err)
	}
}
