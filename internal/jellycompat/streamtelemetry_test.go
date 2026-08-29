package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

const compatTelemetryToken = "compat-telemetry-token"

// compatTelemetryRegistry builds an enabled registry observing the jellycompat
// family. Every test that starts a registry must Stop it: the package-level now
// seam in streamtelemetry races leaked collector goroutines otherwise.
func compatTelemetryRegistry(t testing.TB, families ...streamtelemetry.Family) *streamtelemetry.Registry {
	t.Helper()
	cfg := streamtelemetry.DefaultConfig("compat-test")
	cfg.Enabled = true
	cfg.Retention = time.Minute
	if len(families) == 0 {
		families = []streamtelemetry.Family{streamtelemetry.FamilyJellycompat}
	}
	cfg.Families = make(map[streamtelemetry.Family]bool, len(families))
	for _, family := range families {
		cfg.Families[family] = true
	}
	registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
	t.Cleanup(func() { _ = registry.Stop(context.Background()) })
	return registry
}

type compatTelemetryFixture struct {
	server   *httptest.Server
	client   *http.Client
	registry *streamtelemetry.Registry
	itemID   string
	body     string
	store    CompatPlaybackStore
}

// newCompatTelemetryServer mounts the real compat router — global compression,
// PlaybackSessionAuth, request logging and all — behind a real socket. A
// handler-level test would bypass the middleware under test.
func newCompatTelemetryServer(t testing.TB, registry *streamtelemetry.Registry) compatTelemetryFixture {
	t.Helper()
	const body = "0123456789abcdefghijklmnopqrstuvwxyz"
	filePath := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(filePath, []byte(body), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	store := NewSessionStore(time.Hour, nil)
	if err := store.Put(Session{Token: compatTelemetryToken, StreamAppUserID: 91, ProfileID: "profile-7"}); err != nil {
		t.Fatalf("put compat session: %v", err)
	}
	codec := NewResourceIDCodec()
	const contentID = "telemetry-movie"
	detail := &upstreamItemDetail{
		ContentID: contentID, Type: "movie", Title: "Telemetry Movie",
		Versions: []catalog.FileVersion{{
			FileID: 42, FilePath: filePath, Container: "mp4",
			Duration: 3600, FileSize: int64(len(body)), AddedAt: time.Now(),
		}},
	}
	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	router := NewRouter(Dependencies{
		Config:          cfg,
		SessionStore:    store,
		IDCodec:         codec,
		ContentService:  &stubContentService{detail: detail},
		FileResolver:    testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: filePath}},
		SessionMgr:      &testCompatSessionManager{},
		PlaybackStore:   playbackStore,
		StreamTelemetry: registry,
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)
	return compatTelemetryFixture{
		server: server, client: client, registry: registry,
		itemID: codec.EncodeStringID(EncodedIDItem, contentID), body: body, store: playbackStore,
	}
}

// compatResponse carries just what the assertions need. Returning the live
// *http.Response would leak an unclosed body past this helper.
type compatResponse struct {
	status int
	body   string
}

func (f compatTelemetryFixture) get(t *testing.T, method, url string, headers map[string]string) compatResponse {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 0, 1024)
	chunk := make([]byte, 512)
	for {
		n, readErr := resp.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if readErr != nil {
			break
		}
	}
	return compatResponse{status: resp.StatusCode, body: string(buf)}
}

