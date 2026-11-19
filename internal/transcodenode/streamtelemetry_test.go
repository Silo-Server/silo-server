package transcodenode

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

func telemetryNodeServer(t *testing.T) (*Server, *streamtelemetry.Registry, *httptest.Server) {
	t.Helper()
	srv := newTestServer(t)
	cfg := streamtelemetry.DefaultConfig("transcode-node-test")
	cfg.Enabled = true
	registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
	srv.SetStreamTelemetry(registry)
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = registry.Stop(context.Background()) })
	return srv, registry, server
}

func nodeMediaRequest(t *testing.T, server *httptest.Server, path, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	nodeauthtest.Sign(t, req, testNodeKey)
	if token != "" {
		req.Header.Set("X-Silo-Stream-Token", token)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func TestMountedTranscodeNodeSegmentTelemetry(t *testing.T) {
	tests := []struct {
		name        string
		identity    bool
		wantSession string
	}{
		{name: "playback session the start named", identity: true, wantSession: "viewer-session"},
		{name: "transport fallback", wantSession: "node-transport:transport-session"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, registry, server := telemetryNodeServer(t)
			const transportID = "transport-session"
			const segment = "node-segment-bytes"
			outputDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(outputDir, "seg1.ts"), []byte(segment), 0o600); err != nil {
				t.Fatal(err)
			}
			srv.mu.Lock()
			identity := sessionIdentity{}
			if test.identity {
				identity.playbackSessionID = "viewer-session"
			}
			srv.registerSessionLocked(transportID, playback.NewTranscodeSessionForTest(outputDir), identity, nil)
			srv.mu.Unlock()
			// A relay's stream token names nothing to the node any more.
			status, body := nodeMediaRequest(t, server, "/transcode/"+transportID+"/segment/seg1.ts", "relay-token")
			if status != http.StatusOK || string(body) != segment {
				t.Fatalf("segment = %d %q", status, body)
			}
			snapshot := registry.Sweep()
			if len(snapshot.Sessions) != 1 {
				t.Fatalf("sessions = %+v", snapshot.Sessions)
			}
			session := snapshot.Sessions[0]
			if session.SessionID != test.wantSession || session.Subject != (streamtelemetry.Subject{}) || session.ProfileID != "" || len(session.ViewerIPs) != 0 {
				t.Fatalf("session = %+v", session)
			}
			if len(session.Routes) != 1 || session.Routes[0].Role != streamtelemetry.RoleInternalRelay || session.Routes[0].BytesAccepted != int64(len(segment)) {
				t.Fatalf("routes = %+v", session.Routes)
			}
		})
	}
}
