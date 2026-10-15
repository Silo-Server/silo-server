package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A transcode node checks no deny marker, so the native relays to one must: a
// denied session whose node runtime is still alive (its stop DELETE failed)
// gets 410 on the manifest and segment relays, and the node is never asked.
// The node serves a plan-scoped transport id distinct from the playback
// session id, and a marker under either refuses the relay.
func TestNativeTranscodeRelaysRefuseADeniedSessionOnAliveNode(t *testing.T) {
	for _, deniedID := range []string{"playback session", "transport"} {
		t.Run(deniedID, func(t *testing.T) {
			var nodeHits atomic.Int32
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nodeHits.Add(1)
				_, _ = w.Write([]byte("#EXTM3U\n"))
			}))
			t.Cleanup(node.Close)

			manager := playback.NewSessionManager(0, 0)
			session, err := manager.StartSession(1, "profile-1", 100, playback.PlayTranscode, false)
			if err != nil {
				t.Fatal(err)
			}
			transportID := session.ID + "-plan0001"
			if err := manager.SetTranscodeRoute(session.ID, playback.TranscodeRoute{NodeURL: node.URL, TransportID: transportID}); err != nil {
				t.Fatal(err)
			}
			// The marker store's Redis never answers, so Deny fills only this
			// process's cache: enough to prove the relays consult it first.
			client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
			t.Cleanup(func() { _ = client.Close() })
			deny := playback.NewStreamDeny(client)
			handler := NewPlaybackHandler(manager)
			handler.StreamDeny = deny
			handler.NodeKeys = nodeauthtest.SharedSecret("relay-deny-secret")
			if deniedID == "transport" {
				deny.Deny(context.Background(), transportID)
			} else {
				deny.Deny(context.Background(), session.ID)
			}

			params := map[string]string{"session_id": session.ID, "name": "segment0.m4s"}
			for name, relay := range map[string]http.HandlerFunc{
				"manifest": handler.HandleGetTranscodeManifest,
				"segment":  handler.HandleGetTranscodeSegment,
			} {
				rr := httptest.NewRecorder()
				relay(rr, playbackTestRequest(http.MethodGet, "/", nil, params))
				if rr.Code != http.StatusGone {
					t.Fatalf("%s of a denied remote session = %d %s, want 410", name, rr.Code, rr.Body.String())
				}
			}
			if got := nodeHits.Load(); got != 0 {
				t.Fatalf("node hits = %d: a relay forwarded a denied session", got)
			}
		})
	}
}

// A native remote stop deletes the transport's node recipe before it tells
// the node.
func TestNativeRemoteStopDeletesTheNodeRecipe(t *testing.T) {
	var deletes atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(node.Close)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.NodeKeys = nodeauthtest.SharedSecret("native-stop-secret")
	store := &recordingRecipeCardStoreV3{cards: map[string]playback.RecipeCard{"transport-1": {}}}
	handler.NodeRecipeStore = store

	handler.TranscodeManager().StopRemoteTranscode("transport-1", node.URL)
	if len(store.deleted) != 1 || store.deleted[0] != "transport-1" || deletes.Load() != 1 {
		t.Fatalf("recipe deletes = %v, node DELETEs = %d", store.deleted, deletes.Load())
	}
}
