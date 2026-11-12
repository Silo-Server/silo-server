package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/userstore"
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
	// Keys carry a content revision of the source bytes (issue #1258), so the
	// stored path names the revision of the template's own bytes.
	revision := collectionImageRevision(testCollectionPosterJPEG(t))
	base := "collection-images/collection-1/poster/"
	if gotPath != base+"original."+revision+".webp" {
		t.Fatalf("path = %q, want %q", gotPath, base+"original."+revision+".webp")
	}
	if gotThumbhash == "" {
		t.Fatal("thumbhash is empty")
	}

	want := map[string]bool{
		"/public-assets/" + base + "original." + revision + ".webp": true,
		"/public-assets/" + base + "w500." + revision + ".webp":     true,
		"/public-assets/" + base + "w300." + revision + ".webp":     true,
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
// changed and CDN/browser caches kept serving the old bytes. Keys now carry a
// content revision: different bytes yield a different key (a fresh URL), while
// identical bytes stay stable.
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
	// The revision uses the shared artworkkey shape inside .../poster/, so
	// deleting an admin collection still clears it by prefix and the signed
	// artwork route treats the key as immutable.
	if !strings.HasPrefix(pathA, "collection-images/collection-1/poster/original.") ||
		artworkkey.Revision(pathA) == "" {
		t.Fatalf("unexpected key shape %q", pathA)
	}
	if got := cardThumbnailPath(pathA); got != artworkkey.Variant(pathA, "w300") {
		t.Fatalf("card thumbnail path = %q, want the w300 variant of %q", got, pathA)
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

func TestRemoveReplacedCollectionImageVersion_DeletesOnlySupersededVersion(t *testing.T) {
	store := &stubListDeleteStore{keys: []string{
		"collection-images/c1/poster/original.0000000000000001.webp",
		"collection-images/c1/poster/w300.0000000000000001.webp",
		// A concurrent replacement's committed revision must never be deleted.
		"collection-images/c1/poster/original.00000000000000cc.webp",
		"collection-images/c1/poster/original.0000000000000002.webp",
		"collection-images/c1/poster/w300.0000000000000002.webp",
		// Legacy fixed keys belong to no revision here.
		"collection-images/c1/poster/original.webp",
		// Another image type of the same collection is never touched.
		"collection-images/c1/backdrop/original.0000000000000001.webp",
	}}
	oldPath := "collection-images/c1/poster/original.0000000000000001.webp"
	currentPath := "collection-images/c1/poster/original.0000000000000002.webp"
	if err := removeReplacedCollectionImageVersion(context.Background(), store, adminCollectionImagePrefix, "c1", "poster", oldPath, currentPath); err != nil {
		t.Fatalf("removeReplacedCollectionImageVersion: %v", err)
	}
	assertCollectionImageDeletes(t, store.deleted,
		"collection-images/c1/poster/original.0000000000000001.webp",
		"collection-images/c1/poster/w300.0000000000000001.webp",
	)
}

// Replacing pre-revision artwork removes the legacy fixed keys, which no
// later replacement could otherwise identify, and keeps every revision.
func TestRemoveReplacedCollectionImageVersion_RemovesLegacyFixedKeys(t *testing.T) {
	store := &stubListDeleteStore{keys: []string{
		"collection-images/c1/poster/original.webp",
		"collection-images/c1/poster/w500.webp",
		"collection-images/c1/poster/w300.webp",
		"collection-images/c1/poster/original.0000000000000002.webp",
		"collection-images/c1/poster/w300.0000000000000002.webp",
	}}
	if err := removeReplacedCollectionImageVersion(context.Background(), store, adminCollectionImagePrefix, "c1", "poster",
		"collection-images/c1/poster/original.webp",
		"collection-images/c1/poster/original.0000000000000002.webp",
	); err != nil {
		t.Fatalf("removeReplacedCollectionImageVersion: %v", err)
	}
	assertCollectionImageDeletes(t, store.deleted,
		"collection-images/c1/poster/original.webp",
		"collection-images/c1/poster/w500.webp",
		"collection-images/c1/poster/w300.webp",
	)
}

// partialDeleteStore deletes the first okKeys keys of a batch and then fails,
// as the filesystem store does when one key cannot be removed.
type partialDeleteStore struct {
	stubListDeleteStore
	okKeys int
}

func (s *partialDeleteStore) Delete(_ context.Context, keys []string) (int, error) {
	n := min(s.okKeys, len(keys))
	s.deleted = append(s.deleted, keys[:n]...)
	if n < len(keys) {
		return n, errors.New("permission denied")
	}
	return n, nil
}

// Clearing an image removes only the revision the clear displaced. A
// revision that a concurrent upload stored, committed or not, survives the
// cleanup, so the upload never ends up referencing deleted artwork.
func TestCleanUpReplacedCollectionImage_ClearKeepsConcurrentUpload(t *testing.T) {
	store := &stubListDeleteStore{keys: []string{
		"collection-images/c1/poster/original.0000000000000001.webp",
		"collection-images/c1/poster/w300.0000000000000001.webp",
		"collection-images/c1/poster/original.0000000000000002.webp",
		"collection-images/c1/poster/w300.0000000000000002.webp",
		"collection-images/c1/backdrop/original.0000000000000001.webp",
	}}
	for _, current := range []string{"", "collection-images/c1/poster/original.0000000000000002.webp"} {
		store.deleted = nil
		cleanUpReplacedCollectionImage(t.Context(), store, adminCollectionImagePrefix, "c1", "poster",
			"collection-images/c1/poster/original.0000000000000001.webp",
			func(context.Context) (string, error) { return current, nil },
		)
		assertCollectionImageDeletes(t, store.deleted,
			"collection-images/c1/poster/original.0000000000000001.webp",
			"collection-images/c1/poster/w300.0000000000000001.webp",
		)
	}
}

func assertCollectionImageDeletes(t *testing.T, got []string, want ...string) {
	t.Helper()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("deleted = %#v, want %#v", got, want)
	}
}

func TestRemoveReplacedCollectionImageVersion_NoopCases(t *testing.T) {
	current := "collection-images/c1/poster/original.0000000000000002.webp"
	old := "collection-images/c1/poster/original.0000000000000001.webp"
	cases := []struct{ name, oldPath, currentPath string }{
		// A bundled-template path is not one of our S3 keys.
		{"template path", "/images/collection-templates/x.jpg", current},
		// Another collection's key is never cleaned up from here.
		{"other collection", "collection-images/c2/poster/original.0000000000000001.webp", current},
		{"empty", "", current},
		// Identical content re-upload: same revision, nothing to delete.
		{"same revision", current, current},
		// Concurrent restore of the old content: the row points back at the
		// revision we would clean up, so it must be preserved.
		{"row restored old revision", old, old},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubListDeleteStore{keys: []string{
				"collection-images/c1/poster/original.webp",
				old,
				current,
				"collection-images/c2/poster/original.0000000000000001.webp",
			}}
			if err := removeReplacedCollectionImageVersion(context.Background(), store, adminCollectionImagePrefix, "c1", "poster", tc.oldPath, tc.currentPath); err != nil {
				t.Fatalf("removeReplacedCollectionImageVersion: %v", err)
			}
			if len(store.deleted) != 0 {
				t.Fatalf("expected no deletions, got %#v", store.deleted)
			}
		})
	}
}

