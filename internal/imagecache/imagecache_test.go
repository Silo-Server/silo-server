package imagecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/h2non/bimg"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

const (
	testTMDBProviderID    = "tmdb"
	testMoviesContentType = "movies"
)

// makeTestJPEG generates a minimal solid-color JPEG for use in tests.
func makeTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 600))
	for y := range 600 {
		for x := range 400 {
			img.SetRGBA(x, y, color.RGBA{R: 100, G: 149, B: 237, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("makeTestJPEG: encode: %v", err)
	}
	return buf.Bytes()
}

func makeTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 180, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("makeTestPNG: encode: %v", err)
	}
	return buf.Bytes()
}

// mockStore records every write for test assertions.
type mockStore struct {
	mu                    sync.Mutex
	calls                 []writeCall
	writeErr              error // if non-nil, returned for every write
	failuresBeforeSuccess int
	existing              map[string]bool
	matchErr              error
	matchCalls            []string
}

type writeCall struct {
	key       string
	mediaType string
	data      []byte
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type fixedImageResolver struct{ url string }

func (r fixedImageResolver) ResolveImageURL(context.Context, string, string) string { return r.url }

func (m *mockStore) WriteImmutable(_ context.Context, key string, data []byte, meta artworkstore.ObjectMetadata) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failuresBeforeSuccess > 0 {
		m.failuresBeforeSuccess--
		return errors.New("temporary storage failure")
	}
	if m.writeErr != nil {
		return m.writeErr
	}
	m.calls = append(m.calls, writeCall{key: key, mediaType: meta.MediaType, data: bytes.Clone(data)})
	return nil
}

// Matches treats keys registered via setExisting as content matches; real
// content verification is exercised against the store implementations.
func (m *mockStore) Matches(_ context.Context, key string, _ []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.matchCalls = append(m.matchCalls, key)
	if m.matchErr != nil {
		return false, m.matchErr
	}
	return m.existing[key], nil
}

func (m *mockStore) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, len(m.calls))
	for i, c := range m.calls {
		keys[i] = c.key
	}
	return keys
}

func (m *mockStore) objectData(key string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.calls {
		if c.key == key {
			return c.data
		}
	}
	return nil
}

func (m *mockStore) mediaType(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.calls {
		if c.key == key {
			return c.mediaType
		}
	}
	return ""
}

func (m *mockStore) checkedKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.matchCalls)
}

func (m *mockStore) setExisting(keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.existing == nil {
		m.existing = make(map[string]bool)
	}
	for _, key := range keys {
		m.existing[key] = true
	}
}

func (m *mockStore) resetCalls() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
	m.matchCalls = nil
}

// read adapts the store to artworkkey.ManifestObjectReader so tests can
// validate a stored revision the same way an adopting server would.
func (m *mockStore) read(_ context.Context, key string) (io.ReadCloser, error) {
	data := m.objectData(key)
	if data == nil {
		return nil, fmt.Errorf("no object at %q", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type trackedRevision struct {
	originalPath string
	imageType    string
	objectKeys   []string
}

type recordingRevisionTracker struct {
	mu           sync.Mutex
	calls        []trackedRevision
	recordedPath string
	sourceClass  string
	objects      []artworkstore.ObjectInfo
	recordCalls  int
	err          error
}

func (t *recordingRevisionTracker) TrackArtworkRevision(_ context.Context, originalPath, imageType string, objectKeys []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, trackedRevision{
		originalPath: originalPath,
		imageType:    imageType,
		objectKeys:   slices.Clone(objectKeys),
	})
	return t.err
}

func (t *recordingRevisionTracker) RecordArtworkRevision(_ context.Context, originalPath, sourceClass string, objects []artworkstore.ObjectInfo) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordCalls++
	t.recordedPath = originalPath
	t.sourceClass = sourceClass
	t.objects = append([]artworkstore.ObjectInfo(nil), objects...)
	return t.err
}

func (t *recordingRevisionTracker) recorded() []trackedRevision {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.calls)
}

func hasKey(keys []string, key string) bool {
	return slices.Contains(keys, key)
}

