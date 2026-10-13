package metadata

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// recordingImageProvider returns fixed images and records each request.
type recordingImageProvider struct {
	slug     string
	images   []RemoteImage
	err      error
	requests []ImageRequest
}

func (p *recordingImageProvider) Slug() string       { return cmp.Or(p.slug, "nfo") }
func (p *recordingImageProvider) Name() string       { return p.Slug() }
func (p *recordingImageProvider) ForTypes() []string { return []string{"series"} }
func (p *recordingImageProvider) GetImages(_ context.Context, req ImageRequest) ([]RemoteImage, error) {
	p.requests = append(p.requests, req)
	return p.images, p.err
}

const (
	localSeriesEpisodePath = "/media/Other/Show/Season 1/Show S01E01.mkv"
	localMoviePath         = "/media/Movies/Film/Film.mkv"
)

// newLocalPickerServiceForTest wires local as the sidecar discovery and remote
// as the library's whole provider chain, with one media file for each of
// "local-series-1" and "movie-1".
func newLocalPickerServiceForTest(local ImageProvider, remote Provider, roots []string) (*MetadataService, *fakeByteImageCacher) {
	if remote == nil {
		remote = &recordingImageProvider{slug: "tmdb"}
	}
	cacher := &fakeByteImageCacher{thumbhash: "th-local"}
	service := &MetadataService{chainCache: map[string]chainCacheEntry{
		"7:series": {providers: []Provider{remote}, expiresAt: time.Now().Add(time.Hour)},
		"7:movie":  {providers: []Provider{remote}, expiresAt: time.Now().Add(time.Hour)},
	}}
	service.localImageProvider = local
	files := newFakeFileRepo()
	files.contentIDs[1] = "local-series-1"
	files.contentIDs[2] = "movie-1"
	files.groupFiles["series"] = []*models.MediaFile{{ID: 1, FilePath: localSeriesEpisodePath, MediaFolderID: 7}}
	files.groupFiles["movie"] = []*models.MediaFile{{ID: 2, FilePath: localMoviePath, MediaFolderID: 7}}
	service.fileRepo = files
	service.SetImageCacher(cacher)
	service.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: roots})
	return service, cacher
}

func localApplyRequest(sourceURL string, imageType ImageType) ApplyLocalItemImageRequest {
	return ApplyLocalItemImageRequest{
		ContentID:   "local-series-1",
		ContentType: "series",
		FolderID:    7,
		ImageType:   imageType,
		SourceURL:   sourceURL,
	}
}

// An offered local image is cached under the same local/ key a refresh of
// that file would use, so a later refresh finds it already cached.
func TestApplyLocalItemImageCachesOfferedFile(t *testing.T) {
	root := t.TempDir()
	source := "file://" + writeLocalPoster(t, root)
	provider := &recordingImageProvider{images: []RemoteImage{{ProviderID: "nfo", URL: source, Type: ImagePoster}}}
	service, cacher := newLocalPickerServiceForTest(provider, nil, []string{root})

	result, err := service.ApplyLocalItemImage(context.Background(), localApplyRequest(source, ImagePoster))
	if err != nil {
		t.Fatalf("ApplyLocalItemImage: %v", err)
	}
	if len(cacher.bytesReq) != 1 {
		t.Fatalf("CacheImageBytes calls = %d, want 1", len(cacher.bytesReq))
	}
	req := cacher.bytesReq[0]
	if req.ProviderID != "local" || req.ContentType != "series" || req.ContentID != "local-series-1" || req.SourceURL != source || len(req.KeyDiscriminator) != 8 {
		t.Fatalf("cache request = %+v, want the refresh path's local key", req)
	}
	want := "local/series/local-series-1/" + req.KeyDiscriminator + "/poster/original.webp"
	if result.StoredPath != want || result.Thumbhash != "th-local" {
		t.Fatalf("result = %+v, want stored path %q", result, want)
	}
}

