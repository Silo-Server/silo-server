package metadata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeLocalImageCacheJobs is a fakeImageCacheJobs that also reports the
// target's currently stored cached path, like the real repository.
type fakeLocalImageCacheJobs struct {
	fakeImageCacheJobs
	currentCached string
}

func (f *fakeLocalImageCacheJobs) CurrentTargetCachedPath(context.Context, *models.MetadataImageCacheJob) (string, error) {
	return f.currentCached, nil
}

// fakeByteImageCacher supports both remote and byte caching. CacheImageBytes
// builds the base path the way the real cacher does so key assertions hold.
type fakeByteImageCacher struct {
	fakeImageCacher
	byteReqs  [][]byte
	bytesReq  []CacheImageRequest
	thumbhash string
}

func (f *fakeByteImageCacher) CacheImageBytes(_ context.Context, data []byte, req CacheImageRequest) (*CacheImageResult, error) {
	f.byteReqs = append(f.byteReqs, data)
	f.bytesReq = append(f.bytesReq, req)
	if f.err != nil {
		return nil, f.err
	}
	return &CacheImageResult{
		BasePath:  fmt.Sprintf("%s/%s/%s/%s/%s", req.ProviderID, req.ContentType, req.ContentID, req.KeyDiscriminator, ImageTypeToString(req.ImageType)),
		Thumbhash: f.thumbhash,
		Ext:       ".webp",
	}, nil
}

type fakeLibraryRootResolver struct {
	roots []string
	err   error
}

func (f *fakeLibraryRootResolver) LibraryRootsForContent(context.Context, string) ([]string, error) {
	return f.roots, f.err
}

type fakePrefixDeleter struct {
	bucket   string
	prefixes []string
}

func (f *fakePrefixDeleter) DeletePrefix(_ context.Context, _ string, prefix string) (int, error) {
	f.prefixes = append(f.prefixes, prefix)
	return 1, nil
}

func (f *fakePrefixDeleter) Bucket() string { return f.bucket }

func localArtworkJob(sourcePath string) *models.MetadataImageCacheJob {
	return &models.MetadataImageCacheJob{
		ID:                7,
		TargetType:        ImageCacheTargetItem,
		TargetContentID:   "movie-1",
		SeriesID:          "movie-1",
		SourcePath:        sourcePath,
		ProviderID:        "local",
		ProviderContentID: "movie-1",
		ContentType:       "movies",
		ImageType:         ImageCacheImagePoster,
	}
}

func newLocalProcessorForTest(
	job *models.MetadataImageCacheJob,
	roots []string,
	currentCached string,
) (*ImageCacheProcessor, *fakeLocalImageCacheJobs, *fakeByteImageCacher, *fakeItemArtworkUpdater, *fakePrefixDeleter) {
	jobs := &fakeLocalImageCacheJobs{
		fakeImageCacheJobs: fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{job}},
		currentCached:      currentCached,
	}
	cacher := &fakeByteImageCacher{thumbhash: "th-new"}
	items := &fakeItemArtworkUpdater{updated: true}
	deleter := &fakePrefixDeleter{bucket: "images"}
	p := NewImageCacheProcessorWithTargets(jobs, cacher, nil, ImageCacheProcessorTargets{Items: items})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: roots})
	p.SetImagePrefixDeleter(deleter)
	return p, jobs, cacher, items, deleter
}

func writeLocalPoster(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "poster.jpg")
	if err := os.WriteFile(path, []byte("poster-bytes"), 0o644); err != nil {
		t.Fatalf("writing poster: %v", err)
	}
	return path
}

func TestProcessLocalImageCachesWithinLibraryRoots(t *testing.T) {
	root := t.TempDir()
	posterPath := writeLocalPoster(t, root)
	job := localArtworkJob("file://" + posterPath)
	p, jobs, cacher, items, deleter := newLocalProcessorForTest(job, []string{root}, "")

	stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.Succeeded != 1 {
		t.Fatalf("stats = %+v, want 1 succeeded (failed text %q)", stats, jobs.failedText)
	}
	if len(cacher.bytesReq) != 1 {
		t.Fatalf("CacheImageBytes calls = %d, want 1", len(cacher.bytesReq))
	}
	req := cacher.bytesReq[0]
	if req.ProviderID != "local" || req.ContentType != "movies" || req.ContentID != "movie-1" {
		t.Fatalf("cache request = %+v", req)
	}
	if len(req.KeyDiscriminator) != 8 {
		t.Fatalf("KeyDiscriminator = %q, want 8-char content hash", req.KeyDiscriminator)
	}
	wantCached := "local/movies/movie-1/" + req.KeyDiscriminator + "/poster/original.webp"
	if items.cachedPath != wantCached {
		t.Fatalf("cachedPath = %q, want %q", items.cachedPath, wantCached)
	}
	if items.thumbhash != "th-new" {
		t.Fatalf("thumbhash = %q", items.thumbhash)
	}
	if len(deleter.prefixes) != 0 {
		t.Fatalf("no stale prefix to delete on first cache, got %v", deleter.prefixes)
	}
}

func TestProcessLocalImageRejectsPathOutsideRoots(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	posterPath := writeLocalPoster(t, other)
	job := localArtworkJob("file://" + posterPath)
	p, jobs, cacher, _, _ := newLocalProcessorForTest(job, []string{root}, "")

	stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("stats = %+v, want 1 failed", stats)
	}
	if !isStableProviderImageFailure(jobs.failedText) {
		t.Fatalf("confinement failure %q must be a stable failure", jobs.failedText)
	}
	if len(cacher.bytesReq) != 0 {
		t.Fatal("must not read or cache a file outside library roots")
	}
}

