package imagecache

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/h2non/bimg"

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

// mockS3 records all PutObject calls for test assertions.
type mockS3 struct {
	mu                    sync.Mutex
	calls                 []putCall
	bucket                string
	putErr                error // if non-nil, returned for every PutObject call
	failuresBeforeSuccess int
	existing              map[string]bool
	existsErr             error
	existsCalls           []string
}

type putCall struct {
	bucket string
	key    string
	size   int
	data   []byte
}

type trackedRevision struct {
	originalPath string
	imageType    string
	objectKeys   []string
}

type recordingRevisionTracker struct {
	mu    sync.Mutex
	calls []trackedRevision
	err   error
}

func (t *recordingRevisionTracker) TrackArtworkRevision(_ context.Context, originalPath, imageType string, objectKeys []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, trackedRevision{
		originalPath: originalPath,
		imageType:    imageType,
		objectKeys:   append([]string(nil), objectKeys...),
	})
	return t.err
}

func (t *recordingRevisionTracker) recorded() []trackedRevision {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]trackedRevision, len(t.calls))
	copy(result, t.calls)
	return result
}

func (m *mockS3) PutObject(_ context.Context, bucket, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failuresBeforeSuccess > 0 {
		m.failuresBeforeSuccess--
		return errors.New("temporary s3 failure")
	}
	if m.putErr != nil {
		return m.putErr
	}
	copied := append([]byte(nil), data...)
	m.calls = append(m.calls, putCall{bucket: bucket, key: key, size: len(data), data: copied})
	return nil
}

func (m *mockS3) Bucket() string { return m.bucket }

// ObjectMatches treats keys registered via setExisting as content matches;
// real content verification is exercised against the s3client implementation.
func (m *mockS3) ObjectMatches(_ context.Context, _ string, key string, _ []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.existsCalls = append(m.existsCalls, key)
	if m.existsErr != nil {
		return false, m.existsErr
	}
	return m.existing[key], nil
}

func (m *mockS3) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, len(m.calls))
	for i, c := range m.calls {
		keys[i] = c.key
	}
	return keys
}

func (m *mockS3) objectData(key string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.calls {
		if c.key == key {
			return c.data
		}
	}
	return nil
}

func (m *mockS3) checkedKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, len(m.existsCalls))
	copy(keys, m.existsCalls)
	return keys
}

func (m *mockS3) resetCalls() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
	m.existsCalls = nil
}