// startImageServer starts an httptest server that serves JPEG data.
// If statusCode != 200, it returns that status with an empty body.
func startImageServer(t *testing.T, data []byte, statusCode int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if statusCode != http.StatusOK {
			w.WriteHeader(statusCode)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// requirePortableKey asserts that key is a portable logical key for imageType
// and returns its parsed form.
func requirePortableKey(t *testing.T, key, imageType string) artworkkey.PortableKeyInfo {
	t.Helper()
	info, ok := artworkkey.ParsePortableKey(key)
	if !ok {
		t.Fatalf("key %q is not a portable artwork key", key)
	}
	if info.ImageType != imageType {
		t.Fatalf("key %q image type = %q, want %q", key, info.ImageType, imageType)
	}
	return info
}

func TestCacheBytesTracksExactRevisionBeforeUpload(t *testing.T) {
	store := &mockStore{}
	tracker := &recordingRevisionTracker{}
	cacher := newWithHTTPClient(store, nil)
	cacher.SetArtworkRevisionTracker(tracker)

	result, err := cacher.CacheBytes(context.Background(), makeTestJPEG(t), CacheRequest{
		SourceURL:   "https://images.example/poster.jpg",
		ProviderID:  testTMDBProviderID,
		ContentType: testMoviesContentType,
		ContentID:   "335984",
		ImageType:   metadata.ImagePoster,
	})
	if err != nil {
		t.Fatalf("CacheBytes: %v", err)
	}

	calls := tracker.recorded()
	if len(calls) != 1 {
		t.Fatalf("tracker calls = %d, want 1", len(calls))
	}
	if calls[0].originalPath != result.OriginalPath {
		t.Fatalf("tracked original = %q, want %q", calls[0].originalPath, result.OriginalPath)
	}
	if calls[0].imageType != "poster" {
		t.Fatalf("tracked image type = %q, want poster", calls[0].imageType)
	}
	// The manifest is registered with the images: an interrupted write must
	// leave a reclaimable object set, not an orphan completeness marker.
	wantKeys := make([]string, 0, len(result.VariantPaths)+1)
	for _, key := range result.VariantPaths {
		wantKeys = append(wantKeys, key)
	}
	wantKeys = append(wantKeys, result.ManifestPath)
	sort.Strings(wantKeys)
	if !slices.Equal(calls[0].objectKeys, wantKeys) {
		t.Fatalf("tracked keys = %v, want %v", calls[0].objectKeys, wantKeys)
	}
	for _, key := range calls[0].objectKeys {
		if err := artworkstore.ValidateKey(key); err != nil {
			t.Fatalf("tracked key %q is not a valid store key: %v", key, err)
		}
		if strings.Contains(key, "://") {
			t.Fatalf("tracked key %q carries a scheme; revision tracking rejects those", key)
		}
	}
	if tracker.recordCalls != 1 || tracker.recordedPath != result.OriginalPath || tracker.sourceClass != "provider" {
		t.Fatalf("inventory completion = calls:%d path:%q source:%q", tracker.recordCalls, tracker.recordedPath, tracker.sourceClass)
	}
	if len(tracker.objects) != len(calls[0].objectKeys) {
		t.Fatalf("inventory objects = %d, want %d", len(tracker.objects), len(calls[0].objectKeys))
	}
	for _, object := range tracker.objects {
		stored := store.objectData(object.Key)
		if object.SizeBytes != int64(len(stored)) {
			t.Fatalf("inventory size for %s = %d, stored %d", object.Key, object.SizeBytes, len(stored))
		}
		if object.MediaType == "" {
			t.Fatalf("inventory content type for %s is empty", object.Key)
		}
	}
}

func TestCacheAdoptsPluginReferenceBeforeResolvingOrDownloading(t *testing.T) {
	store, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := makeTestJPEG(t)
	firstClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}
	req := CacheRequest{
		SourceURL: "tmdb://poster/opaque-42", ProviderID: testTMDBProviderID,
		ContentType: testMoviesContentType, ContentID: "42", ImageType: metadata.ImagePoster,
		ImageResolver: fixedImageResolver{url: "https://images.example/poster.jpg"},
	}
	first, err := newWithHTTPClient(store, firstClient).Cache(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	secondClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("download must not run on adoption hit")
	})}
	second, err := newWithHTTPClient(store, secondClient).Cache(context.Background(), req)
	if err != nil {
		t.Fatalf("adopt cache: %v", err)
	}
	if second.OriginalPath != first.OriginalPath || second.ExistingVariants == 0 || second.UploadedVariants != 0 {
		t.Fatalf("adopted result = %#v; first = %#v", second, first)
	}
}