// posterUpdateStore is a personal collection store that holds one collection,
// created by profile p1, with one poster path. It counts the collections it
// creates and the updates that leave the poster alone, and can fail the
// poster update.
type posterUpdateStore struct {
	userstore.UserStore
	posterURL string
	updateErr error
	creates   int
	updates   int
}

func (s *posterUpdateStore) CreateCollection(_ context.Context, input userstore.CreateCollectionInput) (*userstore.Collection, error) {
	s.creates++
	return &userstore.Collection{ID: "c1", Name: input.Name, CreatorProfileID: input.CreatorProfileID}, nil
}

func (s *posterUpdateStore) GetCollection(_ context.Context, id string) (*userstore.Collection, error) {
	return &userstore.Collection{ID: id, CreatorProfileID: "p1", PosterURL: s.posterURL}, nil
}

func (s *posterUpdateStore) UpdateCollection(_ context.Context, input userstore.UpdateCollectionInput) error {
	if input.PosterURL == nil {
		s.updates++
		return nil
	}
	if s.updateErr != nil {
		return s.updateErr
	}
	s.posterURL = *input.PosterURL
	return nil
}

func seedPersonalPoster(t *testing.T, artwork blobstore.Store) string {
	t.Helper()
	oldPath, _, err := uploadCollectionImageVariants(t.Context(), artwork, userCollectionImagePrefix, "c1", collectionImagePoster, testCollectionPosterJPEG(t))
	if err != nil {
		t.Fatalf("seed poster: %v", err)
	}
	return oldPath
}