func TestCacheBytesTracksExactRevisionBeforeUpload(t *testing.T) {
	s3 := &mockS3{bucket: "artwork"}
	tracker := &recordingRevisionTracker{}
	cacher := newWithHTTPClient(s3, nil)
	cacher.SetArtworkRevisionTracker(tracker)

	result, err := cacher.CacheBytes(context.Background(), makeTestJPEG(t), CacheRequest{
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
	wantKeys := make([]string, 0, len(result.VariantPaths))
	for _, key := range result.VariantPaths {
		wantKeys = append(wantKeys, key)
	}
	sort.Strings(wantKeys)
	if !slices.Equal(calls[0].objectKeys, wantKeys) {
		t.Fatalf("tracked keys = %v, want %v", calls[0].objectKeys, wantKeys)
	}
}

func TestCacheBytesDoesNotUploadWhenRevisionTrackingFails(t *testing.T) {
	s3 := &mockS3{bucket: "artwork"}
	tracker := &recordingRevisionTracker{err: errors.New("registry unavailable")}
	cacher := newWithHTTPClient(s3, nil)
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
	if got := len(s3.keys()); got != 0 {
		t.Fatalf("uploaded objects = %d, want 0", got)
	}
}

func (m *mockS3) setExisting(keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.existing == nil {
		m.existing = make(map[string]bool)
	}
	for _, key := range keys {
		m.existing[key] = true
	}
}

func containsKey(keys []string, suffix string) bool {
	for _, k := range keys {
		if len(k) >= len(suffix) && k[len(k)-len(suffix):] == suffix {
			return true
		}
		// Also handle exact match
		if k == suffix {
			return true
		}
	}
	return false
}

func hasKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
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

func TestCache_Poster(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

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

	wantBase := "tmdb/movies/550/poster"
	if result.BasePath != wantBase {
		t.Errorf("BasePath = %q, want %q", result.BasePath, wantBase)
	}
	if result.Thumbhash == "" {
		t.Error("Thumbhash is empty")
	}

	keys := s3.keys()
	// Expect 3 variants: original, w500, w300
	if len(keys) != 3 {
		t.Errorf("expected 3 uploaded variants, got %d: %v", len(keys), keys)
	}
	for _, variant := range []string{"original", "w500", "w300"} {
		want := result.VariantPaths[variant]
		if !hasKey(keys, want) {
			t.Errorf("missing S3 key %q in %v", want, keys)
		}
	}
}

func TestCacheSkipsUploadingVariantsThatAlreadyExist(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	wantBase := "tmdb/movies/550/poster"
	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

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
	s3.setExisting(first.VariantPaths["original"], first.VariantPaths["w500"], first.VariantPaths["w300"])
	s3.resetCalls()
	result, err := c.Cache(context.Background(), req)
	if err != nil {
		t.Fatalf("Cache poster with existing variants: %v", err)
	}
	if result.BasePath != wantBase {
		t.Fatalf("BasePath = %q, want %q", result.BasePath, wantBase)
	}
	if got := s3.keys(); len(got) != 0 {
		t.Fatalf("uploaded keys = %v, want none when variants already exist", got)
	}
	if result.UploadedVariants != 0 || result.ExistingVariants != 3 {
		t.Fatalf("upload stats = uploaded %d existing %d, want uploaded 0 existing 3", result.UploadedVariants, result.ExistingVariants)
	}
	for _, key := range []string{result.VariantPaths["original"], result.VariantPaths["w500"], result.VariantPaths["w300"]} {
		if !hasKey(s3.checkedKeys(), key) {
			t.Fatalf("ObjectExists was not checked for %q; checked %v", key, s3.checkedKeys())
		}
	}
}

func TestCacheDifferentContentCreatesDifferentImmutableRevision(t *testing.T) {
	jpeg := makeTestJPEG(t)
	png := makeTestPNG(t, 400, 600)
	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, http.DefaultClient)
	req := CacheRequest{ProviderID: testTMDBProviderID, ContentType: testMoviesContentType, ContentID: "550", ImageType: metadata.ImagePoster}
	first, err := c.CacheBytes(context.Background(), jpeg, req)
	if err != nil {
		t.Fatalf("cache first poster: %v", err)
	}
	second, err := c.CacheBytes(context.Background(), png, req)
	if err != nil {
		t.Fatalf("cache replacement poster: %v", err)
	}
	if first.Revision == second.Revision || first.OriginalPath == second.OriginalPath {
		t.Fatalf("different content reused revision: first=%q second=%q", first.OriginalPath, second.OriginalPath)
	}
	if got := s3.keys(); len(got) != 6 {
		t.Fatalf("uploaded keys = %v, want both immutable three-variant revisions", got)
	}
}

func TestCacheUploadsOnlyMissingVariants(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

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
	s3.setExisting(first.VariantPaths["original"], first.VariantPaths["w500"])
	s3.resetCalls()
	result, err := c.Cache(context.Background(), req)
	if err != nil {
		t.Fatalf("Cache poster with partial existing variants: %v", err)
	}
	if got := s3.keys(); len(got) != 1 || got[0] != result.VariantPaths["w300"] {
		t.Fatalf("uploaded keys = %v, want only missing w300 variant", got)
	}
	if result.UploadedVariants != 1 || result.ExistingVariants != 2 {
		t.Fatalf("upload stats = uploaded %d existing %d, want uploaded 1 existing 2", result.UploadedVariants, result.ExistingVariants)
	}
}

func TestCache_Backdrop(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/backdrop.jpg",
		ProviderID:  "tmdb",
		ContentType: "movies",
		ContentID:   "550",
		ImageType:   metadata.ImageBackdrop,
	})
	if err != nil {
		t.Fatalf("Cache backdrop: %v", err)
	}

	wantBase := "tmdb/movies/550/backdrop"
	if result.BasePath != wantBase {
		t.Errorf("BasePath = %q, want %q", result.BasePath, wantBase)
	}

	keys := s3.keys()
	// Expect 4 variants: original, w1920, w1280, w300
	if len(keys) != 4 {
		t.Errorf("expected 4 uploaded variants, got %d: %v", len(keys), keys)
	}
	for _, variant := range []string{"original", "w1920", "w1280", "w300"} {
		want := result.VariantPaths[variant]
		if !hasKey(keys, want) {
			t.Errorf("missing S3 key %q in %v", want, keys)
		}
	}
	// Must not have w500
	if _, ok := result.VariantPaths["w500"]; ok {
		t.Error("backdrop should not have w500 variant")
	}
}