func TestCacheImageAdoptsResolvedPluginReference(t *testing.T) {
	store, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := makeTestJPEG(t)
	firstClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}
	req := metadata.CacheImageRequest{
		SourceURL:       "https://images.example/poster.jpg",
		SourceReference: "tmdb://poster/opaque-42",
		ProviderID:      testTMDBProviderID,
		ContentType:     testMoviesContentType,
		ContentID:       "42",
		ImageType:       metadata.ImagePoster,
	}
	fingerprint := stablePluginSourceFingerprint(req.SourceReference)
	if fingerprint == "" {
		t.Fatal("stable plugin source fingerprint is empty")
	}
	first, err := newWithHTTPClient(store, firstClient).CacheImage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	secondClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("download must not run on adoption hit")
	})}
	second, err := newWithHTTPClient(store, secondClient).CacheImage(context.Background(), req)
	if err != nil {
		t.Fatalf("adopt resolved plugin reference: %v", err)
	}
	if second.OriginalPath != first.OriginalPath || second.ExistingVariants == 0 || second.UploadedVariants != 0 {
		t.Fatalf("adopted result = %#v; first = %#v", second, first)
	}
}

func TestCacheBytesDoesNotUploadWhenRevisionTrackingFails(t *testing.T) {
	store := &mockStore{}
	tracker := &recordingRevisionTracker{err: errors.New("registry unavailable")}
	cacher := newWithHTTPClient(store, nil)
	cacher.SetArtworkRevisionTracker(tracker)

	_, err := cacher.CacheBytes(context.Background(), makeTestJPEG(t), CacheRequest{
		ProviderID:  testTMDBProviderID,
		ContentType: testMoviesContentType,
		ContentID:   "335984",
		ImageType:   metadata.ImagePoster,
	})
	if err == nil || !strings.Contains(err.Error(), "track artwork revision") {
		t.Fatalf("CacheBytes error = %v, want tracking failure", err)
	}
	if got := len(store.keys()); got != 0 {
		t.Fatalf("wrote %d objects, want 0", got)
	}
}

func TestCachePosterWritesPortableRevisionWithManifestLast(t *testing.T) {
	srv := startImageServer(t, makeTestJPEG(t), http.StatusOK)
	store := &mockStore{}
	c := newWithHTTPClient(store, srv.Client())

	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/poster.jpg",
		ProviderID:  "tmdb",
		ContentType: "movies",
		ContentID:   "550",
		ImageType:   metadata.ImagePoster,
	})
	if err != nil {
		t.Fatalf("Cache poster: %v", err)
	}
	if result.Thumbhash == "" {
		t.Error("Thumbhash is empty")
	}

	info := requirePortableKey(t, result.OriginalPath, "poster")
	if info.Revision != result.Revision {
		t.Errorf("original key revision = %q, want %q", info.Revision, result.Revision)
	}
	if result.BasePath != info.Directory {
		t.Errorf("BasePath = %q, want revision directory %q", result.BasePath, info.Directory)
	}
	wantDir := fmt.Sprintf("artwork/v1/objects/poster/%s/%s", result.Revision[:2], result.Revision)
	if info.Directory != wantDir {
		t.Errorf("revision directory = %q, want %q", info.Directory, wantDir)
	}
	if result.ManifestPath != wantDir+"/manifest.json" {
		t.Errorf("ManifestPath = %q, want %q", result.ManifestPath, wantDir+"/manifest.json")
	}

	keys := store.keys()
	// Four variants plus the manifest.
	if len(keys) != 5 {
		t.Fatalf("wrote %d objects, want 5: %v", len(keys), keys)
	}
	for _, variant := range []string{"original", "w780", "w500", "w300"} {
		want := result.VariantPaths[variant]
		if want != wantDir+"/"+variant+".webp" {
			t.Errorf("%s key = %q, want %q", variant, want, wantDir+"/"+variant+".webp")
		}
		if !hasKey(keys, want) {
			t.Errorf("missing object %q in %v", want, keys)
		}
		if got := store.mediaType(want); got != "image/webp" {
			t.Errorf("%s media type = %q, want image/webp", variant, got)
		}
	}
	// The completeness marker is only meaningful if it lands after every
	// object it vouches for.
	if last := keys[len(keys)-1]; last != result.ManifestPath {
		t.Fatalf("last write = %q, want the manifest %q", last, result.ManifestPath)
	}
	if got := store.mediaType(result.ManifestPath); got != "application/json" {
		t.Errorf("manifest media type = %q, want application/json", got)
	}
}

