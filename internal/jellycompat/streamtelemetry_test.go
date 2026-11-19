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
	file := &models.MediaFile{ID: 42, ContentID: contentID, FilePath: filePath}
	router := NewRouter(Dependencies{
		Config:          cfg,
		SessionStore:    store,
		IDCodec:         codec,
		ContentService:  &stubContentService{detail: detail},
		FileResolver:    testCompatFileResolver{file: file},
		Downloads:       newTestDownloadService(true, true, file),
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
