package metadata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The server's root resolver must list catalogued files, or symlinked title
// folders quietly lose their artwork again.
var _ cataloguedFileLister = (*catalog.FolderRepository)(nil)

// cataloguedRootResolver is a library root resolver that also lists
// catalogued files, as *catalog.FolderRepository does.
type cataloguedRootResolver struct {
	roots []string
	files []string
	err   error
	asked []string
}

func (r *cataloguedRootResolver) LibraryRootsForContent(context.Context, string) ([]string, error) {
	return r.roots, nil
}

func (r *cataloguedRootResolver) CataloguedFilesUnder(_ context.Context, contentID, dir string, limit int) ([]string, error) {
	r.asked = append(r.asked, contentID)
	if r.err != nil {
		return nil, r.err
	}
	var out []string
	for _, file := range r.files {
		if strings.HasPrefix(file, dir+string(filepath.Separator)) && len(out) < limit {
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
// scanner catalogued through it, has its artwork cached, including artwork in
// a subfolder. Without the catalogued file the same layout is refused.
func TestProcessLocalImageAcceptsSymlinkedTitleFolderWithCataloguedVideo(t *testing.T) {
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
			resolver := &cataloguedRootResolver{roots: []string{library}, files: []string{video}}
			jobs, cacher := runLocalJob(t, job, resolver)
			if len(cacher.byteReqs) != 1 || string(cacher.byteReqs[0]) != tc.want {
				t.Fatalf("cached %d images (failed text %q), want %q", len(cacher.byteReqs), jobs.failedText, tc.want)
			}
			if len(resolver.asked) == 0 || resolver.asked[0] != "movie-1" {
				t.Fatalf("catalogued files asked for %v, want the job's title", resolver.asked)
			}
		})
	}

	t.Run("no catalogued file", func(t *testing.T) {
		job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
		jobs, cacher := runLocalJob(t, job, &cataloguedRootResolver{roots: []string{library}})
		if len(cacher.byteReqs) != 0 || !strings.Contains(jobs.failedText, "local image path outside library roots") {
			t.Fatalf("cached %d images, failed text %q; want the outside-roots refusal", len(cacher.byteReqs), jobs.failedText)
		}
		if !isStableProviderImageFailure(jobs.failedText) {
			t.Fatalf("refusal %q must stay a stable failure", jobs.failedText)
		}
	})

	t.Run("listing fails", func(t *testing.T) {
		job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
		resolver := &cataloguedRootResolver{roots: []string{library}, err: errors.New("database unavailable")}
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
func TestProcessLocalImageAcceptsSymlinkedSeriesFolderWithCataloguedEpisode(t *testing.T) {
	library, title := symlinkedTitle(t, map[string]string{
		"Season 01/Example S01E01.mkv": "video",
		"poster.jpg":                   "series-poster",
	})
	job := localArtworkJob("file://" + filepath.Join(title, "poster.jpg"))
	job.TargetType = ImageCacheTargetItem
	job.TargetContentID = "series-1"
	job.SeriesID = "series-1"
	resolver := &cataloguedRootResolver{roots: []string{library}, files: []string{filepath.Join(title, "Season 01", "Example S01E01.mkv")}}
	jobs, cacher := runLocalJob(t, job, resolver)
	if len(cacher.byteReqs) != 1 || string(cacher.byteReqs[0]) != "series-poster" {
		t.Fatalf("cached %d images (failed text %q), want the series poster", len(cacher.byteReqs), jobs.failedText)
	}
}

// A symlink planted in the library that points somewhere without a catalogued
// file of the title stays refused, even when the title has catalogued files
// elsewhere in the library.
func TestProcessLocalImageRefusesPlantedSymlinkWithoutCataloguedFile(t *testing.T) {
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
	jobs, cacher := runLocalJob(t, job, &cataloguedRootResolver{roots: []string{library}, files: []string{video}})
	if len(cacher.byteReqs) != 0 || !strings.Contains(jobs.failedText, "local image path outside library roots") {
		t.Fatalf("cached %d images, failed text %q; want the outside-roots refusal", len(cacher.byteReqs), jobs.failedText)
	}
}

// A catalogued path that is itself a symlink out of the title folder proves
// nothing about the folder: the check goes through the opened folder, which
// will not follow it out.
func TestProcessLocalImageRefusesCataloguedFileLinkedOutOfTheFolder(t *testing.T) {
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
	resolver := &cataloguedRootResolver{roots: []string{library}, files: []string{filepath.Join(title, "Example (2020).mkv")}}
	jobs, cacher := runLocalJob(t, job, resolver)
	if len(cacher.byteReqs) != 0 || !strings.Contains(jobs.failedText, "local image path outside library roots") {
		t.Fatalf("cached %d images, failed text %q; want the outside-roots refusal", len(cacher.byteReqs), jobs.failedText)
	}
}