func TestCache_Logo(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/logo.png",
		ProviderID:  "tmdb",
		ContentType: "series",
		ContentID:   "1396",
		ImageType:   metadata.ImageLogo,
	})
	if err != nil {
		t.Fatalf("Cache logo: %v", err)
	}

	wantBase := "tmdb/series/1396/logo"
	if result.BasePath != wantBase {
		t.Errorf("BasePath = %q, want %q", result.BasePath, wantBase)
	}

	keys := s3.keys()
	// Expect 3 variants: original, w1280 (4K TV hero logos), w500 — NO w300
	if len(keys) != 3 {
		t.Errorf("expected 3 uploaded variants, got %d: %v", len(keys), keys)
	}
	for _, variant := range []string{"original", "w1280", "w500"} {
		want := result.VariantPaths[variant]
		if !hasKey(keys, want) {
			t.Errorf("missing S3 key %q in %v", want, keys)
		}
	}
	for _, forbidden := range []string{"w300"} {
		if _, ok := result.VariantPaths[forbidden]; ok {
			t.Errorf("logo should not have %s variant", forbidden)
		}
	}
}

func TestCache_ConvertsSVGLogo(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="400" viewBox="0 0 1200 400"><rect width="1200" height="400" fill="#111"/><text x="80" y="255" fill="#fff" font-family="Arial" font-size="180">SILO</text></svg>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(svg)
	}))
	t.Cleanup(srv.Close)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

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
	for _, variant := range []string{"original", "w1280", "w500"} {
		want := result.VariantPaths[variant]
		if !hasKey(s3.keys(), want) {
			t.Errorf("missing S3 key %q in %v", want, s3.keys())
		}
	}
}

func TestCache_CapsLargeOriginalVariant(t *testing.T) {
	pngData := makeTestPNG(t, 2600, 900)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngData)
	}))
	t.Cleanup(srv.Close)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

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
	original := s3.objectData(result.OriginalPath)
	if len(original) == 0 {
		t.Fatal("missing original.webp upload")
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

func TestCache_LocalizedPosterUsesLanguageScopedPath(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/poster-fr.jpg",
		ProviderID:  "tmdb",
		ContentType: "series",
		ContentID:   "1396",
		ImageType:   metadata.ImagePoster,
		Language:    "fr-CA",
	})
	if err != nil {
		t.Fatalf("Cache localized poster: %v", err)
	}

	wantBase := "tmdb/series/1396/localizations/fr-ca/poster"
	if result.BasePath != wantBase {
		t.Errorf("BasePath = %q, want %q", result.BasePath, wantBase)
	}
	if !hasKey(s3.keys(), result.OriginalPath) {
		t.Errorf("missing localized original in %v", s3.keys())
	}
}

func TestCache_ProfileUsesProfileImagePath(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/person.jpg",
		ProviderID:  "tmdb",
		ContentType: "people",
		ContentID:   "287",
		ImageType:   metadata.ImageProfile,
	})
	if err != nil {
		t.Fatalf("Cache profile: %v", err)
	}

	wantBase := "tmdb/people/287/profile"
	if result.BasePath != wantBase {
		t.Errorf("BasePath = %q, want %q", result.BasePath, wantBase)
	}
	for _, variant := range []string{"original", "w500", "w300"} {
		want := result.VariantPaths[variant]
		if !hasKey(s3.keys(), want) {
			t.Errorf("missing S3 key %q in %v", want, s3.keys())
		}
	}
}

func TestCache_DownloadError(t *testing.T) {
	srv := startImageServer(t, nil, http.StatusNotFound)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

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

func TestCache_S3UploadError(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{
		bucket: "media",
		putErr: errors.New("s3: connection refused"),
	}
	c := newWithHTTPClient(s3, srv.Client())

	_, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/poster.jpg",
		ProviderID:  "tmdb",
		ContentType: "movies",
		ContentID:   "550",
		ImageType:   metadata.ImagePoster,
	})
	if err == nil {
		t.Fatal("expected error for S3 upload failure, got nil")
	}
}

func TestCache_RejectsEmptyContentID(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

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
	if len(s3.keys()) != 0 {
		t.Fatalf("expected no uploads for empty content ID, got %v", s3.keys())
	}
}

func TestCache_SeasonPoster_NestsUnderSeries(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

	season := 2
	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:    srv.URL + "/season2.jpg",
		ProviderID:   "tmdb",
		ContentType:  "series",
		ContentID:    "1396",
		ImageType:    metadata.ImagePoster,
		SeasonNumber: &season,
	})
	if err != nil {
		t.Fatalf("Cache season poster: %v", err)
	}

	wantBase := "tmdb/series/1396/seasons/2/poster"
	if result.BasePath != wantBase {
		t.Errorf("BasePath = %q, want %q", result.BasePath, wantBase)
	}
	for _, variant := range []string{"original", "w500", "w300"} {
		want := result.VariantPaths[variant]
		if !hasKey(s3.keys(), want) {
			t.Errorf("missing S3 key %q in %v", want, s3.keys())
		}
	}
}