func TestCacheWritesValidatableManifest(t *testing.T) {
	store := &mockStore{}
	c := newWithHTTPClient(store, nil)

	result, err := c.CacheBytes(context.Background(), makeTestJPEG(t), CacheRequest{
		ProviderID:  testTMDBProviderID,
		ContentType: testMoviesContentType,
		ContentID:   "550",
		ImageType:   metadata.ImageBackdrop,
	})
	if err != nil {
		t.Fatalf("CacheBytes: %v", err)
	}

	manifest, err := artworkkey.ParseManifest(store.objectData(result.ManifestPath))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if manifest.Revision != result.Revision {
		t.Errorf("manifest revision = %q, want %q", manifest.Revision, result.Revision)
	}
	if manifest.ImageType != "backdrop" || manifest.MediaType != "image/webp" {
		t.Errorf("manifest = %+v, want backdrop/image/webp", manifest)
	}
	if manifest.RecipeVersion != artworkkey.PortableRecipeVersion {
		t.Errorf("manifest recipe = %q, want %q", manifest.RecipeVersion, artworkkey.PortableRecipeVersion)
	}
	if len(manifest.Variants) != len(result.VariantPaths) {
		t.Errorf("manifest lists %d variants, want %d", len(manifest.Variants), len(result.VariantPaths))
	}
	// The written objects must re-derive the revision the manifest claims —
	// this is exactly the check an adopting server runs against a copied tree.
	if err := artworkkey.ValidateManifestObjects(context.Background(), manifest, store.read); err != nil {
		t.Fatalf("ValidateManifestObjects: %v", err)
	}
	// The manifest is a closed vocabulary: nothing identifying, locating, or
	// secret may ride along in an extra field.
	raw := store.objectData(result.ManifestPath)
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	wantFields := []string{"format_version", "image_type", "media_type", "recipe_version", "revision", "variants"}
	gotFields := slices.Sorted(maps.Keys(document))
	if !slices.Equal(gotFields, wantFields) {
		t.Fatalf("manifest fields = %v, want %v", gotFields, wantFields)
	}
	var variants []map[string]json.RawMessage
	if err := json.Unmarshal(document["variants"], &variants); err != nil {
		t.Fatalf("unmarshal manifest variants: %v", err)
	}
	wantVariantFields := []string{"digest", "filename", "name", "size_bytes"}
	for _, variant := range variants {
		if got := slices.Sorted(maps.Keys(variant)); !slices.Equal(got, wantVariantFields) {
			t.Fatalf("manifest variant fields = %v, want %v", got, wantVariantFields)
		}
	}
	for _, forbidden := range []string{"tmdb", "http", "://", "bucket"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Errorf("manifest %s contains %q", raw, forbidden)
		}
	}
}