// A replacement whose upload succeeds but whose update fails must leave the
// stored poster readable: the row still points at the old key. The client
// gets a 500 that repeats none of the store's own words.
func TestUploadPersonalCollectionPoster_FailedUpdateKeepsStoredPoster(t *testing.T) {
	artwork, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	oldPath := seedPersonalPoster(t, artwork)
	const storeWords = "bucket silo-private refused the write"
	store := &posterUpdateStore{posterURL: oldPath, updateErr: errors.New(storeWords)}
	h := NewCollectionHandler(testUserStoreProvider{store: store})
	h.ArtworkStore = artwork

	_, err = h.UploadPersonalCollectionPoster(t.Context(), 1, "p1", "c1", testCollectionSolidJPEG(t))
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusInternalServerError || strings.Contains(apiErr.Message, storeWords) {
		t.Fatalf("err = %v, want a 500 without the store's error text", err)
	}
	for _, key := range []string{oldPath, cardThumbnailPath(oldPath)} {
		if _, err := artwork.Stat(t.Context(), key); err != nil {
			t.Fatalf("stored poster %q is gone after a failed update: %v", key, err)
		}
	}
}

// A committed replacement removes the revision it superseded and keeps its own.
func TestProcessCollectionPoster_ReplacementRemovesOldRevision(t *testing.T) {
	artwork, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	oldPath := seedPersonalPoster(t, artwork)
	store := &posterUpdateStore{posterURL: oldPath}
	h := &CollectionHandler{ArtworkStore: artwork}

	replacement := testCollectionSolidJPEG(t)
	if err := h.processCollectionPoster(t.Context(), store, "c1", "p1", replacement, ""); err != nil {
		t.Fatalf("processCollectionPoster: %v", err)
	}
	if store.posterURL == oldPath || artworkkey.Revision(store.posterURL) == "" {
		t.Fatalf("poster path = %q, want a new revision replacing %q", store.posterURL, oldPath)
	}
	if _, err := artwork.Stat(t.Context(), store.posterURL); err != nil {
		t.Fatalf("new poster %q missing: %v", store.posterURL, err)
	}
	items, _, err := artwork.List(t.Context(), "user-collection-images/c1/poster/", "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range items {
		if artworkkey.Revision(item.Key) != artworkkey.Revision(store.posterURL) {
			t.Fatalf("superseded object %q was not removed", item.Key)
		}
	}
}

// A replacement committed just before the client disconnects still removes the
// revision it superseded.
func TestCleanUpReplacedCollectionImage_OutlivesCanceledRequest(t *testing.T) {
	store := &stubListDeleteStore{keys: []string{
		"collection-images/c1/poster/original.0000000000000001.webp",
		"collection-images/c1/poster/original.0000000000000002.webp",
	}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	readCurrent := func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "collection-images/c1/poster/original.0000000000000002.webp", nil
	}
	cleanUpReplacedCollectionImage(ctx, store, adminCollectionImagePrefix, "c1", "poster",
		"collection-images/c1/poster/original.0000000000000001.webp", readCurrent)
	assertCollectionImageDeletes(t, store.deleted, "collection-images/c1/poster/original.0000000000000001.webp")
}

func TestDownloadCollectionImageURL_ClientErrorsAre400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	for name, rawURL := range map[string]string{
		"missing source": server.URL + "/poster.jpg",
		"non-http":       "ftp://example.invalid/poster.jpg",
		"no host":        "http://",
		"opaque":         "http:example.invalid/poster.jpg",
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

// captureDefaultLog sends the default logger's output to the returned buffer
// until the test ends.
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestCollectionArtworkError_HidesServerFailures(t *testing.T) {
	logs := captureDefaultLog(t)

	cause := errors.New("uploading original: connection reset")
	for _, err := range []error{
		personalCollectionPosterError(t.Context(), "c1", cause),
		collectionArtworkError(cause, "Failed to store collection artwork"),
	} {
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok || apiErr.Status != http.StatusInternalServerError || apiErr.Message != "Failed to store collection artwork" {
			t.Fatalf("err = %#v, want the 500 fallback", err)
		}
	}

	// A busy answer is the caller's to act on, so it is not hidden, on the
	// personal routes or the admin ones.
	busy := &APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "busy", RetryAfter: 30}
	for _, err := range []error{
		personalCollectionPosterError(t.Context(), "c1", fmt.Errorf("poster: %w", busy)),
		collectionArtworkError(busy, "Failed to store collection artwork"),
	} {
		if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusServiceUnavailable || apiErr.RetryAfter != 30 {
			t.Fatalf("err = %#v, want the 503 kept with its Retry-After", err)
		}
	}

	// A request the client ended is not logged as a failure.
	logs.Reset()
	_ = personalCollectionPosterError(t.Context(), "c1", fmt.Errorf("poster: uploading original: %w", context.Canceled))
	if logs.Len() != 0 {
		t.Fatalf("a canceled request was logged: %q", logs.String())
	}
}

