package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesource/nodesourcetest"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// A remote start carries the grant its node opens the file with, minted for
// that node; a grant that cannot be minted fails the start before the node is
// called.
func TestPrepareRemoteTransportV3GrantsTheNodeItsSource(t *testing.T) {
	var got transcodenode.TranscodeStartRequest
	starts := 0
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/transcode/start" {
			starts++
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: got.SessionID, Status: "started", AudioRecipeVersion: got.AudioRecipeVersion})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer node.Close()

	file := v3HandlerFixtureFile(t)
	start := func(grants nodesourcetest.Granter) *transportErrorV3 {
		handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
		handler.NodeKeys = testNodeKeys
		handler.JWTSecret = "test-secret"
		handler.SourceGrants = grants
		handler.PlaybackConfig = func() config.PlaybackConfig {
			return config.PlaybackConfig{TranscodeEnabled: true, HWAccel: "none"}
		}
		transport, transportErr := handler.prepareRemoteTransportV3(
			httptest.NewRequest(http.MethodPost, "/", nil),
			&playback.Session{ID: "session-node-grant", UserID: 7, ProfileID: "profile-1"},
			file, remoteHLSResultV3(),
			nodepool.Plan{TranscodeNode: &nodepool.Node{ID: 12, URL: node.URL}}, preparedTimelineV3{}, mediaAuthModeV3{},
		)
		if transportErr == nil {
			transport.rollback()
		}
		return transportErr
	}
	if err := start(nodesourcetest.Granter{}); err != nil {
		t.Fatalf("prepare remote transport: %v", err)
	}
	if want := nodesourcetest.GrantFor(12, file.ID, file.FilePath); got.SourceGrant != want {
		t.Fatalf("source grant = %q, want %q", got.SourceGrant, want)
	}

	before := starts
	if err := start(nodesourcetest.Granter{Err: errors.New("no grant key")}); err == nil || !err.retryable {
		t.Fatalf("failed grant: %+v", err)
	}
	if starts != before {
		t.Fatal("a start without a grant reached the node")
	}
}