func TestCacheSkipsWritingVariantsThatAlreadyExist(t *testing.T) {
	srv := startImageServer(t, makeTestJPEG(t), http.StatusOK)
	store := &mockStore{}
	c := newWithHTTPClient(store, srv.Client())

	req := CacheRequest{
		SourceURL:   srv.URL + "/poster.jpg",
		ProviderID:  "tmdb",
		ContentType: "movies",
		ContentID:   "550",
		ImageType:   metadata.ImagePoster,
	}
	first, err := c.Cache(context.Background(), req)
	if err != nil {
		t.Fatalf("prime immutable variants: %v", err)
	}
	store.setExisting(first.VariantPaths["original"], first.VariantPaths["w780"], first.VariantPaths["w500"], first.VariantPaths["w300"])
	store.resetCalls()

	result, err := c.Cache(context.Background(), req)
	if err != nil {
		t.Fatalf("Cache poster with existing variants: %v", err)
	}
	if result.OriginalPath != first.OriginalPath {
		t.Fatalf("re-cached original = %q, want the same immutable key %q", result.OriginalPath, first.OriginalPath)
	}
	// Only the manifest is rewritten: re-materializing is how an incomplete
	// revision (variants stored, manifest lost) heals itself.
	if got := store.keys(); len(got) != 1 || got[0] != result.ManifestPath {
		t.Fatalf("wrote %v, want only the manifest %q", got, result.ManifestPath)
	}
	if result.UploadedVariants != 0 || result.ExistingVariants != 4 {
		t.Fatalf("write stats = uploaded %d existing %d, want uploaded 0 existing 4", result.UploadedVariants, result.ExistingVariants)
	}
	for _, key := range []string{result.VariantPaths["original"], result.VariantPaths["w780"], result.VariantPaths["w500"], result.VariantPaths["w300"]} {
		if !hasKey(store.checkedKeys(), key) {
			t.Fatalf("content match was not checked for %q; checked %v", key, store.checkedKeys())
		}
	}
}

func TestCacheWritesOnlyMissingVariants(t *testing.T) {
	srv := startImageServer(t, makeTestJPEG(t), http.StatusOK)
	store := &mockStore{}
	c := newWithHTTPClient(store, srv.Client())

	req := CacheRequest{
		SourceURL:   srv.URL + "/poster.jpg",
		ProviderID:  "tmdb",
		ContentType: "movies",
		ContentID:   "550",
		ImageType:   metadata.ImagePoster,
	}
	first, err := c.Cache(context.Background(), req)
	if err != nil {
		t.Fatalf("prime immutable variants: %v", err)
	}
	store.setExisting(first.VariantPaths["original"], first.VariantPaths["w780"], first.VariantPaths["w500"])
	store.resetCalls()

	result, err := c.Cache(context.Background(), req)
	if err != nil {
		t.Fatalf("Cache poster with partial existing variants: %v", err)
	}
	want := []string{result.VariantPaths["w300"], result.ManifestPath}
	if got := store.keys(); !slices.Equal(got, want) {
		t.Fatalf("wrote %v, want %v", got, want)
	}
	if result.UploadedVariants != 1 || result.ExistingVariants != 3 {
		t.Fatalf("write stats = uploaded %d existing %d, want uploaded 1 existing 3", result.UploadedVariants, result.ExistingVariants)
	}
}

func TestCacheDifferentContentCreatesDifferentImmutableRevision(t *testing.T) {
	store := &mockStore{}
	c := newWithHTTPClient(store, http.DefaultClient)
	req := CacheRequest{ProviderID: testTMDBProviderID, ContentType: testMoviesContentType, ContentID: "550", ImageType: metadata.ImagePoster}

	first, err := c.CacheBytes(context.Background(), makeTestJPEG(t), req)
	if err != nil {
		t.Fatalf("cache first poster: %v", err)
	}
	second, err := c.CacheBytes(context.Background(), makeTestPNG(t, 400, 600), req)
	if err != nil {
		t.Fatalf("cache replacement poster: %v", err)
	}
	if first.Revision == second.Revision || first.OriginalPath == second.OriginalPath {
		t.Fatalf("different content reused revision: first=%q second=%q", first.OriginalPath, second.OriginalPath)
	}
	if got := store.keys(); len(got) != 10 {
		t.Fatalf("wrote %v, want both immutable revisions in full (4 variants + manifest each)", got)
	}
}

