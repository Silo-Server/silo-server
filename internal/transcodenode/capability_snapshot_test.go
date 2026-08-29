package transcodenode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
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

	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = testSecret
	cfg.Playback.TranscodeDir = t.TempDir()
	cfg.Playback.FFmpegPath = ffmpegPath
	watcher.SetConfigForTest(cfg)
	return &Server{watcher: watcher, sessions: make(map[string]*playback.TranscodeSession)}, logPath
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

// The capability endpoint and the background snapshot must agree, so a served
// report carries its hash and health starts advertising it immediately —
// otherwise the API would refetch a report it already has.
func TestHWCapabilitiesPublishesCapabilityHash(t *testing.T) {
	server := newTestServer(t)

	request := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+testSecret)
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
	if info.CapabilityHash == "" {
		t.Fatal("served capability report carries no capability_hash")
	}
	// The hash must describe this payload, not some earlier one.
	served := info
	served.CapabilityHash = ""
	if want := playback.ComputeCapabilityHash(served); want != info.CapabilityHash {
		t.Fatalf("capability_hash = %s, want %s for the served payload", info.CapabilityHash, want)
	}
	if got := decodeHealth(t, server).CapabilitiesHash; got != info.CapabilityHash {
		t.Fatalf("health capabilities_hash = %q, want the just-served %q", got, info.CapabilityHash)
	}
}

// The first snapshot waits on encoder warmup so it measures a primed encoder,
// and a canceled node must not leave that wait running.
func TestStartCapabilitySnapshotsWaitsForReadyChannel(t *testing.T) {
	server, ffmpegLog := newCapabilityTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})

	server.StartCapabilitySnapshots(ctx, ready)
	cancel()
	<-ctx.Done()

	if _, err := os.Stat(ffmpegLog); !os.IsNotExist(err) {
		contents, _ := os.ReadFile(ffmpegLog)
		t.Fatalf("snapshot probed before warmup completed:\n%s", contents)
	}
	if got := decodeHealth(t, server).CapabilitiesHash; got != "" {
		t.Fatalf("capabilities_hash = %q, want empty while the snapshot is gated", got)
	}
}

// A failed probe is not evidence the hardware changed, so the previously
// published hash must survive it.
func TestRefreshCapabilitySnapshotKeepsHashOnProbeFailure(t *testing.T) {
	server, _ := newCapabilityTestServer(t)
	server.storeCapabilityHash("sha256:previous")

	server.refreshCapabilitySnapshot(context.Background())

	if got := server.storedCapabilityHash(); got != "sha256:previous" {
		t.Fatalf("stored hash = %q, want the previous hash kept after a failed probe", got)
	}
}
