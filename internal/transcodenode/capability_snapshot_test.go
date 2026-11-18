package transcodenode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// newCapabilityTestServer builds a node whose configured ffmpeg is a script
// that records every invocation, so a test can assert nothing probed.
func newCapabilityTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ffmpeg-invocations.log")
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\nexit 1\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	cfg := &config.Config{}
	cfg.Playback.TranscodeDir = t.TempDir()
	cfg.Playback.FFmpegPath = ffmpegPath
	return &Server{watcher: newTestRuntime(cfg), nodeKey: testNodeKey, sessions: make(map[string]*playback.TranscodeSession)}, logPath
}

// newProbingTestServer builds a node whose configured ffmpeg answers every
// capability probe with success for the software backend, so the capability
// endpoint serves a report without probing the host's real ffmpeg.
func newProbingTestServer(t *testing.T) *Server {
	t.Helper()
	server := newTestServer(t)
	ffmpegPath := filepath.Join(t.TempDir(), "successful-probe.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\necho tonemapx\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	return server
}

func decodeHealth(t *testing.T, server *Server) HealthResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var health HealthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return health
}

// Health is polled every 30 seconds per node and is the liveness signal the
// pools act on. It must publish the last snapshot's hash and nothing more: if
// it probed, a slow or wedged ffmpeg would make a live node look dead.
func TestHealthPublishesStoredCapabilityHashWithoutProbing(t *testing.T) {
	server, ffmpegLog := newCapabilityTestServer(t)

	if got := decodeHealth(t, server).CapabilitiesHash; got != "" {
		t.Fatalf("capabilities_hash = %q before any snapshot, want empty", got)
	}

	server.storeCapabilityHash("sha256:abc123")

	if got := decodeHealth(t, server).CapabilitiesHash; got != "sha256:abc123" {
		t.Fatalf("capabilities_hash = %q, want the stored snapshot hash", got)
	}
	if _, err := os.Stat(ffmpegLog); !os.IsNotExist(err) {
		contents, _ := os.ReadFile(ffmpegLog)
		t.Fatalf("health ran ffmpeg probes:\n%s", contents)
	}
}

func TestHWCapabilitiesWithholdsProgressiveRemuxWithoutNodeIdentity(t *testing.T) {
	server := newProbingTestServer(t)

	request := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)
	nodeauthtest.Sign(t, request, testNodeKey)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code == http.StatusServiceUnavailable {
		t.Skip("this host's ffmpeg cannot answer a capability probe")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var info playback.HWAccelInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if slices.Contains(info.TransportFeatures, playback.TransportFeatureProgressiveRemuxExecutionV1) {
		t.Fatalf("transport features = %v, want progressive remux withheld without a resolved node row", info.TransportFeatures)
	}
}