// Identity fields no longer decide storage: the same encoded bytes are the
// same object no matter which item, season, language, or sidecar produced
// them, and are stored and counted once.
func TestCacheConvergesOnBytesNotIdentity(t *testing.T) {
	data := makeTestJPEG(t)
	store := &mockStore{}
	c := newWithHTTPClient(store, nil)

	season, episode := 2, 5
	requests := []CacheRequest{
		{ProviderID: "tmdb", ContentType: "movies", ContentID: "550", ImageType: metadata.ImagePoster},
		{ProviderID: "metadb", ContentType: "series", ContentID: "1396", ImageType: metadata.ImagePoster},
		{ProviderID: "tmdb", ContentType: "series", ContentID: "1396", ImageType: metadata.ImagePoster, SeasonNumber: &season, EpisodeNumber: &episode},
		{ProviderID: "tmdb", ContentType: "series", ContentID: "1396", ImageType: metadata.ImagePoster, Language: "fr-CA"},
		{ProviderID: "local", ContentType: "movies", ContentID: "movie-1", ImageType: metadata.ImagePoster, KeyDiscriminator: "deadbeef"},
	}
	var want string
	for i, req := range requests {
		result, err := c.CacheBytes(context.Background(), data, req)
		if err != nil {
			t.Fatalf("CacheBytes %d: %v", i, err)
		}
		if i == 0 {
			want = result.OriginalPath
			continue
		}
		if result.OriginalPath != want {
			t.Fatalf("request %d stored at %q, want the content-addressed key %q", i, result.OriginalPath, want)
		}
	}
}

// The same bytes under a different image type are a different recipe and must
// not share a revision: the ladders differ, and the type is part of the digest.
func TestCacheImageTypeParticipatesInRevision(t *testing.T) {
	data := makeTestJPEG(t)
	c := newWithHTTPClient(&mockStore{}, nil)
	req := CacheRequest{ProviderID: "tmdb", ContentType: "movies", ContentID: "550"}

	req.ImageType = metadata.ImagePoster
	poster, err := c.CacheBytes(context.Background(), data, req)
	if err != nil {
		t.Fatalf("cache poster: %v", err)
	}
	req.ImageType = metadata.ImageBackdrop
	backdrop, err := c.CacheBytes(context.Background(), data, req)
	if err != nil {
		t.Fatalf("cache backdrop: %v", err)
	}
	if poster.Revision == backdrop.Revision {
		t.Fatalf("poster and backdrop share revision %q", poster.Revision)
	}
	requirePortableKey(t, poster.OriginalPath, "poster")
	requirePortableKey(t, backdrop.OriginalPath, "backdrop")
}

func TestCacheVariantLadders(t *testing.T) {
	tests := []struct {
		name      string
		imageType metadata.ImageType
		typeName  string
		want      []string
		forbidden []string
	}{
		{"poster", metadata.ImagePoster, "poster", []string{"original", "w780", "w500", "w300"}, []string{"w1280"}},
		{"backdrop", metadata.ImageBackdrop, "backdrop", []string{"original", "w1920", "w1280", "w300"}, []string{"w500"}},
		{"logo", metadata.ImageLogo, "logo", []string{"original", "w1280", "w500"}, []string{"w300"}},
		{"profile", metadata.ImageProfile, "profile", []string{"original", "w500", "w300"}, []string{"w1920"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := startImageServer(t, makeTestJPEG(t), http.StatusOK)
			store := &mockStore{}
			c := newWithHTTPClient(store, srv.Client())

			result, err := c.Cache(context.Background(), CacheRequest{
				SourceURL:   srv.URL + "/art.jpg",
				ProviderID:  "tmdb",
				ContentType: "movies",
				ContentID:   "550",
				ImageType:   tc.imageType,
			})
			if err != nil {
				t.Fatalf("Cache: %v", err)
			}
			info := requirePortableKey(t, result.OriginalPath, tc.typeName)
			if len(result.VariantPaths) != len(tc.want) {
				t.Fatalf("variants = %v, want %v", result.VariantPaths, tc.want)
			}
			keys := store.keys()
			for _, variant := range tc.want {
				key := result.VariantPaths[variant]
				if key != info.Directory+"/"+variant+".webp" {
					t.Errorf("%s key = %q", variant, key)
				}
				if !hasKey(keys, key) {
					t.Errorf("missing object %q in %v", key, keys)
				}
			}
			for _, variant := range tc.forbidden {
				if _, ok := result.VariantPaths[variant]; ok {
					t.Errorf("%s should not have a %s variant", tc.typeName, variant)
				}
			}
			// GC expansion from the stored key alone must cover exactly what
			// was written, manifest included.
			expanded := artworkkey.ObjectKeys(result.OriginalPath, tc.typeName)
			sort.Strings(expanded)
			sort.Strings(keys)
			if !slices.Equal(expanded, keys) {
				t.Fatalf("ObjectKeys = %v, want the written set %v", expanded, keys)
			}
		})
	}
}

