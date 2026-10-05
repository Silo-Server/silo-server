package metadata

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// recordingImageProvider returns fixed images and records each request.
type recordingImageProvider struct {
	images   []RemoteImage
	requests []ImageRequest
}

func (p *recordingImageProvider) Slug() string       { return "nfo" }
func (p *recordingImageProvider) Name() string       { return "nfo" }
func (p *recordingImageProvider) ForTypes() []string { return []string{"series"} }
func (p *recordingImageProvider) GetImages(_ context.Context, req ImageRequest) ([]RemoteImage, error) {
	p.requests = append(p.requests, req)
	return p.images, nil
}

func newLocalPickerServiceForTest(provider Provider, roots []string) (*MetadataService, *fakeByteImageCacher) {
	cacher := &fakeByteImageCacher{thumbhash: "th-local"}
	service := &MetadataService{chainCache: map[string]chainCacheEntry{
		"7:series": {providers: []Provider{provider}, expiresAt: time.Now().Add(time.Hour)},
		"7:movie":  {providers: []Provider{provider}, expiresAt: time.Now().Add(time.Hour)},
	}}
	service.SetImageCacher(cacher)
	service.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: roots})
	return service, cacher
}

func localApplyRequest(sourceURL string, imageType ImageType) ApplyLocalItemImageRequest {
	return ApplyLocalItemImageRequest{
		ContentID:   "local-series-1",
		ContentType: "series",
		Language:    "en",
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
	service, cacher := newLocalPickerServiceForTest(provider, []string{root})

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
	service, cacher := newLocalPickerServiceForTest(provider, []string{root})

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
	service, cacher := newLocalPickerServiceForTest(provider, []string{root})

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
	service, _ := newLocalPickerServiceForTest(&recordingImageProvider{}, []string{root})

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

// The local listing passes the item's file context to providers; the plain
// provider listing keeps sending none.
func TestFetchItemImagesWithLocalPassesItemContext(t *testing.T) {
	provider := &recordingImageProvider{}
	service, _ := newLocalPickerServiceForTest(provider, nil)
	files := newFakeFileRepo()
	files.contentIDs[1] = "local-series-1"
	files.groupFiles["show"] = []*models.MediaFile{
		{ID: 1, FilePath: "/media/Other/Show/Season 1/Show S01E01.mkv", MediaFolderID: 7},
	}
	service.fileRepo = files

	if _, _, err := service.FetchItemImages(context.Background(), nil, "series", "en", 7); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.FetchItemImagesWithLocal(context.Background(), nil, "series", "en", 7, "local-series-1"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(provider.requests))
	}
	if plain := provider.requests[0]; plain.RepresentativeFilePath != "" || len(plain.AllGroupFilePaths) != 0 {
		t.Fatalf("FetchItemImages sent local context: %+v", plain)
	}
	if local := provider.requests[1]; local.RepresentativeFilePath != "/media/Other/Show/Season 1/Show S01E01.mkv" {
		t.Fatalf("FetchItemImagesWithLocal request = %+v, want the item's media file", local)
	}
}

// Movies use the plural content type in their cache key, like the refresh
// path's enqueueItemImages does.
func TestApplyLocalItemImageUsesTheRefreshKeyForMovies(t *testing.T) {
	root := t.TempDir()
	source := "file://" + writeLocalPoster(t, root)
	provider := &recordingImageProvider{images: []RemoteImage{{ProviderID: "nfo", URL: source, Type: ImagePoster}}}
	service, cacher := newLocalPickerServiceForTest(provider, []string{root})
	req := localApplyRequest(source, ImagePoster)
	req.ContentID, req.ContentType = "movie-1", "movie"

	result, err := service.ApplyLocalItemImage(context.Background(), req)
	if err != nil {
		t.Fatalf("ApplyLocalItemImage: %v", err)
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
	service, cacher := newLocalPickerServiceForTest(provider, []string{root})

	_, err := service.ApplyLocalItemImage(context.Background(), localApplyRequest(source, ImagePoster))
	if err == nil || !strings.Contains(err.Error(), "outside library roots") {
		t.Fatalf("err = %v, want outside library roots", err)
	}
	if len(cacher.bytesReq) != 0 {
		t.Fatalf("cached %d images, want none", len(cacher.bytesReq))
	}
}
