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

func runLocalJob(t *testing.T, job *models.MetadataImageCacheJob, resolver LibraryRootResolver) (*fakeLocalImageCacheJobs, *fakeByteImageCacher) {
	t.Helper()
	jobs := &fakeLocalImageCacheJobs{fakeImageCacheJobs: fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{job}}}
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
	if localFileIOError(&fs.PathError{Op: "stat", Path: "x", Err: syscall.ENOENT}) || localFileIOError(&fs.PathError{Op: "stat", Path: "x", Err: syscall.EACCES}) {
		t.Fatal("a missing or forbidden path is not a storage failure")
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
// folder, absolute or relative, still vouches for the folder; the link is
// read, not followed, so a target outside it is never touched.
func TestProcessLocalImageAcceptsCatalogedVideoLinkedInsideTheFolder(t *testing.T) {
	for _, absolute := range []bool{true, false} {
		t.Run(fmt.Sprintf("absolute=%v", absolute), func(t *testing.T) {
			library, title := symlinkedTitle(t, map[string]string{
				"payload/Example (2020).mkv": "video",
				"poster.jpg":                 "poster-bytes",
			})
			store, err := filepath.EvalSymlinks(title)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join("payload", "Example (2020).mkv")
			if absolute {
				target = filepath.Join(store, target)
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
}
