package handlers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"

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
		recorder.client(),
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
		recorder.client(),
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