// Linux libvips reads a truncated PNG's header and fails inside Process; other
// builds may accept the damaged data. Either way it must not become a 500.
func TestUploadCollectionImageVariants_TruncatedPNGIsNotAServerError(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 600, 900))
	for y := 0; y < 900; y++ {
		for x := 0; x < 600; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 99, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	data := buf.Bytes()

	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	_, _, err = uploadCollectionImageVariants(t.Context(), store, userCollectionImagePrefix, "c1", "poster", data[:len(data)/2])
	if err == nil {
		return
	}
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want nil or a 400 APIError", err)
	}
}

// An upload that cannot be decoded must answer 400 and leave the stored poster
// and the row untouched.
func TestProcessCollectionPoster_InvalidImageKeepsStoredPoster(t *testing.T) {
	artwork, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	oldPath := seedPersonalPoster(t, artwork)
	store := &posterUpdateStore{posterURL: oldPath}
	h := &CollectionHandler{ArtworkStore: artwork}

	err = h.processCollectionPoster(t.Context(), store, "c1", "p1", []byte("not an image"), "")
	mapped := personalCollectionPosterError(t.Context(), "c1", err)
	if apiErr, ok := errors.AsType[*APIError](mapped); !ok || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("mapped err = %v, want a 400 APIError", mapped)
	}
	if store.posterURL != oldPath {
		t.Fatalf("poster path = %q, want the stored %q", store.posterURL, oldPath)
	}
	for _, key := range []string{oldPath, cardThumbnailPath(oldPath)} {
		if _, err := artwork.Stat(t.Context(), key); err != nil {
			t.Fatalf("stored poster %q is gone after an invalid upload: %v", key, err)
		}
	}
}

// posterSourceTransport answers a poster source download without dialing.
type posterSourceTransport func(*http.Request) (*http.Response, error)

func (f posterSourceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// servePoster answers every request with body as an image.
func servePoster(body string) posterSourceTransport {
	return func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/jpeg"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
}

// A create whose poster_source_url the server cannot use, or cannot
// download, creates nothing, so repeating the request cannot leave two
// collections. An image it cannot use is the caller's 400; a source on an
// address the server will not fetch keeps its one fixed answer; any other
// failed download is a 500 with a fixed message. The log line for a failed
// download names the source host and carries nothing else of the URL the
// request or a redirect named, wherever in it a credential sits.
func TestCreatePersonalCollection_UnusablePosterCreatesNothing(t *testing.T) {
	artwork, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	logs := captureDefaultLog(t)

	const (
		credential = "5up3r-pr1vate"
		notAllowed = "poster source: the image URL must be a public internet address"
		failed     = "Failed to store collection artwork"
	)
	connectionReset := posterSourceTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset")
	})
	for _, tc := range []struct {
		name, source string
		// transport replaces the guarded client's; nil keeps it.
		transport posterSourceTransport
		status    int
		message   string
		// loggedHost is the host a failed download's log line names.
		loggedHost string
	}{
		{name: "a file that is not an image", source: "https://images.example/poster.jpg", transport: servePoster("not an image"), status: http.StatusBadRequest},
		{name: "a source on the server's own network", source: closedLoopbackURL(t), status: http.StatusInternalServerError, message: notAllowed},
		{name: "failed download, credential in the query", source: "https://images.example/poster.jpg?sig=" + credential,
			transport: connectionReset, status: http.StatusInternalServerError, message: failed, loggedHost: "images.example"},
		{name: "failed download, credential in the path", source: "https://images.example/k/" + credential + "/poster.jpg",
			transport: connectionReset, status: http.StatusInternalServerError, message: failed, loggedHost: "images.example"},
		// The client reports a Location it cannot parse by quoting it.
		{name: "redirect to an unparsable address", source: "https://images.example/poster.jpg", transport: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://cdn.example/%zz?sig=" + credential}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}, status: http.StatusInternalServerError, message: failed, loggedHost: "images.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			store := &posterUpdateStore{}
			h := NewCollectionHandler(testUserStoreProvider{store: store})
			h.ArtworkStore = artwork
			if tc.transport != nil {
				h.HTTPClient = &http.Client{Transport: tc.transport}
			}

			_, err := h.CreatePersonalCollection(t.Context(), PersonalCollectionCreateCommand{
				UserID: 1, ProfileID: "p1",
				Request: PersonalCollectionCreateRequest{Name: "Posters", PosterSourceURL: tc.source},
			})
			if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != tc.status || (tc.message != "" && apiErr.Message != tc.message) {
				t.Errorf("err = %v, want a %d APIError %q", err, tc.status, tc.message)
			}
			if store.creates != 0 {
				t.Errorf("created %d collections, want none", store.creates)
			}
			line := logs.String()
			if tc.loggedHost != "" && !strings.Contains(line, "host="+tc.loggedHost) {
				t.Errorf("log = %q, want a line naming the source host", line)
			}
			if strings.Contains(line, credential) {
				t.Errorf("log carries the credential: %q", line)
			}
		})
	}
}

