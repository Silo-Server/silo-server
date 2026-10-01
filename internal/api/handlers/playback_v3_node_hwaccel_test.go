package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// A node under a per-node acceleration override resolves to its own backend no
// matter what the request says, so a request carrying this API host's
// cluster-wide value would be silently corrected on arrival — and the recipe
// card would then describe an encode that never ran. Every other node keeps the
// cluster value verbatim: a node honors a named backend without re-checking it,
// so "auto" has to survive dispatch to reach live detection on the node, and a
// stale capability report must never stand in for it.
func TestPrepareRemoteTransportV3DispatchesTheNodesEffectiveBackend(t *testing.T) {
	override := func(value string) *string { return &value }
	tests := []struct {
		name         string
		cluster      string
		override     *string
		capabilities string
		want         string
	}{
		{
			name:     "node overridden to software wins over a qsv cluster",
			cluster:  "qsv",
			override: override("none"),
			want:     "none",
		},
		{
			name:     "node overridden to other hardware wins too",
			cluster:  "qsv",
			override: override("nvenc"),
			want:     "nvenc",
		},
		{
			name:     "an override lands immediately, ahead of the stale report it contradicts",
			cluster:  "qsv",
			override: override("none"),
			// The node was still reporting qsv when the operator disabled it.
			capabilities: `{"resolved":"qsv"}`,
			want:         "none",
		},
		{
			name:    "a node with no override keeps the cluster value",
			cluster: "qsv",
			want:    "qsv",
		},
		{
			name:     "a blank override is not an override",
			cluster:  "qsv",
			override: override("  "),
			want:     "qsv",
		},
		{
			name:    "auto reaches the node so it resolves against live hardware",
			cluster: "auto",
			// A boot-time probe that ran before the render devices were
			// attached must not pin later sessions to software.
			capabilities: `{"resolved":"none","render_devices":[]}`,
			want:         "auto",
		},
		{
			name:         "a stale report never overrides the cluster value",
			cluster:      "qsv",
			capabilities: `{"resolved":"none"}`,
			want:         "qsv",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got transcodenode.TranscodeStartRequest
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/transcode/start" {
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Errorf("decode remote start: %v", err)
					}
					writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: got.SessionID, Status: "started", AudioRecipeVersion: got.AudioRecipeVersion})
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer node.Close()

			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-secret"
			handler.PlaybackConfig = func() config.PlaybackConfig {
				return config.PlaybackConfig{TranscodeEnabled: true, HWAccel: test.cluster}
			}
			pooled := &nodepool.Node{URL: node.URL, HWAccelOverride: test.override}
			if test.capabilities != "" {
				pooled.Capabilities = json.RawMessage(test.capabilities)
			}
			transport, transportErr := handler.prepareRemoteTransportV3(
				httptest.NewRequest(http.MethodPost, "/", nil),
				&playback.Session{ID: "session-node-hwaccel", UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t), remoteHLSResultV3(),
				nodepool.Plan{TranscodeNode: pooled}, preparedTimelineV3{}, mediaAuthModeV3{},
			)
			if transportErr != nil {
				t.Fatalf("prepare remote transport: %v", transportErr)
			}
			defer transport.rollback()
			if got.HWAccel != test.want {
				t.Fatalf("dispatched hw_accel = %q, want %q", got.HWAccel, test.want)
			}
		})
	}
}
