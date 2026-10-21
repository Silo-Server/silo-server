package transcodenode

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
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
	nodeauthtest.Sign(t, req, testSecret)
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

func signedNodeTelemetryToken(t *testing.T, sessionID, transportID, secret string) string {
	t.Helper()
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID: sessionID, TranscodeTransportID: transportID, PlayMethod: string(playback.PlayTranscode),
		UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
	}, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestMountedTranscodeNodeSegmentTelemetry(t *testing.T) {
	tests := []struct {
		name        string
		withToken   bool
		wantSession string
	}{
		{name: "canonical viewer session", withToken: true, wantSession: "viewer-session"},
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
			srv.sessions[transportID] = playback.NewTranscodeSessionForTest(outputDir)
			srv.mu.Unlock()
			token := ""
			if test.withToken {
				token = signedNodeTelemetryToken(t, "viewer-session", transportID, testSecret)
			}
			status, body := nodeMediaRequest(t, server, "/transcode/"+transportID+"/segment/seg1.ts", token)
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

func TestCanonicalSessionID(t *testing.T) {
	srv := newTestServer(t)
	const transportID = "transport-id"
	tests := []struct {
		name      string
		token     string
		want      string
		wantClaim bool
	}{
		{name: "matching", token: signedNodeTelemetryToken(t, "viewer-id", transportID, testSecret), want: "viewer-id", wantClaim: true},
		{name: "different transport", token: signedNodeTelemetryToken(t, "viewer-id", "other", testSecret), want: "node-transport:" + transportID},
		{name: "wrong secret", token: signedNodeTelemetryToken(t, "viewer-id", transportID, "wrong"), want: "node-transport:" + transportID},
		{name: "no header", want: "node-transport:" + transportID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/transcode/"+transportID+"/master.m3u8", nil)
			if test.token != "" {
				req.Header.Set("X-Silo-Stream-Token", test.token)
			}
			got, claims := srv.canonicalSessionID(req, transportID)
			if got != test.want || (claims != nil) != test.wantClaim {
				t.Fatalf("canonical = %q, claims=%+v", got, claims)
			}
		})
	}
}