func TestProcessLocalImageRejectsDotDotTraversal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	posterPath := writeLocalPoster(t, outside)
	traversal := root + "/../" + filepath.Base(outside) + "/" + filepath.Base(posterPath)
	job := localArtworkJob("file://" + traversal)
	p, jobs, cacher, _, _ := newLocalProcessorForTest(job, []string{root}, "")

	stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("stats = %+v, want 1 failed", stats)
	}
	if !isStableProviderImageFailure(jobs.failedText) {
		t.Fatalf("traversal failure %q must be a stable failure", jobs.failedText)
	}
	if len(cacher.bytesReq) != 0 {
		t.Fatal("must not cache a traversal path")
	}
}

func TestProcessLocalImageAcceptsLogicalPathUnderSymlinkedRoot(t *testing.T) {
	// The scanner records logical paths under symlinked roots; confinement is
	// deliberately lexical-on-logical (no EvalSymlinks).
	realRoot := t.TempDir()
	writeLocalPoster(t, realRoot)
	linkParent := t.TempDir()
	linkedRoot := filepath.Join(linkParent, "library")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	job := localArtworkJob("file://" + filepath.Join(linkedRoot, "poster.jpg"))
	p, jobs, cacher, _, _ := newLocalProcessorForTest(job, []string{linkedRoot}, "")

	stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.Succeeded != 1 {
		t.Fatalf("stats = %+v (failed text %q), want success under symlinked root", stats, jobs.failedText)
	}
	if len(cacher.bytesReq) != 1 {
		t.Fatal("expected the logical path to be read and cached")
	}
}

func TestProcessLocalImageMissingFileIsStableFailure(t *testing.T) {
	root := t.TempDir()
	job := localArtworkJob("file://" + filepath.Join(root, "poster.jpg"))
	p, jobs, _, _, _ := newLocalProcessorForTest(job, []string{root}, "")

	stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("stats = %+v, want 1 failed", stats)
	}
	if !isStableProviderImageFailure(jobs.failedText) {
		t.Fatalf("ENOENT failure %q must be a stable failure", jobs.failedText)
	}
}

func TestProcessLocalImageRejectsSymlinkedLeaf(t *testing.T) {
	root := t.TempDir()
	target := writeLocalPoster(t, root)
	link := filepath.Join(root, "linked.jpg")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	job := localArtworkJob("file://" + link)
	p, _, cacher, _, _ := newLocalProcessorForTest(job, []string{root}, "")

	stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("stats = %+v, want 1 failed", stats)
	}
	if len(cacher.bytesReq) != 0 {
		t.Fatal("must not cache a symlinked leaf")
	}
}

func TestProcessLocalImageDeletesStalePrefixOnRecache(t *testing.T) {
	root := t.TempDir()
	posterPath := writeLocalPoster(t, root)
	job := localArtworkJob("file://" + posterPath)
	stale := "local/movies/movie-1/00000000/poster/original.webp"
	p, _, cacher, _, deleter := newLocalProcessorForTest(job, []string{root}, stale)

	stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.Succeeded != 1 {
		t.Fatalf("stats = %+v, want success", stats)
	}
	if len(cacher.bytesReq) != 1 {
		t.Fatal("expected one cache call")
	}
	if len(deleter.prefixes) != 1 || deleter.prefixes[0] != "local/movies/movie-1/00000000/poster/" {
		t.Fatalf("stale prefixes deleted = %v", deleter.prefixes)
	}
}

func TestProcessLocalImageSkipsPrefixDeleteWhenUnchanged(t *testing.T) {
	root := t.TempDir()
	posterPath := writeLocalPoster(t, root)
	job := localArtworkJob("file://" + posterPath)

	// First pass discovers the hash for these bytes.
	probe, _, probeCacher, probeItems, _ := newLocalProcessorForTest(job, []string{root}, "")
	if _, err := probe.RunOnce(context.Background(), "w1", 10, 1); err != nil {
		t.Fatalf("probe RunOnce: %v", err)
	}
	if len(probeCacher.bytesReq) != 1 {
		t.Fatal("probe cache call missing")
	}

	// Second pass with the same stored cached path must not delete anything.
	p, _, _, _, deleter := newLocalProcessorForTest(job, []string{root}, probeItems.cachedPath)
	stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.Succeeded != 1 {
		t.Fatalf("stats = %+v, want success", stats)
	}
	if len(deleter.prefixes) != 0 {
		t.Fatalf("unchanged art must not delete prefixes, got %v", deleter.prefixes)
	}
}

func TestProcessLocalImageFailsWithoutRootResolver(t *testing.T) {
	root := t.TempDir()
	posterPath := writeLocalPoster(t, root)
	job := localArtworkJob("file://" + posterPath)
	jobs := &fakeLocalImageCacheJobs{
		fakeImageCacheJobs: fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{job}},
	}
	cacher := &fakeByteImageCacher{thumbhash: "th"}
	p := NewImageCacheProcessorWithTargets(jobs, cacher, nil, ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})

	stats, err := p.RunOnce(context.Background(), "w1", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("stats = %+v, want failure without a library root resolver", stats)
	}
	if len(cacher.bytesReq) != 0 {
		t.Fatal("must not cache without confinement roots")
	}
}
