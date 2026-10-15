package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
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
		identity    sessionIdentity
		wantSession string
	}{
		{name: "viewer session from the start request", identity: sessionIdentity{playbackSessionID: "viewer-session"}, wantSession: "viewer-session"},
		// A session started without one is filed under its transport, which
		// is the playback session's own id for a jellycompat transcode.
		{name: "no playback session", wantSession: "transport-session"},
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
			srv.registerSessionLocked(transportID, playback.NewTranscodeSessionForTest(outputDir), test.identity, nil)
			srv.mu.Unlock()
			// A forwarded token is not read: identity comes from the start.
			token := signedNodeTelemetryToken(t, "token-session", transportID, testSecret)
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

// A native transcode runs on a node under a plan-scoped transport id, and the
// relay forwards no token for a header-authenticated attempt. The node files
// its bytes and its session record under the viewer's playback session the
// start request names, and creates no session of its own.
func TestNativeTranscodeTelemetryUsesTheViewerPlaybackSession(t *testing.T) {
	srv, registry, server := telemetryNodeServer(t)
	tracker := nodesessions.NewLocalTracker(nil)
	srv.tracker = tracker
	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.watcher.Config().Playback.FFmpegPath = ffmpegPath
	const playbackSessionID = "viewer-playback-session"
	const transportID = playbackSessionID + "-plan1234-abcd1234"
	body, err := json.Marshal(TranscodeStartRequest{
		SessionID: transportID, PlaybackSessionID: playbackSessionID, UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
		SourceGrant: "/media/movie.mkv",
		InputPath:   "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body))
	nodeauthtest.Sign(t, start, testSecret)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, start)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rr.Code, rr.Body.String())
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	const segment = "native-segment-bytes"
	if err := os.WriteFile(filepath.Join(srv.sessionOutputDir(transportID), "seg1.ts"), []byte(segment), 0o600); err != nil {
		t.Fatal(err)
	}
	status, got := nodeMediaRequest(t, server, "/transcode/"+transportID+"/segment/seg1.ts", "")
	if status != http.StatusOK || string(got) != segment {
		t.Fatalf("segment = %d %q", status, got)
	}
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].SessionID != playbackSessionID {
		t.Fatalf("telemetry sessions = %+v, want only %q", snapshot.Sessions, playbackSessionID)
	}
	records := tracker.Snapshot()
	if len(records) != 1 {
		t.Fatalf("session records = %+v", records)
	}
	if record := records[0]; record.SessionID != transportID || record.AuthUserID != 7 || record.ProfileID != "profile-1" || record.MediaFileID != 42 {
		t.Fatalf("session record = %+v, want the transport owned by the start request's viewer", record)
	}
	if calls := srv.core().resolveCalls(); calls != 0 {
		t.Fatalf("a started transcode asked the core %d times for its identity", calls)
	}
}

func TestMountedTranscodeNodeProgressiveRemuxTelemetry(t *testing.T) {
	srv, registry, server := telemetryNodeServer(t)
	srv.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nprintf node-remux-bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.watcher.Config().Playback.FFmpegPath = ffmpegPath
	claims := streamtoken.Claims{
		SessionID: "viewer-session", MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: "transport-session",
		RoutingWorkload:        string(noderouting.WorkloadRemux),
		RoutingExecution:       string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11,
		RoutingEgress:          string(noderouting.EgressProxy),
	}
	card := playback.RecipeCardFromClaims(&claims)
	srv.useCoreRecipes(&stubRecipeStore{card: &card, ok: true})
	token, err := streamtoken.Sign(claims, testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	status, body := nodeMediaRequest(t, server, "/remux/transport-session", token)
	if status != http.StatusOK || string(body) != "node-remux-bytes" {
		t.Fatalf("remux = %d %q", status, body)
	}
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].SessionID != "viewer-session" {
		t.Fatalf("sessions = %+v", snapshot.Sessions)
	}
	routes := snapshot.Sessions[0].Routes
	if len(routes) != 1 || routes[0].Role != streamtelemetry.RoleInternalRelay || routes[0].BytesAccepted != int64(len(body)) {
		t.Fatalf("routes = %+v", routes)
	}
}

func TestMountedTranscodeNodeArtifactTelemetry(t *testing.T) {
	t.Run("successful relay", func(t *testing.T) {
		srv, registry, server := telemetryNodeServer(t)
		const artifactID = "telemetry-artifact"
		const body = "artifact-bytes"
		if err := os.MkdirAll(srv.artifactRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(srv.artifactRoot, artifactID+".mp4"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		status, got := nodeMediaRequest(t, server, "/downloads/artifacts/"+artifactID, "")
		if status != http.StatusOK || string(got) != body {
			t.Fatalf("artifact = %d %q", status, got)
		}
		snapshot := registry.Sweep()
		if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 1 {
			t.Fatalf("snapshot = %+v", snapshot)
		}
		transfer := snapshot.Transfers[0]
		if transfer.Role != streamtelemetry.RoleInternalRelay || transfer.BytesAccepted != int64(len(body)) || transfer.Subject != (streamtelemetry.Subject{}) || transfer.ViewerIP != "" {
			t.Fatalf("transfer = %+v", transfer)
		}
	})

	t.Run("missing artifact", func(t *testing.T) {
		_, registry, server := telemetryNodeServer(t)
		status, _ := nodeMediaRequest(t, server, "/downloads/artifacts/missing-artifact", "")
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		snapshot := registry.Sweep()
		if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 0 {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	})
}

func TestMountedTranscodeNodeUnknownSessionCreatesNothing(t *testing.T) {
	_, registry, server := telemetryNodeServer(t)
	status, _ := nodeMediaRequest(t, server, "/transcode/unknown/segment/seg1.ts", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d", status)
	}
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
