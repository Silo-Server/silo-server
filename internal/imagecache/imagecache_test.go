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
	"testing/synctest"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/netguard"
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

func (m *mockS3) Put(ctx context.Context, key string, data []byte) error {
	return m.PutObject(ctx, m.bucket, key, data)
}

func (m *mockS3) Bucket() string { return m.bucket }
func (m *mockS3) Matches(ctx context.Context, key string, data []byte) (bool, error) {
	return m.ObjectMatches(ctx, m.bucket, key, data)
}

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

func TestCacheBytesTracksPendingThenExactRevision(t *testing.T) {
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
	if len(calls) != 2 {
		t.Fatalf("tracker calls = %d, want pending and complete manifests", len(calls))
	}
	if calls[0].originalPath != result.OriginalPath {
		t.Fatalf("tracked original = %q, want %q", calls[0].originalPath, result.OriginalPath)
	}
	if calls[0].imageType != "poster" {
		t.Fatalf("tracked image type = %q, want poster", calls[0].imageType)
	}
	if len(calls[0].objectKeys) != 0 {
		t.Fatalf("pending keys = %v, want none before uploads complete", calls[0].objectKeys)
	}
	wantKeys := make([]string, 0, len(result.VariantPaths))
	for _, key := range result.VariantPaths {
		wantKeys = append(wantKeys, key)
	}
	sort.Strings(wantKeys)
	if !slices.Equal(calls[1].objectKeys, wantKeys) {
		t.Fatalf("completed keys = %v, want %v", calls[1].objectKeys, wantKeys)
	}
}

func TestCacheBytesUploadFailureLeavesManifestPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s3 := &mockS3{bucket: "artwork", putErr: errors.New("storage unavailable")}
		tracker := &recordingRevisionTracker{}
		cacher := newWithHTTPClient(s3, nil)
		cacher.SetArtworkRevisionTracker(tracker)

		_, err := cacher.CacheBytes(context.Background(), makeTestJPEG(t), CacheRequest{
			ProviderID:  testTMDBProviderID,
			ContentType: testMoviesContentType,
			ContentID:   "335984",
			ImageType:   metadata.ImagePoster,
		})
		if err == nil || !strings.Contains(err.Error(), "storage unavailable") {
			t.Fatalf("CacheBytes error = %v, want upload failure", err)
		}
		calls := tracker.recorded()
		if len(calls) != 1 {
			t.Fatalf("tracker calls = %d, want only the pending manifest", len(calls))
		}
		if len(calls[0].objectKeys) != 0 {
			t.Fatalf("tracked keys = %v, want no completion proof after upload failure", calls[0].objectKeys)
		}
	})
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

// Without private access the cache refuses a local-network source, including
// a host name that only resolves to one when the transport dials it. With
// private access it still refuses a blocked address such as cloud metadata.
func TestCacheRefusesPrivateSources(t *testing.T) {
	srv := startImageServer(t, makeTestJPEG(t), http.StatusOK)
	for _, tc := range []struct {
		name          string
		sourceURL     string
		privateAccess bool
		want          error
	}{
		{"local network", srv.URL + "/poster.jpg", false, netguard.ErrPrivateDestination},
		{"host resolving to local network", strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/poster.jpg", false, netguard.ErrPrivateDestination},
		{"blocked with private access", "http://169.254.169.254/latest/poster.jpg", true, netguard.ErrBlockedDestination},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.privateAccess {
				ctx = netguard.WithPrivateAccess(ctx)
			}
			s3 := &mockS3{bucket: "media"}
			_, err := New(s3).Cache(ctx, CacheRequest{
				SourceURL:   tc.sourceURL,
				ProviderID:  "tmdb",
				ContentType: "movies",
				ContentID:   "550",
				ImageType:   metadata.ImagePoster,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Cache error = %v, want %v", err, tc.want)
			}
			if got := s3.keys(); len(got) != 0 {
				t.Fatalf("uploaded keys = %v, want none", got)
			}
		})
	}
}

// Provider artwork URLs are untrusted, so the cache refuses every address
// netguard does not class as Public: CGNAT (Tailscale), cloud metadata, and
// NAT64 forms of either. The context is already canceled, so an address the
// check misses fails with context.Canceled instead of being dialed.
func TestCacheRefusesNonPublicSourcesWithoutPrivateAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, host := range []string{
		"100.64.1.1",
		"100.101.102.103",
		"100.100.100.200",
		"198.18.0.1",
		"0.1.2.3",
		"[64:ff9b::a9fe:a9fe]",
		"[64:ff9b::a00:5]",
		"[::ffff:100.64.1.1]",
	} {
		t.Run(host, func(t *testing.T) {
			s3 := &mockS3{bucket: "media"}
			_, err := New(s3).Cache(ctx, CacheRequest{
				SourceURL:   "http://" + host + "/poster.jpg",
				ProviderID:  "tmdb",
				ContentType: "movies",
				ContentID:   "550",
				ImageType:   metadata.ImagePoster,
			})
			if !errors.Is(err, netguard.ErrPrivateDestination) && !errors.Is(err, netguard.ErrBlockedDestination) {
				t.Fatalf("Cache error = %v, want a netguard refusal", err)
			}
		})
	}
}

func TestCacheFetchesLocalNetworkSourceWithPrivateAccess(t *testing.T) {
	srv := startImageServer(t, makeTestJPEG(t), http.StatusOK)
	s3 := &mockS3{bucket: "media"}
	c := New(s3)

	result, err := c.Cache(netguard.WithPrivateAccess(context.Background()), CacheRequest{
		SourceURL:   srv.URL + "/poster.jpg",
		ProviderID:  "tmdb",
		ContentType: "movies",
		ContentID:   "550",
		ImageType:   metadata.ImagePoster,
	})
	if err != nil {
		t.Fatalf("Cache with private access: %v", err)
	}
	if result.BasePath != "tmdb/movies/550/poster" {
		t.Fatalf("BasePath = %q, want tmdb/movies/550/poster", result.BasePath)
	}
	if got := s3.keys(); len(got) != 4 {
		t.Fatalf("uploaded keys = %v, want 4 variants", got)
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
	if got := s3.keys(); len(got) != 8 {
		t.Fatalf("uploaded keys = %v, want both immutable four-variant revisions", got)
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

type stubResolver struct {
	httpURL string
}

func (s stubResolver) ResolveImageURL(_ context.Context, _ string, _ string) string {
	return s.httpURL
}

type countingArtworkStore struct {
	*blobstore.Filesystem
	mu     sync.Mutex
	writes int
}

func (s *countingArtworkStore) Put(ctx context.Context, key string, data []byte) error {
	s.mu.Lock()
	s.writes++
	s.mu.Unlock()
	return s.Filesystem.Put(ctx, key, data)
}

func newWithHTTPClient(s3 ObjectPutter, client *http.Client) *Cacher {
	if client == nil {
		client = http.DefaultClient
	}
	return &Cacher{s3: s3, httpClient: client}
}
