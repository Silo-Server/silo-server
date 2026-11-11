package handlers

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"testing/synctest"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/imageutil"
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

// posterUpdateStore is a personal collection store that holds one poster path
// and can fail the poster update.
type posterUpdateStore struct {
	userstore.UserStore
	posterURL string
	updateErr error
	created   int
}

func (s *posterUpdateStore) CreateCollection(_ context.Context, input userstore.CreateCollectionInput) (*userstore.Collection, error) {
	s.created++
	return &userstore.Collection{ID: "c1", Name: input.Name}, nil
}

func (s *posterUpdateStore) GetCollection(_ context.Context, id string) (*userstore.Collection, error) {
	return &userstore.Collection{ID: id, PosterURL: s.posterURL}, nil
}

func (s *posterUpdateStore) UpdateCollection(_ context.Context, input userstore.UpdateCollectionInput) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	if input.PosterURL != nil {
		s.posterURL = *input.PosterURL
	}
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
// stored poster readable: the row still points at the old key.
func TestProcessCollectionPoster_FailedUpdateKeepsStoredPoster(t *testing.T) {
	artwork, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	oldPath := seedPersonalPoster(t, artwork)
	store := &posterUpdateStore{posterURL: oldPath, updateErr: errors.New("update failed")}
	h := &CollectionHandler{ArtworkStore: artwork}

	replacement := testCollectionSolidJPEG(t)
	if _, err := h.processCollectionPoster(t.Context(), store, "c1", "p1", func() ([]byte, error) { return replacement, nil }, ""); err == nil {
		t.Fatal("processCollectionPoster succeeded, want the update error")
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
	if _, err := h.processCollectionPoster(t.Context(), store, "c1", "p1", func() ([]byte, error) { return replacement, nil }, ""); err != nil {
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

func TestCollectionArtworkError_HidesServerFailures(t *testing.T) {
	err := collectionArtworkError(errors.New("uploading original: connection reset"), "Failed to store collection artwork")
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusInternalServerError || apiErr.Message != "Failed to store collection artwork" {
		t.Fatalf("err = %#v, want the 500 fallback", err)
	}
	// A server too busy to decode the upload is the client's cue to retry.
	busy := apiError(http.StatusServiceUnavailable, "service_unavailable", "busy")
	err = collectionArtworkError(busy, "Failed to store collection artwork")
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %#v, want the 503 kept", err)
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

// plainSVG is a small drawing of one rectangle.
const plainSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><rect width="8" height="8"/></svg>`

// An upload that cannot be decoded, or is in a format a profile may not
// supply, must answer 400 and leave the stored poster and the row untouched.
func TestProcessCollectionPoster_InvalidImageKeepsStoredPoster(t *testing.T) {
	for name, upload := range map[string]string{"not an image": "not an image", "an SVG": plainSVG} {
		t.Run(name, func(t *testing.T) {
			artwork, err := blobstore.NewFilesystem(t.TempDir())
			if err != nil {
				t.Fatalf("NewFilesystem: %v", err)
			}
			oldPath := seedPersonalPoster(t, artwork)
			store := &posterUpdateStore{posterURL: oldPath}
			h := &CollectionHandler{ArtworkStore: artwork}

			_, err = h.processCollectionPoster(t.Context(), store, "c1", "p1", func() ([]byte, error) { return []byte(upload), nil }, "")
			mapped := collectionArtworkError(err, "Failed to store collection artwork")
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
		})
	}
}

// avatarProfileStore is a user store that knows every profile it is asked for.
type avatarProfileStore struct{ userstore.UserStore }

func (avatarProfileStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	return &userstore.Profile{ID: id}, nil
}

// The bytes decide what an avatar is: an SVG sent as a PNG part is refused
// before anything is decoded or stored.
func TestUploadAvatar_SVGIsUnsupported(t *testing.T) {
	avatars, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	h := NewProfileHandler(pagingIntegrationProvider{account: 1, store: avatarProfileStore{}})
	h.AvatarStore = avatars

	_, err = h.UploadAvatar(t.Context(), ProfileAvatarUpload{
		UserID: 1, ProfileID: "p1", ContentType: "image/png", File: strings.NewReader(plainSVG),
	})
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want a 400 APIError", err)
	}
}

// tooLargeImageHeader is a JPEG cut off after its header, which declares more
// pixels than the server decodes. There is no image behind it.
var tooLargeImageHeader = []byte{
	0xFF, 0xD8, // start of image
	0xFF, 0xC0, 0, 17, 8, 0x23, 0x28, 0x23, 0x28, 3, 1, 0x11, 0, 2, 0x11, 0, 3, 0x11, 0, // 9000×9000 frame
	0xFF, 0xDA, 0, 12, 3, 1, 0, 2, 0, 3, 0, 0, 63, 0, // start of scan
	0xFF, 0xD9, // end of image
}

// A create whose poster is refused stores nothing: a poster too large to
// decode is the caller's 400, and a server too busy to decode it answers a
// 503 the client may repeat without leaving two collections.
func TestCreatePersonalCollection_RefusedPosterCreatesNothing(t *testing.T) {
	artwork, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	for _, tc := range []struct {
		name   string
		poster []byte
		busy   bool
		want   int
	}{
		{"too large to decode", tooLargeImageHeader, false, http.StatusBadRequest},
		{"no decode slot or place to wait", testCollectionPosterJPEG(t), true, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.busy {
				idle := uploadedImageDecodes
				uploadedImageDecodes = newDecodeSlots(1, 0)
				uploadedImageDecodes.running <- struct{}{}
				t.Cleanup(func() { uploadedImageDecodes = idle })
			}
			store := &posterUpdateStore{}
			h := NewCollectionHandler(pagingIntegrationProvider{account: 1, store: store})
			h.ArtworkStore = artwork

			_, err := h.CreatePersonalCollection(t.Context(), PersonalCollectionCreateCommand{
				UserID: 1, ProfileID: "p1",
				Request:    PersonalCollectionCreateRequest{Name: "Wide"},
				PosterFile: func() ([]byte, error) { return tc.poster, nil },
			})
			if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != tc.want {
				t.Fatalf("err = %v, want a %d APIError", err, tc.want)
			}
			if store.created != 0 {
				t.Fatalf("created %d collections, want none", store.created)
			}
		})
	}
}

// With every decode slot taken an upload waits for a turn no longer than its
// request, with the wait full as well the next upload is refused undecoded,
// and each way out gives its place back.
func TestDecodeSlots_BoundDecodesAndWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		slots := newDecodeSlots(1, 1)
		ran := 0
		decode := func() (*imageutil.VariantResult, error) {
			ran++
			return nil, errors.New("decode failed")
		}
		slots.running <- struct{}{} // a decode in progress

		gone, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := slots.decode(gone, decode); !errors.Is(err, context.Canceled) || len(slots.waiting) != 0 {
			t.Fatalf("err = %v with %d waiting, want the canceled request and nobody waiting", err, len(slots.waiting))
		}

		waited := make(chan error)
		go func() {
			_, err := slots.decode(t.Context(), decode)
			waited <- err
		}()
		synctest.Wait() // that upload now waits for the slot

		_, err := slots.decode(t.Context(), decode)
		if busy, ok := errors.AsType[*APIError](err); !ok || busy.Status != http.StatusServiceUnavailable || busy.RetryAfter == 0 {
			t.Fatalf("err = %v, want a 503 with Retry-After", err)
		}
		if ran != 0 {
			t.Fatalf("decodes = %d while the slot was taken, want 0", ran)
		}

		<-slots.running // the decode in progress ends
		if err := <-waited; err == nil || ran != 1 {
			t.Fatalf("err = %v after %d decodes, want the waiting upload's own decode error", err, ran)
		}
		if len(slots.running) != 0 || len(slots.waiting) != 0 {
			t.Fatalf("%d running and %d waiting after every upload ended", len(slots.running), len(slots.waiting))
		}
	})
}
