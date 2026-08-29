package abs

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

func TestABSSubjectNormalization(t *testing.T) {
	for _, test := range []struct {
		name   string
		userID string
		want   streamtelemetry.Subject
	}{
		// A positive integer is a silo account id, so it lands in the same
		// subject space native, compat and proxy publish — that is what lets a
		// per-user total sum across families (§4.2b).
		{"positive account id", "42", streamtelemetry.UserSubject(42)},
		// "0" and "-1" parse but name no account. Merging them into the shared
		// user space would attribute ABS bytes to a user that does not exist.
		{"zero", "0", streamtelemetry.Subject{Kind: streamtelemetry.SubjectABSUser, ID: "0"}},
		{"negative", "-1", streamtelemetry.Subject{Kind: streamtelemetry.SubjectABSUser, ID: "-1"}},
		{"non numeric", "abc", streamtelemetry.Subject{Kind: streamtelemetry.SubjectABSUser, ID: "abc"}},
		{"overflow", "99999999999999999999", streamtelemetry.Subject{Kind: streamtelemetry.SubjectABSUser, ID: "99999999999999999999"}},
		{"empty", "", streamtelemetry.Subject{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := absSubject(test.userID); got != test.want {
				t.Fatalf("absSubject(%q) = %+v, want %+v", test.userID, got, test.want)
			}
		})
	}
}

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
func absTelemetryServer(t testing.TB, registry *streamtelemetry.Registry, deps Dependencies) *httptest.Server {
	t.Helper()
	handler := New(deps)
	handler.SetStreamTelemetry(registry)
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.Use(httpstream.CompressExcept(5, SkipMediaCompression))
	handler.Mount(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
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

func getWithHeaders(t *testing.T, client *http.Client, method, url string, headers map[string]string) absResponse {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return absResponse{status: resp.StatusCode, header: resp.Header.Clone(), body: buf.Bytes()}
}

func TestMountedABSRouterAttributesPublicTrack(t *testing.T) {
	body := []byte("\xff\xfb\x00\x00" + strings.Repeat("audio", 400))
	registry := telemetryRegistry(t)
	deps := absPublicTrackDeps(t, "sid-telemetry", "book-1", "42", body)
	server := absTelemetryServer(t, registry, deps)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)

	got := getWithHeaders(t, client, http.MethodGet, server.URL+"/public/session/sid-telemetry/track/1",
		map[string]string{"X-Silo-Client": "Silo Audiobooks", "X-Silo-Client-Version": "3.1.0"})
	if got.status != http.StatusOK || !bytes.Equal(got.body, body) {
		t.Fatalf("GET = %d, %d bytes (want %d)", got.status, len(got.body), len(body))
	}

	// Byte totals come from Sweep, not Snapshot: SessionView.BytesAccepted is
	// lastSweptBytes and only the sweep folds live observations into it.
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("sessions = %+v", snapshot.Sessions)
	}
	session := snapshot.Sessions[0]
	if session.SessionID != "sid-telemetry" {
		t.Fatalf("session id = %q", session.SessionID)
	}
	if session.Subject != streamtelemetry.UserSubject(42) || session.ProfileID != "profile-1" {
		t.Fatalf("subject = %+v profile = %q", session.Subject, session.ProfileID)
	}
	if session.MediaFileID != 77 {
		t.Fatalf("media file id = %d, want 77", session.MediaFileID)
	}
	if session.PlayMethod != "direct" {
		t.Fatalf("play method = %q", session.PlayMethod)
	}
	if session.StartedAtSource != streamtelemetry.StartedAtSourceSession {
		t.Fatalf("started source = %q, want the ABS session's own start", session.StartedAtSource)
	}
	// ABS public tracks carry no signed stream token; claiming a verified
	// issued-at would be a fabrication.
	if session.TokenIssuedAtSources[streamtelemetry.TokenIssuedAtSourceNone] != 1 {
		t.Fatalf("token sources = %+v", session.TokenIssuedAtSources)
	}
	if len(session.Routes) != 1 || session.Routes[0].Role != streamtelemetry.RoleViewerEgress || !session.Routes[0].CapRelevant {
		t.Fatalf("routes = %+v", session.Routes)
	}
	if session.Routes[0].BytesAccepted != int64(len(body)) {
		t.Fatalf("bytes = %d, want %d", session.Routes[0].BytesAccepted, len(body))
	}
	if len(session.ClientVariants) != 1 || session.ClientVariants[0].Name != "Silo Audiobooks" || session.ClientVariants[0].Version != "3.1.0" {
		t.Fatalf("client variants = %+v", session.ClientVariants)
	}
}