// The apply endpoint accepts a file:// URL from the request, so it must only
// read files the item's own discovery offered, for the type it offered them.
func TestApplyLocalItemImageRejectsFilesNotOffered(t *testing.T) {
	root := t.TempDir()
	offered := "file://" + writeLocalPoster(t, root)
	other := filepath.Join(root, "other.jpg")
	if err := os.WriteFile(other, []byte("other-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := &recordingImageProvider{images: []RemoteImage{{ProviderID: "nfo", URL: offered, Type: ImagePoster}}}
	service, cacher := newLocalPickerServiceForTest(provider, nil, []string{root})

	for name, req := range map[string]ApplyLocalItemImageRequest{
		"file not offered":     localApplyRequest("file://"+other, ImagePoster),
		"offered as poster":    localApplyRequest(offered, ImageBackdrop),
		"remote source":        localApplyRequest("https://image.example/poster.jpg", ImagePoster),
		"relative file scheme": localApplyRequest("file://../poster.jpg", ImagePoster),
	} {
		if _, err := service.ApplyLocalItemImage(context.Background(), req); !errors.Is(err, ErrLocalImageNotOffered) {
			t.Errorf("%s: err = %v, want ErrLocalImageNotOffered", name, err)
		}
	}
	if len(cacher.bytesReq) != 0 {
		t.Fatalf("cached %d images, want none", len(cacher.bytesReq))
	}
}

// Discovery offering a file does not bypass the library-root confinement.
func TestApplyLocalItemImageRejectsOfferedFileOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := "file://" + writeLocalPoster(t, t.TempDir())
	provider := &recordingImageProvider{images: []RemoteImage{{ProviderID: "nfo", URL: outside, Type: ImagePoster}}}
	service, cacher := newLocalPickerServiceForTest(provider, nil, []string{root})

	_, err := service.ApplyLocalItemImage(context.Background(), localApplyRequest(outside, ImagePoster))
	if err == nil || !strings.Contains(err.Error(), "outside library roots") {
		t.Fatalf("err = %v, want outside library roots", err)
	}
	if len(cacher.bytesReq) != 0 {
		t.Fatalf("cached %d images, want none", len(cacher.bytesReq))
	}
}

func TestLocalImagePreviewReturnsWebPDataURI(t *testing.T) {
	root := t.TempDir()
	poster := filepath.Join(root, "poster.png")
	img := image.NewNRGBA(image.Rect(0, 0, 600, 900))
	for y := range 900 {
		for x := range 600 {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poster, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	service, _ := newLocalPickerServiceForTest(&recordingImageProvider{}, nil, []string{root})

	preview, err := service.LocalImagePreview(context.Background(), "local-series-1", "file://"+poster)
	if err != nil {
		t.Fatalf("LocalImagePreview: %v", err)
	}
	encoded, ok := strings.CutPrefix(preview, "data:image/webp;base64,")
	if !ok {
		t.Fatalf("preview = %.40q..., want a WebP data URI", preview)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) < 12 || string(data[8:12]) != "WEBP" {
		t.Fatalf("preview is not WebP (err %v)", err)
	}

	if _, err := service.LocalImagePreview(context.Background(), "local-series-1", "file://"+writeLocalPoster(t, t.TempDir())); err == nil {
		t.Fatal("preview of a file outside the library roots succeeded")
	}
}

// Local choices come only from sidecar discovery, which gets the item's file
// context even when the library's chain does not use the NFO provider. A
// file:// URL from a chain provider is neither listed nor applyable.
func TestFetchItemImagesWithLocalTrustsOnlySidecarDiscovery(t *testing.T) {
	root := t.TempDir()
	poster := "file://" + writeLocalPoster(t, root)
	elsewhere := filepath.Join(root, "elsewhere.jpg")
	if err := os.WriteFile(elsewhere, []byte("elsewhere-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := &recordingImageProvider{images: []RemoteImage{{ProviderID: "nfo", URL: poster, Type: ImagePoster}}}
	remote := &recordingImageProvider{slug: "tmdb", images: []RemoteImage{
		{ProviderID: "tmdb", URL: "https://image.example/poster.jpg", Type: ImagePoster, Rating: 7},
		{ProviderID: "tmdb", URL: "file://" + elsewhere, Type: ImagePoster, Rating: 9},
	}}
	service, cacher := newLocalPickerServiceForTest(local, remote, []string{root})

	images, _, err := service.FetchItemImagesWithLocal(context.Background(), map[string]string{"tmdb": "1"}, "series", "en", 7, "local-series-1")
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, image := range images {
		urls = append(urls, image.URL)
	}
	if want := []string{"https://image.example/poster.jpg", poster}; !slices.Equal(urls, want) {
		t.Fatalf("images = %v, want %v", urls, want)
	}
	if len(local.requests) != 1 || local.requests[0].RepresentativeFilePath != localSeriesEpisodePath {
		t.Fatalf("sidecar discovery requests = %+v, want the item's media file", local.requests)
	}
	if len(remote.requests) != 1 || remote.requests[0].RepresentativeFilePath != "" || len(remote.requests[0].PrimarySidecarSearchPaths) != 0 {
		t.Fatalf("chain requests = %+v, want no local context", remote.requests)
	}

	_, err = service.ApplyLocalItemImage(context.Background(), localApplyRequest("file://"+elsewhere, ImagePoster))
	if !errors.Is(err, ErrLocalImageNotOffered) || len(cacher.bytesReq) != 0 {
		t.Fatalf("apply of a chain provider's file = %v (cached %d), want ErrLocalImageNotOffered", err, len(cacher.bytesReq))
	}
}

// Movies use the plural content type in their cache key, like the refresh
// path's enqueueItemImages does.
func TestApplyLocalItemImageUsesTheRefreshKeyForMovies(t *testing.T) {
	root := t.TempDir()
	source := "file://" + writeLocalPoster(t, root)
	provider := &recordingImageProvider{images: []RemoteImage{{ProviderID: "nfo", URL: source, Type: ImagePoster}}}
	service, cacher := newLocalPickerServiceForTest(provider, nil, []string{root})
	req := localApplyRequest(source, ImagePoster)
	req.ContentID, req.ContentType = "movie-1", "movie"

	result, err := service.ApplyLocalItemImage(context.Background(), req)
	if err != nil {
		t.Fatalf("ApplyLocalItemImage: %v", err)
	}
	if len(provider.requests) != 1 || provider.requests[0].RepresentativeFilePath != localMoviePath {
		t.Fatalf("sidecar discovery requests = %+v, want the movie's file", provider.requests)
	}
	cacheReq := cacher.bytesReq[0]
	want := "local/movies/movie-1/" + cacheReq.KeyDiscriminator + "/poster/original.webp"
	if cacheReq.ContentType != "movies" || result.StoredPath != want {
		t.Fatalf("content type %q, stored path %q, want movies and %q", cacheReq.ContentType, result.StoredPath, want)
	}
}

// A directory symlink inside the library that points outside it cannot pull
// an outside file in, even when discovery offers the linked path.
func TestApplyLocalItemImageRejectsSymlinkedDirectoryEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeLocalPoster(t, outside)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	source := "file://" + filepath.Join(root, "linked", "poster.jpg")
	provider := &recordingImageProvider{images: []RemoteImage{{ProviderID: "nfo", URL: source, Type: ImagePoster}}}
	service, cacher := newLocalPickerServiceForTest(provider, nil, []string{root})

	_, err := service.ApplyLocalItemImage(context.Background(), localApplyRequest(source, ImagePoster))
	if err == nil || !strings.Contains(err.Error(), "outside library roots") {
		t.Fatalf("err = %v, want outside library roots", err)
	}
	if len(cacher.bytesReq) != 0 {
		t.Fatalf("cached %d images, want none", len(cacher.bytesReq))
	}
}

type countingObservedLocationRepo struct {
	gets int
}

func (r *countingObservedLocationRepo) Get(context.Context, int, string) (*models.ObservedMediaLocation, error) {
	r.gets++
	return &models.ObservedMediaLocation{ContentGroupCount: 1}, nil
}

// The picker lists a series' local artwork on every page and again on apply,
// so the sidecar directory check runs once per distinct observed root rather
// than once per episode file.
func TestDirectorySidecarSearchPathsChecksEachRootOnce(t *testing.T) {
	locations := &countingObservedLocationRepo{}
	service := &MetadataService{observedLocationRepo: locations}
	files := make([]*models.MediaFile, 0, 301)
	for i := range 300 {
		files = append(files, &models.MediaFile{ID: i + 1, MediaFolderID: 7, ObservedRootPath: "/media/Other/Show", ContentGroupKey: "show"})
	}
	files = append(files, &models.MediaFile{ID: 301, MediaFolderID: 7, ObservedRootPath: "/media/Other/Show Extras", ContentGroupKey: "show"})

	paths := service.directorySidecarSearchPathsForFiles(context.Background(), files)
	if locations.gets != 2 {
		t.Fatalf("observed location lookups = %d, want one per distinct root (2)", locations.gets)
	}
	if len(paths) != 2 || paths[0] != "/media/Other/Show" || paths[1] != "/media/Other/Show Extras" {
		t.Fatalf("paths = %v", paths)
	}
}

// Sidecar discovery does not depend on the provider chain, so a chain that
// fails to resolve still lists the item's local artwork and reports the
// failure instead of failing the whole listing.
func TestFetchItemImagesWithLocalSurvivesChainFailure(t *testing.T) {
	root := t.TempDir()
	poster := "file://" + writeLocalPoster(t, root)
	local := &recordingImageProvider{images: []RemoteImage{{ProviderID: "nfo", URL: poster, Type: ImagePoster}}}
	service, _ := newLocalPickerServiceForTest(local, nil, []string{root})
	pool, err := pgxpool.New(context.Background(), "postgres://silo@127.0.0.1:1/silo?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	service.chainRepo = NewChainRepository(pool)
	service.chainCache = map[string]chainCacheEntry{}

	images, providerErrors, err := service.FetchItemImagesWithLocal(context.Background(), nil, "series", "en", 7, "local-series-1")
	if err != nil {
		t.Fatalf("FetchItemImagesWithLocal: %v", err)
	}
	if len(images) != 1 || images[0].URL != poster {
		t.Fatalf("images = %+v, want only the local poster", images)
	}
	if got := providerErrors["chain"]; got != "Image provider chain unavailable" {
		t.Fatalf("provider errors = %v, want the fixed chain failure message", providerErrors)
	}
}

// A discovery failure is reported by source only. Its error text can carry
// paths or provider details, so it stays in the log and out of the listing.
func TestFetchItemImagesWithLocalReportsDiscoveryFailureWithoutItsText(t *testing.T) {
	local := &recordingImageProvider{err: errors.New("nfo read failed: desc = api_key=secret-value /media/Other/Show/tvshow.nfo")}
	service, _ := newLocalPickerServiceForTest(local, nil, []string{t.TempDir()})

	_, providerErrors, err := service.FetchItemImagesWithLocal(context.Background(), map[string]string{"tmdb": "1"}, "series", "en", 7, "local-series-1")
	if err != nil {
		t.Fatalf("FetchItemImagesWithLocal: %v", err)
	}
	got := providerErrors[imageCacheLocalProviderID]
	if got != "Local artwork discovery failed" {
		t.Fatalf("provider errors = %v, want the fixed local failure message", providerErrors)
	}
	if strings.Contains(got, "api_key") || strings.Contains(got, "/media/") {
		t.Fatalf("local failure message %q leaks the underlying error", got)
	}
}

// The discovery failure is still logged for the admin, with credential
// assignments masked, including one nested inside another assignment.
func TestFetchItemImagesWithLocalLogsDiscoveryFailureWithoutSecrets(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	local := &recordingImageProvider{err: errors.New("nfo read failed: desc = api_key=secret-value")}
	service, _ := newLocalPickerServiceForTest(local, nil, []string{t.TempDir()})
	if _, _, err := service.FetchItemImagesWithLocal(context.Background(), map[string]string{"tmdb": "1"}, "series", "en", 7, "local-series-1"); err != nil {
		t.Fatalf("FetchItemImagesWithLocal: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "local artwork discovery failed") || !strings.Contains(out, "nfo read failed") {
		t.Fatalf("log = %q, want the discovery failure with its context", out)
	}
	if strings.Contains(out, "secret-value") {
		t.Fatalf("log = %q, leaks the api_key value", out)
	}
}
