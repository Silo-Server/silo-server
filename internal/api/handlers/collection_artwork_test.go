package handlers

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type collectionArtworkS3Recorder struct {
	server *httptest.Server
	mu     sync.Mutex
	puts   []string
}

func newCollectionArtworkS3Recorder(t *testing.T) *collectionArtworkS3Recorder {
	t.Helper()

	recorder := &collectionArtworkS3Recorder{}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		if r.Method == http.MethodPut {
			recorder.mu.Lock()
			recorder.puts = append(recorder.puts, r.URL.Path)
			recorder.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(recorder.server.Close)
	return recorder
}

func (r *collectionArtworkS3Recorder) client() *s3client.Client {
	return s3client.NewClient(s3client.BucketConfig{
		Endpoint:  r.server.URL,
		Region:    "us-east-1",
		Bucket:    "public-assets",
		AccessKey: "test",
		SecretKey: "test",
		PathStyle: true,
	})
}

func (r *collectionArtworkS3Recorder) putPaths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.puts))
	copy(out, r.puts)
	return out
}

func TestStoreBundledCollectionPosterIfS3Configured_NoS3KeepsPath(t *testing.T) {
	path := "/images/collection-templates/template.jpg"
	gotPath, gotThumbhash, stored, err := storeBundledCollectionPosterIfS3Configured(
		context.Background(),
		nil,
		fstest.MapFS{},
		"collection-1",
		adminCollectionImagePrefix,
		path,
	)
	if err != nil {
		t.Fatalf("storeBundledCollectionPosterIfS3Configured: %v", err)
	}
	if stored {
		t.Fatal("stored = true, want false")
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	if gotThumbhash != "" {
		t.Fatalf("thumbhash = %q, want empty", gotThumbhash)
	}
}

func TestStoreBundledCollectionPosterIfS3Configured_IgnoresNonTemplatePath(t *testing.T) {
	recorder := newCollectionArtworkS3Recorder(t)
	path := "collection-images/existing/poster/original.webp"

	gotPath, gotThumbhash, stored, err := storeBundledCollectionPosterIfS3Configured(
		context.Background(),
		blobstore.NewS3(recorder.client()),
		fstest.MapFS{},
		"collection-1",
		adminCollectionImagePrefix,
		path,
	)
	if err != nil {
		t.Fatalf("storeBundledCollectionPosterIfS3Configured: %v", err)
	}
	if stored {
		t.Fatal("stored = true, want false")
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	if gotThumbhash != "" {
		t.Fatalf("thumbhash = %q, want empty", gotThumbhash)
	}
	if puts := recorder.putPaths(); len(puts) != 0 {
		t.Fatalf("PUT paths = %#v, want none", puts)
	}
}

func TestStoreBundledCollectionPosterIfS3Configured_UploadsTemplatePoster(t *testing.T) {
	recorder := newCollectionArtworkS3Recorder(t)
	frontendFS := fstest.MapFS{
		"images/collection-templates/template.jpg": {
			Data: testCollectionPosterJPEG(t),
		},
	}

	gotPath, gotThumbhash, stored, err := storeBundledCollectionPosterIfS3Configured(
		context.Background(),
		blobstore.NewS3(recorder.client()),
		frontendFS,
		"collection-1",
		adminCollectionImagePrefix,
		"/images/collection-templates/template.jpg",
	)
	if err != nil {
		t.Fatalf("storeBundledCollectionPosterIfS3Configured: %v", err)
	}
	if !stored {
		t.Fatal("stored = false, want true")
	}
	if gotPath != "collection-images/collection-1/poster/original.webp" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotThumbhash == "" {
		t.Fatal("thumbhash is empty")
	}

	want := map[string]bool{
		"/public-assets/collection-images/collection-1/poster/original.webp": true,
		"/public-assets/collection-images/collection-1/poster/w500.webp":     true,
		"/public-assets/collection-images/collection-1/poster/w300.webp":     true,
	}
	puts := recorder.putPaths()
	if len(puts) != len(want) {
		t.Fatalf("PUT paths = %#v", puts)
	}
	for _, path := range puts {
		if !want[path] {
			t.Fatalf("unexpected PUT path %q in %#v", path, puts)
		}
	}
}

func testCollectionPosterJPEG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 32, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 6), G: uint8(y * 4), B: 120, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestUploadCollectionImageVariants_RejectsNonImage(t *testing.T) {
	recorder := newCollectionArtworkS3Recorder(t)

	_, _, err := uploadCollectionImageVariants(
		context.Background(),
		blobstore.NewS3(recorder.client()),
		adminCollectionImagePrefix,
		"collection-1",
		"poster",
		[]byte(`<?xml version="1.0"?><root/>`),
	)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want a 400 APIError", err)
	}
	if puts := recorder.putPaths(); len(puts) != 0 {
		t.Fatalf("PUT paths = %#v, want none", puts)
	}
}