func TestMountedABSRouterPublicTrackEdgeCases(t *testing.T) {
	body := []byte("\xff\xfb\x00\x00audio-bytes")

	t.Run("head counts zero bytes but one request", func(t *testing.T) {
		registry := telemetryRegistry(t)
		server := absTelemetryServer(t, registry, absPublicTrackDeps(t, "sid-head", "book-1", "42", body))
		got := getWithHeaders(t, server.Client(), http.MethodHead, server.URL+"/public/session/sid-head/track/1", nil)
		if got.status != http.StatusOK || len(got.body) != 0 {
			t.Fatalf("HEAD = %d, %d bytes", got.status, len(got.body))
		}
		snapshot := registry.Sweep()
		if len(snapshot.Sessions) != 1 {
			t.Fatalf("sessions = %+v", snapshot.Sessions)
		}
		if snapshot.Sessions[0].BytesAccepted != 0 || snapshot.Sessions[0].RequestCount != 1 {
			t.Fatalf("HEAD accounting = bytes %d requests %d",
				snapshot.Sessions[0].BytesAccepted, snapshot.Sessions[0].RequestCount)
		}
	})

	t.Run("range is byte exact", func(t *testing.T) {
		large := bytes.Repeat([]byte("abcdefghij"), 300_000)
		registry := telemetryRegistry(t)
		server := absTelemetryServer(t, registry, absPublicTrackDeps(t, "sid-range", "book-1", "42", large))
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		t.Cleanup(client.CloseIdleConnections)
		got := getWithHeaders(t, client, http.MethodGet, server.URL+"/public/session/sid-range/track/1",
			map[string]string{"Range": "bytes=1000-2999"})
		if got.status != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", got.status)
		}
		if want := fmt.Sprintf("bytes 1000-2999/%d", len(large)); got.header.Get("Content-Range") != want {
			t.Fatalf("Content-Range = %q, want %q", got.header.Get("Content-Range"), want)
		}
		if !bytes.Equal(got.body, large[1000:3000]) {
			t.Fatalf("range body mismatch: %d bytes", len(got.body))
		}
		if snapshot := registry.Sweep(); len(snapshot.Sessions) != 1 || snapshot.Sessions[0].BytesAccepted != 2000 {
			t.Fatalf("range accounting = %+v", snapshot.Sessions)
		}
	})

	// A rejected request must create no LOGICAL activity. It does not produce an
	// empty snapshot: the observer still saw the request and the error body, and
	// that is deliberately retained in the unattributed counters.
	for _, test := range []struct {
		name       string
		sid        string
		closed     bool
		wantStatus int
	}{
		{"unknown session", "sid-missing", false, http.StatusNotFound},
		{"closed session", "sid-closed", true, http.StatusGone},
	} {
		t.Run(test.name+" creates no logical activity", func(t *testing.T) {
			registry := telemetryRegistry(t)
			deps := absPublicTrackDeps(t, "sid-real", "book-1", "42", body)
			if test.closed {
				closedAt := time.Now()
				store := deps.PlaybackSessionStore.(*fakePlaybackSessionStore)
				_ = store.InsertPlaybackSession(context.Background(), ABSPlaybackSession{
					ID: test.sid, UserID: "42", ContentID: "book-1", ClosedAt: &closedAt,
				})
			}
			server := absTelemetryServer(t, registry, deps)
			got := getWithHeaders(t, server.Client(), http.MethodGet,
				server.URL+"/public/session/"+test.sid+"/track/1", nil)
			if got.status != test.wantStatus {
				t.Fatalf("status = %d, want %d", got.status, test.wantStatus)
			}
			snapshot := registry.Sweep()
			if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 0 {
				t.Fatalf("rejected request created logical activity: %+v %+v", snapshot.Sessions, snapshot.Transfers)
			}
			if snapshot.UnattributedObservations == 0 {
				t.Fatal("rejected request was not counted as unattributed")
			}
		})
	}
}

// feedMediaStore resolves one media file by id for the RSS feed-file route.
type feedMediaStore struct {
	noopMediaStore
	file *models.MediaFile
}

func (f *feedMediaStore) GetMediaFileByID(_ context.Context, id int) (*models.MediaFile, error) {
	if f.file == nil || f.file.ID != id {
		return nil, ErrNotFound
	}
	return f.file, nil
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

// §4.2b: the RSS feed route has no authenticated caller — the slug is the
// capability — so the transfer must be attributed to the feed's owner.
func TestMountedABSRouterFeedFileResolvesOwner(t *testing.T) {
	body := []byte(strings.Repeat("feed-audio", 200))
	path := filepath.Join(t.TempDir(), "feed.mp3")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	registry := telemetryRegistry(t)
	server := absTelemetryServer(t, registry, Dependencies{
		MediaStore:   &feedMediaStore{file: &models.MediaFile{ID: 501, FilePath: path, ContentID: "book-9"}},
		RSSFeedStore: &feedStore{feed: RSSFeed{ID: "feed-1", UserID: "7", ProfileID: "profile-9", LibraryItemID: "book-9", Slug: "slug-9"}},
	})
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)

	got := getWithHeaders(t, client, http.MethodGet, server.URL+"/feed/slug-9/file/501", nil)
	if got.status != http.StatusOK || !bytes.Equal(got.body, body) {
		t.Fatalf("GET = %d, %d bytes", got.status, len(got.body))
	}
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 0 {
		t.Fatalf("feed file created a logical session: %+v", snapshot.Sessions)
	}
	if len(snapshot.Transfers) != 1 {
		t.Fatalf("transfers = %+v", snapshot.Transfers)
	}
	transfer := snapshot.Transfers[0]
	if transfer.Subject != streamtelemetry.UserSubject(7) || transfer.ProfileID != "profile-9" {
		t.Fatalf("feed transfer was not attributed to the feed owner: %+v", transfer)
	}
	if transfer.MediaFileID != 501 || transfer.BytesAccepted != int64(len(body)) {
		t.Fatalf("transfer = %+v", transfer)
	}
}

// The family gate is the kill switch that makes enrolling a family sharing the
// API process reversible without losing all observation.
func TestMountedABSRouterFamilyGate(t *testing.T) {
	body := []byte("\xff\xfb\x00\x00audio-bytes")
	registry := telemetryRegistry(t, streamtelemetry.FamilyNative)
	server := absTelemetryServer(t, registry, absPublicTrackDeps(t, "sid-gated", "book-1", "42", body))
	got := getWithHeaders(t, server.Client(), http.MethodGet, server.URL+"/public/session/sid-gated/track/1", nil)
	if got.status != http.StatusOK || len(got.body) == 0 {
		t.Fatalf("gated-out family broke serving: %d, %d bytes", got.status, len(got.body))
	}
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 0 || snapshot.UnattributedObservations != 0 {
		t.Fatalf("gated-out family still observed: %+v", snapshot)
	}
}
