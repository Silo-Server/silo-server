package abs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// telemetryRegistry builds an enabled registry observing the ABS family. Every
// test that starts one must Stop it: the package-level now seam in
// streamtelemetry races leaked collector goroutines otherwise.
func telemetryRegistry(t testing.TB, families ...streamtelemetry.Family) *streamtelemetry.Registry {
	t.Helper()
	cfg := streamtelemetry.DefaultConfig("abs-test")
	cfg.Enabled = true
	cfg.Retention = time.Minute
	if len(families) == 0 {
		families = []streamtelemetry.Family{streamtelemetry.FamilyABS}
	}
	cfg.Families = make(map[streamtelemetry.Family]bool, len(families))
	for _, family := range families {
		cfg.Families[family] = true
	}
	registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
	t.Cleanup(func() { _ = registry.Stop(context.Background()) })
	return registry
}

// absTelemetryServer mounts the real ABS router — access log, compression and
// all — behind a real socket. Handler-level tests bypass the middleware under
// test, which is how this project once shipped a feature that was a no-op for
// weeks.
func absTelemetryServer(t testing.TB, registry *streamtelemetry.Registry, deps Dependencies) *absServer {
	t.Helper()
	handler := New(deps)
	handler.SetStreamTelemetry(registry)
	handler.SetDownloads(newDownloadService(true, &downloadAccounts{}))
	server := &absServer{registry: registry}
	router := chi.NewRouter()
	// Outermost on purpose: it returns only after every inner handler,
	// including the telemetry observer's deferred release, has run.
	router.Use(server.trackInFlight)
	router.Use(middleware.Recoverer)
	router.Use(httpstream.CompressExcept(SkipMediaCompression))
	handler.Mount(router)
	server.Server = httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

// absServer is the mounted router plus a count of requests its handlers have
// not finished. The client sees the last body byte before the handler's
// deferred telemetry release has run, so a Sweep taken straight after client.Do
// can fold an observation whose bytes and outcome are not recorded yet.
type absServer struct {
	*httptest.Server
	registry *streamtelemetry.Registry
	inFlight atomic.Int64
}

func (s *absServer) trackInFlight(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.inFlight.Add(1)
		defer s.inFlight.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// settledSweep sweeps the registry once the server has finished every request
// the test sent — the only point at which the totals are final.
func (s *absServer) settledSweep(t testing.TB) streamtelemetry.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for s.inFlight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d request(s) still in flight after 10s", s.inFlight.Load())
		}
		time.Sleep(time.Millisecond)
	}
	return s.registry.Sweep()
}

func absPublicTrackDeps(t testing.TB, sid, contentID, userID string, body []byte) Dependencies {
	t.Helper()
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakePlaybackSessionStore{}
	_ = store.InsertPlaybackSession(context.Background(), ABSPlaybackSession{
		ID: sid, UserID: userID, ProfileID: "profile-1", ContentID: contentID,
		StartedAt: time.Now().Add(-time.Hour).UTC().Truncate(time.Second),
	})
	return Dependencies{
		MediaStore:           &filesMediaStore{contentID: contentID, files: []*models.MediaFile{{ID: 77, FilePath: path}}},
		PlaybackSessionStore: store,
	}
}

// absResponse carries just what the assertions need. Returning the live
// *http.Response instead would leak an unclosed body past this helper.
type absResponse struct {
	status int
	header http.Header
	body   []byte
}

// feedMediaStore serves one item with one media file for the RSS feed-file
// route.
type feedMediaStore struct {
	noopMediaStore
	file *models.MediaFile
}

func (f *feedMediaStore) GetAudiobookByID(_ context.Context, id string, _ catalog.AccessFilter) (*models.MediaItem, error) {
	if f.file == nil || f.file.ContentID != id {
		return nil, nil
	}
	return &models.MediaItem{ContentID: id}, nil
}

func (f *feedMediaStore) GetMediaFiles(_ context.Context, id string, _ catalog.AccessFilter) ([]*models.MediaFile, error) {
	if f.file == nil || f.file.ContentID != id {
		return nil, nil
	}
	return []*models.MediaFile{f.file}, nil
}

type feedStore struct {
	feed RSSFeed
}

func (f *feedStore) GetFeedBySlug(_ context.Context, slug string) (RSSFeed, error) {
	if f.feed.Slug != slug {
		return RSSFeed{}, ErrNotFound
	}
	return f.feed, nil
}
func (f *feedStore) CreateFeed(context.Context, RSSFeed) error { return nil }
func (f *feedStore) CloseFeed(context.Context, string) error   { return nil }
func (f *feedStore) GetFeed(context.Context, string) (RSSFeed, error) {
	return RSSFeed{}, ErrNotFound
}
func (f *feedStore) ListUserFeeds(context.Context, string, string) ([]RSSFeed, error) {
	return nil, nil
}
