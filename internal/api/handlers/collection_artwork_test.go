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
	"strings"
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
	// Keys are content-addressed by the source bytes (issue #1258), so the
	// stored path carries the version segment for the template's own bytes.
	version := collectionImageVersion(testCollectionPosterJPEG(t))
	base := "collection-images/collection-1/poster/" + version
	if gotPath != base+"/original.webp" {
		t.Fatalf("path = %q, want %q", gotPath, base+"/original.webp")
	}
	if gotThumbhash == "" {
		t.Fatal("thumbhash is empty")
	}

	want := map[string]bool{
		"/public-assets/" + base + "/original.webp": true,
		"/public-assets/" + base + "/w500.webp":     true,
		"/public-assets/" + base + "/w300.webp":     true,
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

// Issue #1258: replacing collection artwork left the original image displayed
// because every upload wrote the same fixed key, so the public URL never
// changed and CDN/browser caches kept serving the old bytes. Keys are now
// content-addressed: different bytes yield a different path (a fresh URL),
// while identical bytes stay stable.
func TestUploadCollectionImageVariants_ContentAddressedKeysBustCache(t *testing.T) {
	recorder := newCollectionArtworkS3Recorder(t)
	store := blobstore.NewS3(recorder.client())

	first := testCollectionPosterJPEG(t)
	second := testCollectionSolidJPEG(t)

	pathA, _, err := uploadCollectionImageVariants(context.Background(), store, adminCollectionImagePrefix, "collection-1", "poster", first)
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	pathB, _, err := uploadCollectionImageVariants(context.Background(), store, adminCollectionImagePrefix, "collection-1", "poster", second)
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	pathARepeat, _, err := uploadCollectionImageVariants(context.Background(), store, adminCollectionImagePrefix, "collection-1", "poster", first)
	if err != nil {
		t.Fatalf("repeat upload: %v", err)
	}

	if pathA == pathB {
		t.Fatalf("replacement reused the key %q; the URL would stay cached", pathA)
	}
	if pathA != pathARepeat {
		t.Fatalf("identical bytes produced different keys %q and %q", pathA, pathARepeat)
	}
	// The version is a path segment under .../poster/, so the whole prefix is
	// still cleanable by removeCollectionImageVariants.
	if !strings.HasPrefix(pathA, "collection-images/collection-1/poster/") ||
		!strings.HasSuffix(pathA, "/original.webp") {
		t.Fatalf("unexpected key shape %q", pathA)
	}
}

func testCollectionSolidJPEG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 32, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 200, B: 40, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
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

func TestCollectionImagePathVersion(t *testing.T) {
	cases := map[string]string{
		"collection-images/c1/poster/abc123def456abcd/original.webp":    "abc123def456abcd",
		"user-collection-images/c1/backdrop/deadbeefdeadbeef/w300.webp": "deadbeefdeadbeef",
		"collection-images/c1/poster/original.webp":                     "poster", // legacy fixed key: no version segment
		"":     "",
		"solo": "",
	}
	for in, want := range cases {
		if got := collectionImagePathVersion(in); got != want {
			t.Errorf("collectionImagePathVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// stubListDeleteStore satisfies blobstore.Store but only implements List and
// Delete; removeStaleCollectionImageVariants uses no other method.
type stubListDeleteStore struct {
	blobstore.Store
	keys    []string
	deleted []string
}

func (s *stubListDeleteStore) List(_ context.Context, prefix, _ string, _ int) ([]blobstore.ObjectInfo, string, error) {
	var out []blobstore.ObjectInfo
	for _, k := range s.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, blobstore.ObjectInfo{Key: k})
		}
	}
	return out, "", nil
}

func (s *stubListDeleteStore) Delete(_ context.Context, keys []string) (int, error) {
	s.deleted = append(s.deleted, keys...)
	return len(keys), nil
}

func TestRemoveStaleCollectionImageVariants_KeepsNewVersion(t *testing.T) {
	store := &stubListDeleteStore{keys: []string{
		"collection-images/c1/poster/oldversion000000/original.webp",
		"collection-images/c1/poster/oldversion000000/w300.webp",
		"collection-images/c1/poster/original.webp", // legacy fixed key
		"collection-images/c1/poster/newversion111111/original.webp",
		"collection-images/c1/poster/newversion111111/w300.webp",
		"collection-images/c1/backdrop/other/original.webp", // different imageType, untouched
	}}
	if err := removeStaleCollectionImageVariants(context.Background(), store, adminCollectionImagePrefix, "c1", "poster", "newversion111111"); err != nil {
		t.Fatalf("removeStaleCollectionImageVariants: %v", err)
	}
	deleted := map[string]bool{}
	for _, k := range store.deleted {
		deleted[k] = true
	}
	// Old version and legacy key are removed; the just-written version survives.
	for _, k := range []string{
		"collection-images/c1/poster/oldversion000000/original.webp",
		"collection-images/c1/poster/oldversion000000/w300.webp",
		"collection-images/c1/poster/original.webp",
	} {
		if !deleted[k] {
			t.Errorf("expected %q to be deleted", k)
		}
	}
	for _, k := range []string{
		"collection-images/c1/poster/newversion111111/original.webp",
		"collection-images/c1/poster/newversion111111/w300.webp",
		"collection-images/c1/backdrop/other/original.webp",
	} {
		if deleted[k] {
			t.Errorf("did not expect %q to be deleted", k)
		}
	}
}