// createCountingStore is a personal collection store that counts the
// collections it is asked to create and creates none.
type createCountingStore struct {
	userstore.UserStore
	creates int
}

func (s *createCountingStore) CreateCollection(context.Context, userstore.CreateCollectionInput) (*userstore.Collection, error) {
	s.creates++
	return nil, errors.New("no collection is created in this test")
}

// An import's poster_url is stored and later shown as the collection's
// poster, so it may only be a bundled template poster or an image URL. A bare
// storage key must be refused before anything is created.
func TestImportTMDB_RefusesStorageKeyAsPoster(t *testing.T) {
	cases := []struct {
		name, poster string
		refused      bool
	}{
		{"storage key", "library-posters/1.jpg", true},
		{"escaped path out of the template directory", collectionTemplateImageDir + "%2e%2e/%2e%2e/api/v2/artwork/library-posters/1.jpg", true},
		{"template poster", collectionTemplateImageDir + "tmdb_popular_movies.jpg", false},
		{"image URL", "https://image.example/poster.jpg", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &createCountingStore{}
			h := NewUserCollectionImportHandler(testUserStoreProvider{store: store}, nil, nil, nil, nil, nil)

			req := UserImportTMDBRequest{Preset: "popular", MediaType: "movie"}
			req.Title = "Popular"
			req.PosterURL = tc.poster
			_, err := h.ImportTMDB(t.Context(), 1, "p1", req)

			apiErr, _ := errors.AsType[*APIError](err)
			refused := apiErr != nil && apiErr.Status == http.StatusBadRequest && apiErr.Field == "poster_url"
			if refused != tc.refused || (store.creates == 0) != tc.refused {
				t.Fatalf("err = %v with %d creates, want refused on poster_url = %v", err, store.creates, tc.refused)
			}
		})
	}
}

// recordingResolver resolves each key to itself and records the keys it was
// asked to sign.
type recordingResolver struct{ asked []string }

func (r *recordingResolver) ResolveURLs(ctx context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	r.asked = append(r.asked, keys...)
	return keyEchoResolver{}.ResolveURLs(ctx, keys)
}

// A personal collection's stored poster is signed only when it is an object in
// that collection's own image directory. Any other stored key reads as no
// poster and never reaches the signer.
func TestPersonalCollectionPoster_SignsOnlyTheCollectionsOwnKeys(t *testing.T) {
	const own = "user-collection-images/c1/poster/original.0123456789abcdef.webp"
	cases := []struct {
		name, stored, want string
	}{
		{"another prefix", "library-posters/1.jpg", ""},
		{"another collection", "user-collection-images/c2/poster/original.0123456789abcdef.webp", ""},
		{"a path out of its directory", "user-collection-images/c1/../c2/poster/original.webp", ""},
		{"its own poster", own, "resolved:" + cardThumbnailPath(own)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &recordingResolver{}
			h := &UserCollectionImportHandler{ArtworkResolver: resolver}
			view := h.collectionView(t.Context(), userstore.Collection{ID: "c1", PosterURL: tc.stored})
			if view.PosterURL != tc.want {
				t.Fatalf("poster_url = %q, want %q", view.PosterURL, tc.want)
			}
			if tc.want == "" && len(resolver.asked) != 0 {
				t.Fatalf("signer was asked for %q", resolver.asked)
			}
		})
	}
}
