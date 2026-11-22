package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func TestFetchRemoteTranscodeCapabilitiesDoesNotForwardJWTOnRedirect(t *testing.T) {
	const jwt = "cluster-jwt-secret"
	var redirectTargetHits atomic.Int32
	var redirectTargetAuthorization atomic.Value
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetHits.Add(1)
		redirectTargetAuthorization.Store(r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{Resolved: "qsv"})
	}))
	defer redirectTarget.Close()

	var sourceSigned atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceSigned.Store(nodeauthtest.Signed(r, jwt))
		http.Redirect(w, r, redirectTarget.URL+"/hw-capabilities", http.StatusFound)
	}))
	defer source.Close()

	_, err := fetchRemoteNodeCapabilities(context.Background(), nodeauth.TargetTranscode, source.URL, nodeauthtest.NodeKey(jwt))
	if !sourceSigned.Load() {
		t.Fatal("source request was not signed with the shared secret")
	}
	if got := redirectTargetHits.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, Authorization = %#v; want no redirect request", got, redirectTargetAuthorization.Load())
	}
	if err == nil || !strings.Contains(err.Error(), "node returned 302") {
		t.Fatalf("redirect response error = %v, want node returned 302", err)
	}
}

func TestFetchRemoteTranscodeCapabilitiesRejectsOversizedResponse(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resolved":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 2<<20)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer remote.Close()

	_, err := fetchRemoteNodeCapabilities(context.Background(), nodeauth.TargetTranscode, remote.URL, nodeauthtest.NodeKey("cluster-jwt-secret"))
	if err == nil || !strings.Contains(err.Error(), "capability response exceeds") {
		t.Fatalf("oversized response error = %v, want bounded-response error", err)
	}
}

// A start a node refuses at its own job cap holds the node back from the next
// placement on this server; a draining refusal, which carries no reason, does
// not. Either way the caller gets the status and tries another executor.
func TestRemoteStartRefusedAtCapacityHoldsTheNode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		refusal  string
		wantHeld bool
	}{
		{name: "at capacity", refusal: nodepool.RefusalAtCapacity, wantHeld: true},
		{name: "draining", refusal: "", wantHeld: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.refusal != "" {
					w.Header().Set(nodepool.NodeRefusalHeader, tc.refusal)
				}
				http.Error(w, "refused", http.StatusServiceUnavailable)
			}))
			defer node.Close()
			transcodes := nodepool.NewTranscodePool()
			transcodes.SetKeyCheck(func(int) bool { return true })
			transcodes.SetNodes([]*nodepool.Node{{ID: 1, URL: node.URL, Enabled: true, Healthy: true}})
			planner := nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
			handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.NodePlanner = planner

			_, status, err := handler.startRemotePlaybackTransport(context.Background(), node.URL, transcodenode.TranscodeStartRequest{SessionID: "s"})
			if err != nil || status != http.StatusServiceUnavailable {
				t.Fatalf("start = %d, %v; want the node's 503", status, err)
			}
			if held := !planner.TranscodeWorkAvailableWith(nil); held != tc.wantHeld {
				t.Fatalf("node held back = %v, want %v", held, tc.wantHeld)
			}
		})
	}
}