func TestDownloadCollectionImageURL_ClientErrorsAre400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	for name, rawURL := range map[string]string{
		"missing source": server.URL + "/poster.jpg",
		"non-http":       "ftp://example.invalid/poster.jpg",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := downloadCollectionImageURL(context.Background(), server.Client(), rawURL)
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Status != http.StatusBadRequest {
				t.Fatalf("err = %v, want a 400 APIError", err)
			}
		})
	}
}

func TestProcessCollectionPoster_InvalidImageKeepsExistingPoster(t *testing.T) {
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	existing := userCollectionImagePrefix + "/collection-1/poster/original.webp"
	if err := store.Put(context.Background(), existing, []byte("current poster")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h := &CollectionHandler{ArtworkStore: store}
	_, err = h.processCollectionPoster(
		context.Background(),
		nil,
		"collection-1",
		"profile-1",
		func() ([]byte, error) { return []byte("not an image"), nil },
		"",
	)
	mapped := collectionArtworkError(err, "Failed to store collection artwork")
	if apiErr, ok := errors.AsType[*APIError](mapped); !ok || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("mapped err = %v, want a 400 APIError", mapped)
	}
	items, _, err := store.List(context.Background(), userCollectionImagePrefix+"/collection-1/poster/", "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Key != existing {
		t.Fatalf("stored poster objects = %#v, want only %q", items, existing)
	}
}

func TestCollectionArtworkError_HidesServerFailures(t *testing.T) {
	err := collectionArtworkError(errors.New("uploading original: connection reset"), "Failed to store collection artwork")
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusInternalServerError || apiErr.Message != "Failed to store collection artwork" {
		t.Fatalf("err = %#v, want the 500 fallback", err)
	}
}

// failingPutStore rejects every Put so a test can fail storage after decoding.
type failingPutStore struct{ blobstore.Store }

func (failingPutStore) Put(context.Context, string, []byte) error {
	return errors.New("storage unavailable")
}

func TestProcessCollectionPoster_StorageFailureKeepsExistingPoster(t *testing.T) {
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	existing := userCollectionImagePrefix + "/collection-1/poster/original.webp"
	if err := store.Put(context.Background(), existing, []byte("current poster")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h := &CollectionHandler{ArtworkStore: failingPutStore{store}}
	_, err = h.processCollectionPoster(
		context.Background(),
		nil,
		"collection-1",
		"profile-1",
		func() ([]byte, error) { return testCollectionPosterJPEG(t), nil },
		"",
	)
	if err == nil {
		t.Fatal("processCollectionPoster succeeded, want the storage error")
	}
	items, _, err := store.List(context.Background(), userCollectionImagePrefix+"/collection-1/poster/", "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Key != existing {
		t.Fatalf("stored poster objects = %#v, want only %q", items, existing)
	}
}

func TestPruneCollectionImageVariants_RemovesOnlyStaleObjects(t *testing.T) {
	ctx := context.Background()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	variants, err := generateCollectionImageVariants("poster", testCollectionPosterJPEG(t))
	if err != nil {
		t.Fatalf("generateCollectionImageVariants: %v", err)
	}
	if _, _, err := storeCollectionImageVariants(ctx, store, userCollectionImagePrefix, "collection-1", "poster", variants); err != nil {
		t.Fatalf("storeCollectionImageVariants: %v", err)
	}
	stale := userCollectionImagePrefix + "/collection-1/poster/original.jpg"
	other := userCollectionImagePrefix + "/collection-1/backdrop/original.jpg"
	for _, key := range []string{stale, other} {
		if err := store.Put(ctx, key, []byte("old")); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}

	if err := pruneCollectionImageVariants(ctx, store, userCollectionImagePrefix, "collection-1", "poster", variants); err != nil {
		t.Fatalf("pruneCollectionImageVariants: %v", err)
	}
	items, _, err := store.List(ctx, userCollectionImagePrefix+"/collection-1/", "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make(map[string]bool, len(items))
	for _, item := range items {
		got[item.Key] = true
	}
	if got[stale] || !got[other] || len(got) != len(variants.Variants)+1 {
		t.Fatalf("objects after prune = %v", got)
	}
}