// playSessions exposes the fixture's compat play sessions so a test can assert
// telemetry is NOT keyed on their ids.
func (f compatTelemetryFixture) playSessions() []PlaybackSession {
	store, ok := f.store.(*PlaybackSessionStore)
	if !ok {
		return nil
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	out := make([]PlaybackSession, 0, len(store.sessions))
	for _, play := range store.sessions {
		out = append(out, play)
	}
	return out
}

func TestMountedCompatRouterAttributesDirectStream(t *testing.T) {
	registry := compatTelemetryRegistry(t)
	fixture := newCompatTelemetryServer(t, registry)
	mediaURL := fixture.server.URL + "/Videos/" + fixture.itemID + "/stream.mp4?static=true&api_key=" + compatTelemetryToken

	got := fixture.get(t, http.MethodGet, mediaURL, map[string]string{
		"X-Emby-Authorization": `MediaBrowser Client="Jellyfin Web", Device="Chrome", DeviceId="device-abc", Version="10.11.6"`,
	})
	if got.status != http.StatusOK || got.body != fixture.body {
		t.Fatalf("GET = %d %q", got.status, got.body)
	}

	// Sweep, not Snapshot: BytesAccepted is lastSweptBytes.
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("sessions = %+v", snapshot.Sessions)
	}
	session := snapshot.Sessions[0]
	// Session.StreamAppUserID is the numeric silo account id, so compat lands in
	// the same subject space as native and proxy and a per-user total sums.
	if session.Subject != streamtelemetry.UserSubject(91) || session.ProfileID != "profile-7" {
		t.Fatalf("identity = %+v", session)
	}
	// The canonical key is the UPSTREAM playback session id (what the proxy,
	// nodesessions and playback_sessions_sync all publish), not the compat
	// PlaybackSession.ID. Keying on the latter splits one viewing into two
	// merged sessions and makes every compat session look telemetry_only.
	if session.SessionID != "upstream-started" {
		t.Fatalf("session id = %q, want the upstream playback session id", session.SessionID)
	}
	for _, play := range fixture.playSessions() {
		if session.SessionID == play.ID {
			t.Fatalf("telemetry keyed on the compat play session id %q", play.ID)
		}
		if play.UpstreamSessionID != session.SessionID {
			t.Fatalf("play session upstream id = %q, telemetry session id = %q", play.UpstreamSessionID, session.SessionID)
		}
	}
	if session.MediaFileID != 42 {
		t.Fatalf("media file id = %d", session.MediaFileID)
	}
	if len(session.Routes) != 1 || session.Routes[0].Role != streamtelemetry.RoleViewerEgress {
		t.Fatalf("routes = %+v", session.Routes)
	}
	if session.Routes[0].BytesAccepted != int64(len(fixture.body)) {
		t.Fatalf("bytes = %d, want %d", session.Routes[0].BytesAccepted, len(fixture.body))
	}
	// The compat token is a session token, not a signed stream token with an iat
	// this path verifies.
	if session.TokenIssuedAtSources[streamtelemetry.TokenIssuedAtSourceNone] == 0 {
		t.Fatalf("token sources = %+v", session.TokenIssuedAtSources)
	}
	// Client identity comes from the MediaBrowser authorization header — the same
	// parser the negotiation path uses for DeviceId — not X-Silo-Client*.
	if len(session.DeviceIDs) != 1 || session.DeviceIDs[0] != "device-abc" {
		t.Fatalf("device ids = %+v", session.DeviceIDs)
	}
	if len(session.ClientVariants) != 1 || session.ClientVariants[0].Name != "Jellyfin Web" || session.ClientVariants[0].Version != "10.11.6" {
		t.Fatalf("client variants = %+v", session.ClientVariants)
	}
}

func TestMountedCompatRouterDownloadIsATransfer(t *testing.T) {
	registry := compatTelemetryRegistry(t)
	fixture := newCompatTelemetryServer(t, registry)
	url := fixture.server.URL + "/Items/" + fixture.itemID + "/Download?api_key=" + compatTelemetryToken

	got := fixture.get(t, http.MethodGet, url, nil)
	if got.status != http.StatusOK || got.body != fixture.body {
		t.Fatalf("GET = %d %q", got.status, got.body)
	}
	snapshot := registry.Sweep()
	// §4.2b: a download has a user but no stable playback session.
	if len(snapshot.Sessions) != 0 {
		t.Fatalf("download created a logical session: %+v", snapshot.Sessions)
	}
	if len(snapshot.Transfers) != 1 {
		t.Fatalf("transfers = %+v", snapshot.Transfers)
	}
	transfer := snapshot.Transfers[0]
	if transfer.Subject != streamtelemetry.UserSubject(91) || transfer.MediaFileID != 42 {
		t.Fatalf("transfer = %+v", transfer)
	}
	if transfer.BytesAccepted != int64(len(fixture.body)) {
		t.Fatalf("transfer bytes = %d", transfer.BytesAccepted)
	}
}

func TestMountedCompatRouterBitrateTestIsACapExemptTransfer(t *testing.T) {
	registry := compatTelemetryRegistry(t)
	fixture := newCompatTelemetryServer(t, registry)

	got := fixture.get(t, http.MethodGet,
		fixture.server.URL+"/Playback/BitrateTest?api_key="+compatTelemetryToken, nil)
	if got.status != http.StatusOK || len(got.body) != 1024*1024 {
		t.Fatalf("bitrate probe = %d, %d bytes", got.status, len(got.body))
	}
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 1 {
		t.Fatalf("bitrate probe activity = %+v %+v", snapshot.Sessions, snapshot.Transfers)
	}
	if snapshot.Transfers[0].Subject != streamtelemetry.UserSubject(91) {
		t.Fatalf("bitrate probe subject = %+v", snapshot.Transfers[0].Subject)
	}
	// §4.2 "classify but exempt": transfer-observed, never cap-relevant.
	if snapshot.Transfers[0].BytesAccepted != 1024*1024 {
		t.Fatalf("bitrate probe bytes = %d", snapshot.Transfers[0].BytesAccepted)
	}
}

