package transcodenode

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// unreachableStreamDeny returns a marker store whose Redis never answers, so
// Deny populates only the in-process cache and every other lookup fails open.
// That is enough to prove the serve paths consult the marker before serving or
// reconstructing, without a Redis in the test environment.
func unreachableStreamDeny(t *testing.T) *playback.StreamDeny {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return playback.NewStreamDeny(client)
}

func nodeRequest(t *testing.T, server *Server, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+testSecret)
	if token != "" {
		req.Header.Set("X-Silo-Stream-Token", token)
	}
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, req)
	return rr
}

func TestStreamDenyRefusesHLSServeAndReconstruction(t *testing.T) {
	server := newTestServer(t)
	deny := unreachableStreamDeny(t)
	server.SetStreamDeny(deny)
	deny.Deny(t.Context(), "denied-transport")

	for _, path := range []string{"/transcode/denied-transport/master.m3u8", "/transcode/denied-transport/segment/seg_00001.m4s"} {
		rr := nodeRequest(t, server, http.MethodGet, path, "")
		if rr.Code != http.StatusGone || strings.TrimSpace(rr.Body.String()) != "playback session ended" {
			t.Fatalf("%s = %d %q, want 410 playback session ended", path, rr.Code, rr.Body.String())
		}
		if !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s Content-Type = %q, want text/plain", path, rr.Header().Get("Content-Type"))
		}
	}

	// The marker names the playback session; the URL names the transport. A
	// forwarded token ties them together so the transport is cut too.
	deny.Deny(t.Context(), "denied-playback")
	claims := streamtoken.Claims{SessionID: "denied-playback", TranscodeTransportID: "live-transport"}
	token, err := streamtoken.Sign(claims, testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rr := nodeRequest(t, server, http.MethodGet, "/transcode/live-transport/master.m3u8", token)
	if rr.Code != http.StatusGone {
		t.Fatalf("token-linked denied session = %d %q, want 410", rr.Code, rr.Body.String())
	}

	// An unknown session with an unreachable Redis fails open: the request
	// proceeds to the normal lookup, which finds nothing and answers 404.
	rr = nodeRequest(t, server, http.MethodGet, "/transcode/unknown-transport/master.m3u8", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("fail-open unknown session = %d %q, want 404", rr.Code, rr.Body.String())
	}
}

func TestStreamDenyRefusesProgressiveRemuxBeforeFFmpeg(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	deny := unreachableStreamDeny(t)
	server.SetStreamDeny(deny)
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nprintf node-remux-bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	claims := streamtoken.Claims{
		SessionID: "playback-1", MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: "transport-1",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy),
	}
	card := playback.RecipeCardFromClaims(&claims)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true})
	token, err := streamtoken.Sign(claims, testSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	deny.Deny(t.Context(), "playback-1")
	rr := nodeRequest(t, server, http.MethodGet, "/remux/transport-1?seek=2", token)
	if rr.Code != http.StatusGone || strings.TrimSpace(rr.Body.String()) != "playback session ended" {
		t.Fatalf("denied remux = %d %q, want 410 playback session ended", rr.Code, rr.Body.String())
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs after denied remux = %d, want 0", got)
	}
}

func TestStreamDenyNilStoreKeepsServing(t *testing.T) {
	server := newTestServer(t)
	rr := nodeRequest(t, server, http.MethodGet, "/transcode/unknown-transport/master.m3u8", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("no deny store = %d %q, want 404", rr.Code, rr.Body.String())
	}
}