func TestCacheConvertsSVGLogo(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="400" viewBox="0 0 1200 400"><rect width="1200" height="400" fill="#111"/><text x="80" y="255" fill="#fff" font-family="Arial" font-size="180">SILO</text></svg>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(svg)
	}))
	t.Cleanup(srv.Close)

	store := &mockStore{}
	c := newWithHTTPClient(store, srv.Client())

	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/logo.svg",
		ProviderID:  "tmdb",
		ContentType: "series",
		ContentID:   "1396",
		ImageType:   metadata.ImageLogo,
	})
	if err != nil {
		t.Fatalf("Cache SVG logo: %v", err)
	}
	if result.Thumbhash == "" {
		t.Fatal("Thumbhash is empty")
	}
	if result.Ext != ".webp" {
		t.Fatalf("Ext = %q, want .webp", result.Ext)
	}
	for _, variant := range []string{"original", "w500"} {
		if !hasKey(store.keys(), result.VariantPaths[variant]) {
			t.Errorf("missing object %q in %v", result.VariantPaths[variant], store.keys())
		}
	}
}

func TestCacheCapsLargeOriginalVariant(t *testing.T) {
	pngData := makeTestPNG(t, 2600, 900)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngData)
	}))
	t.Cleanup(srv.Close)

	store := &mockStore{}
	c := newWithHTTPClient(store, srv.Client())

	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/logo.png",
		ProviderID:  "tmdb",
		ContentType: "series",
		ContentID:   "1396",
		ImageType:   metadata.ImageLogo,
	})
	if err != nil {
		t.Fatalf("Cache large logo: %v", err)
	}
	original := store.objectData(result.OriginalPath)
	if len(original) == 0 {
		t.Fatal("missing original.webp write")
	}
	size, err := bimg.NewImage(original).Size()
	if err != nil {
		t.Fatalf("reading original.webp size: %v", err)
	}
	if size.Width > 1920 {
		t.Fatalf("original.webp width = %d, want <= 1920", size.Width)
	}
	if len(original) >= 10*1024*1024 {
		t.Fatalf("original.webp size = %d bytes, want < 10 MiB", len(original))
	}
}

func TestCacheDownloadError(t *testing.T) {
	srv := startImageServer(t, nil, http.StatusNotFound)
	store := &mockStore{}
	c := newWithHTTPClient(store, srv.Client())

	_, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/missing.jpg",
		ProviderID:  "tmdb",
		ContentType: "movies",
		ContentID:   "999",
		ImageType:   metadata.ImagePoster,
	})
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
}

func TestCacheStoreWriteError(t *testing.T) {
	srv := startImageServer(t, makeTestJPEG(t), http.StatusOK)
	store := &mockStore{writeErr: errors.New("storage: connection refused")}
	c := newWithHTTPClient(store, srv.Client())

	_, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/poster.jpg",
		ProviderID:  "tmdb",
		ContentType: "movies",
		ContentID:   "550",
		ImageType:   metadata.ImagePoster,
	})
	if err == nil {
		t.Fatal("expected error for storage write failure, got nil")
	}
}

