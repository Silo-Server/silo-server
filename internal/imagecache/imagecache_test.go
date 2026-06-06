package imagecache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
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

// mockS3 records all PutObject calls for test assertions.
type mockS3 struct {
	mu     sync.Mutex
	calls  []putCall
	bucket string
	putErr error // if non-nil, returned for every PutObject call
}

type putCall struct {
	bucket string
	key    string
	size   int
}

func (m *mockS3) PutObject(_ context.Context, bucket, key string, data []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, putCall{bucket: bucket, key: key, size: len(data)})
	return nil
}

func (m *mockS3) Bucket() string { return m.bucket }

func (m *mockS3) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, len(m.calls))
	for i, c := range m.calls {
		keys[i] = c.key
	}
	return keys
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
		want := wantBase + "/" + variant + ".webp"
		if !hasKey(keys, want) {
			t.Errorf("missing S3 key %q in %v", want, keys)
		}
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
		want := wantBase + "/" + variant + ".webp"
		if !hasKey(keys, want) {
			t.Errorf("missing S3 key %q in %v", want, keys)
		}
	}
	// Must not have w500
	if hasKey(keys, wantBase+"/w500.webp") {
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
	// Expect 2 variants: original, w500 — NO w300 or w1280
	if len(keys) != 2 {
		t.Errorf("expected 2 uploaded variants, got %d: %v", len(keys), keys)
	}
	for _, variant := range []string{"original", "w500"} {
		want := wantBase + "/" + variant + ".webp"
		if !hasKey(keys, want) {
			t.Errorf("missing S3 key %q in %v", want, keys)
		}
	}
	for _, forbidden := range []string{"w300", "w1280"} {
		if hasKey(keys, wantBase+"/"+forbidden+".webp") {
			t.Errorf("logo should not have %s variant", forbidden)
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
		want := wantBase + "/" + variant + ".webp"
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
		want := wantBase + "/" + variant + ".webp"
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

	for _, season := range []int{1, 2, 3} {
		s := season
		_, err := c.Cache(context.Background(), CacheRequest{
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
	}

	keys := s3.keys()
	for _, season := range []int{1, 2, 3} {
		want := fmt.Sprintf("tmdb/series/1396/seasons/%d/poster/original.webp", season)
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

type stubResolver struct {
	httpURL string
}

func (s stubResolver) ResolveImageURL(_ context.Context, _ string, _ string) string {
	return s.httpURL
}

// Ensure the containsKey helper is used at least once (avoids unused warning).
var _ = containsKey