func TestCache_EpisodeStill_NestsUnderSeasonAndEpisode(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

	season, episode := 2, 5
	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:     srv.URL + "/s02e05.jpg",
		ProviderID:    "tmdb",
		ContentType:   "series",
		ContentID:     "1396",
		ImageType:     metadata.ImageStill,
		SeasonNumber:  &season,
		EpisodeNumber: &episode,
	})
	if err != nil {
		t.Fatalf("Cache episode still: %v", err)
	}

	wantBase := "tmdb/series/1396/seasons/2/episodes/5/still"
	if result.BasePath != wantBase {
		t.Errorf("BasePath = %q, want %q", result.BasePath, wantBase)
	}
	for _, variant := range []string{"original", "w500", "w300"} {
		want := result.VariantPaths[variant]
		if !hasKey(s3.keys(), want) {
			t.Errorf("missing S3 key %q in %v", want, s3.keys())
		}
	}
}

func TestCache_SeasonsDoNotCollide(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

	originalPaths := make(map[int]string)
	for _, season := range []int{1, 2, 3} {
		s := season
		result, err := c.Cache(context.Background(), CacheRequest{
			SourceURL:    srv.URL + "/season.jpg",
			ProviderID:   "tmdb",
			ContentType:  "series",
			ContentID:    "1396",
			ImageType:    metadata.ImagePoster,
			SeasonNumber: &s,
		})
		if err != nil {
			t.Fatalf("Cache season %d: %v", season, err)
		}
		originalPaths[season] = result.OriginalPath
	}

	keys := s3.keys()
	for _, season := range []int{1, 2, 3} {
		want := originalPaths[season]
		if !hasKey(keys, want) {
			t.Errorf("missing S3 key %q in %v", want, keys)
		}
	}
}

func TestCache_SeasonZero_Specials(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

	// Season 0 is the conventional "specials" season — must produce a
	// distinct key, not be confused with a missing season dimension.
	specials := 0
	result, err := c.Cache(context.Background(), CacheRequest{
		SourceURL:    srv.URL + "/specials.jpg",
		ProviderID:   "tmdb",
		ContentType:  "series",
		ContentID:    "1396",
		ImageType:    metadata.ImagePoster,
		SeasonNumber: &specials,
	})
	if err != nil {
		t.Fatalf("Cache specials poster: %v", err)
	}
	if result.BasePath != "tmdb/series/1396/seasons/0/poster" {
		t.Errorf("BasePath = %q, want %q", result.BasePath, "tmdb/series/1396/seasons/0/poster")
	}

	// Cache an item-level series poster (no season dimension) and confirm
	// it lands under a distinct key from the specials poster.
	c2 := newWithHTTPClient(&mockS3{bucket: "media"}, srv.Client())
	itemResult, err := c2.Cache(context.Background(), CacheRequest{
		SourceURL:   srv.URL + "/series-poster.jpg",
		ProviderID:  "tmdb",
		ContentType: "series",
		ContentID:   "1396",
		ImageType:   metadata.ImagePoster,
	})
	if err != nil {
		t.Fatalf("Cache item poster: %v", err)
	}
	if itemResult.BasePath == result.BasePath {
		t.Errorf("specials and item-level posters share a base path %q", result.BasePath)
	}
	if itemResult.BasePath != "tmdb/series/1396/poster" {
		t.Errorf("item BasePath = %q, want %q", itemResult.BasePath, "tmdb/series/1396/poster")
	}
}

func TestCache_RejectsEpisodeWithoutSeason(t *testing.T) {
	s3 := &mockS3{bucket: "media"}
	c := New(s3)

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
	if len(s3.keys()) != 0 {
		t.Fatalf("expected no uploads for invalid request, got %v", s3.keys())
	}
}

func TestCache_ResolvesPluginURL(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media"}
	c := newWithHTTPClient(s3, srv.Client())

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
	if result.BasePath != "tmdb/movies/550/poster" {
		t.Errorf("BasePath = %q, want %q", result.BasePath, "tmdb/movies/550/poster")
	}
}

func TestCacheRetriesTransientPutObjectFailure(t *testing.T) {
	jpeg := makeTestJPEG(t)
	srv := startImageServer(t, jpeg, http.StatusOK)

	s3 := &mockS3{bucket: "media", failuresBeforeSuccess: 1}
	c := newWithHTTPClient(s3, srv.Client())

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
	if len(s3.keys()) == 0 {
		t.Fatal("expected uploads after retry")
	}
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

// Ensure the containsKey helper is used at least once (avoids unused warning).
var _ = containsKey