func TestCacheRejectsEmptyContentID(t *testing.T) {
	srv := startImageServer(t, makeTestJPEG(t), http.StatusOK)
	store := &mockStore{}
	c := newWithHTTPClient(store, srv.Client())

	_, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/poster.jpg",
		ProviderID:  "tmdb",
		ContentType: "series",
		ContentID:   "",
		ImageType:   metadata.ImagePoster,
	})
	if err == nil {
		t.Fatal("expected error for empty content ID, got nil")
	}
	if len(store.keys()) != 0 {
		t.Fatalf("expected no writes for empty content ID, got %v", store.keys())
	}
}

func TestCacheRejectsEpisodeWithoutSeason(t *testing.T) {
	store := &mockStore{}
	c := New(store)

	episode := 5
	_, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:     "https://example.com/still.jpg",
		ProviderID:    "tmdb",
		ContentType:   "series",
		ContentID:     "1396",
		ImageType:     metadata.ImageStill,
		EpisodeNumber: &episode,
	})
	if err == nil {
		t.Fatal("expected error for EpisodeNumber without SeasonNumber, got nil")
	}
	if len(store.keys()) != 0 {
		t.Fatalf("expected no writes for invalid request, got %v", store.keys())
	}
}

func TestCacheResolvesPluginURL(t *testing.T) {
	srv := startImageServer(t, makeTestJPEG(t), http.StatusOK)
	store := &mockStore{}
	c := newWithHTTPClient(store, srv.Client())

	resolver := stubResolver{httpURL: srv.URL + "/from-resolver.jpg"}
	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:     "tmdb://poster/abc.jpg",
		ProviderID:    "tmdb",
		ContentType:   "movies",
		ContentID:     "550",
		ImageType:     metadata.ImagePoster,
		ImageResolver: resolver,
	})
	if err != nil {
		t.Fatalf("Cache plugin URL: %v", err)
	}
	requirePortableKey(t, result.OriginalPath, "poster")
}

func TestCacheRetriesTransientWriteFailure(t *testing.T) {
	srv := startImageServer(t, makeTestJPEG(t), http.StatusOK)
	store := &mockStore{failuresBeforeSuccess: 1}
	c := newWithHTTPClient(store, srv.Client())

	_, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:     srv.URL + "/still.jpg",
		ProviderID:    "tmdb",
		ContentType:   "series",
		ContentID:     "1396",
		ImageType:     metadata.ImageStill,
		SeasonNumber:  intPointer(1),
		EpisodeNumber: intPointer(1),
	})
	if err != nil {
		t.Fatalf("Cache() error = %v", err)
	}
	if len(store.keys()) == 0 {
		t.Fatal("expected writes after retry")
	}
}

// A content mismatch on a content-addressed key cannot be fixed by trying
// again, and burning backoff on it delays the real failure.
func TestCacheDoesNotRetryContentMismatch(t *testing.T) {
	store := &countingStore{err: artworkstore.ErrContentMismatch}
	c := newWithHTTPClient(store, nil)

	_, err := c.CacheBytes(context.Background(), makeTestJPEG(t), CacheRequest{
		ProviderID:  "tmdb",
		ContentType: "movies",
		ContentID:   "550",
		ImageType:   metadata.ImageLogo,
	})
	if !errors.Is(err, artworkstore.ErrContentMismatch) {
		t.Fatalf("CacheBytes error = %v, want ErrContentMismatch", err)
	}
	// The logo ladder is three variants written concurrently; each may attempt
	// exactly once.
	if got := store.attempts(); got > 3 {
		t.Fatalf("write attempts = %d, want at most one per variant", got)
	}
}

type countingStore struct {
	mu    sync.Mutex
	count int
	err   error
}

func (s *countingStore) WriteImmutable(_ context.Context, _ string, _ []byte, _ artworkstore.ObjectMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	return s.err
}

func (s *countingStore) Matches(context.Context, string, []byte) (bool, error) { return false, nil }

func (s *countingStore) attempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func intPointer(v int) *int {
	return &v
}

type stubResolver struct {
	httpURL string
}

func (s stubResolver) ResolveImageURL(_ context.Context, _ string, _ string) string {
	return s.httpURL
}