// Rejected requests create no logical activity. Where the rejection happens
// decides whether they are observed at all, and both halves are worth pinning:
// PlaybackSessionAuth is mounted as middleware OUTSIDE the per-route observer
// (router.go:256), so a 401 never reaches the wrapper and costs nothing, while a
// request that authenticates and then fails inside the handler is observed and
// counted as unattributed.
func TestMountedCompatRouterRejectedRequestsCreateNoLogicalActivity(t *testing.T) {
	t.Run("middleware 401 is never observed", func(t *testing.T) {
		registry := compatTelemetryRegistry(t)
		fixture := newCompatTelemetryServer(t, registry)

		got := fixture.get(t, http.MethodGet,
			fixture.server.URL+"/Videos/"+fixture.itemID+"/stream.mp4?static=true&api_key=not-a-token", nil)
		if got.status != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", got.status)
		}
		snapshot := registry.Sweep()
		if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 0 {
			t.Fatalf("401 created logical activity: %+v %+v", snapshot.Sessions, snapshot.Transfers)
		}
		if snapshot.UnattributedObservations != 0 || snapshot.UnattributedBytes != 0 {
			t.Fatalf("401 rejected by middleware should never reach the observer: %+v", snapshot)
		}
	})

	// This is the case that proves the attach sits after the play-session
	// resolution rather than at the top of the handler: the caller is
	// authenticated, so the observer runs, but no session is ever attached.
	t.Run("authenticated request with no resolvable play session", func(t *testing.T) {
		registry := compatTelemetryRegistry(t)
		fixture := newCompatTelemetryServer(t, registry)
		unknownItem := NewResourceIDCodec().EncodeStringID(EncodedIDItem, "no-such-item")

		got := fixture.get(t, http.MethodGet,
			fixture.server.URL+"/Videos/"+unknownItem+"/stream.mp4?static=true&api_key="+compatTelemetryToken, nil)
		if got.status == http.StatusOK {
			t.Fatal("unknown item served successfully")
		}
		snapshot := registry.Sweep()
		if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 0 {
			t.Fatalf("unresolvable play session created logical activity: %+v %+v", snapshot.Sessions, snapshot.Transfers)
		}
		if snapshot.UnattributedObservations == 0 {
			t.Fatal("an observed but never-attached request was not counted as unattributed")
		}
	})
}

func TestMountedCompatRouterHEADCountsZeroBytes(t *testing.T) {
	registry := compatTelemetryRegistry(t)
	fixture := newCompatTelemetryServer(t, registry)
	mediaURL := fixture.server.URL + "/Videos/" + fixture.itemID + "/stream.mp4?static=true&api_key=" + compatTelemetryToken

	got := fixture.get(t, http.MethodHead, mediaURL, nil)
	if got.status != http.StatusOK || got.body != "" {
		t.Fatalf("HEAD = %d %q", got.status, got.body)
	}
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("sessions = %+v", snapshot.Sessions)
	}
	if snapshot.Sessions[0].BytesAccepted != 0 || snapshot.Sessions[0].RequestCount != 1 {
		t.Fatalf("HEAD accounting = bytes %d requests %d",
			snapshot.Sessions[0].BytesAccepted, snapshot.Sessions[0].RequestCount)
	}
}

// The kill switch that makes enrolling a family which shares the API process
// with native reversible without losing all observation.
func TestMountedCompatRouterFamilyGate(t *testing.T) {
	registry := compatTelemetryRegistry(t, streamtelemetry.FamilyNative)
	fixture := newCompatTelemetryServer(t, registry)
	mediaURL := fixture.server.URL + "/Videos/" + fixture.itemID + "/stream.mp4?static=true&api_key=" + compatTelemetryToken

	got := fixture.get(t, http.MethodGet, mediaURL, nil)
	if got.status != http.StatusOK || got.body != fixture.body {
		t.Fatalf("gated-out family broke serving: %d %q", got.status, got.body)
	}
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 0 || snapshot.UnattributedObservations != 0 {
		t.Fatalf("gated-out family still observed: %+v", snapshot)
	}
}
