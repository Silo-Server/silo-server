package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func decodeProxyHealth(t *testing.T, server *Server) healthResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var health healthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return health
}

// A proxy executes remux recipes, so the API tracks its capabilities the same
// way it tracks a transcode node's: by the hash health advertises. Until the
// first snapshot the field must be absent rather than a hash of nothing, which
// the sweep would treat as a real report.
func TestProxyHealthPublishesCapabilityHashOnlyAfterSnapshot(t *testing.T) {
	server := newCapabilityProxyServer(t, "capability-secret")

	if got := decodeProxyHealth(t, server).CapabilitiesHash; got != "" {
		t.Fatalf("capabilities_hash = %q before any snapshot, want empty", got)
	}

	server.refreshCapabilitySnapshot(context.Background())

	hash := decodeProxyHealth(t, server).CapabilitiesHash
	if hash == "" {
		t.Fatal("capabilities_hash is still empty after a snapshot")
	}
	// A second snapshot of an unchanged ffmpeg must not move the hash, or the
	// sweep would refetch this proxy's report forever.
	server.refreshCapabilitySnapshot(context.Background())
	if got := decodeProxyHealth(t, server).CapabilitiesHash; got != hash {
		t.Fatalf("capabilities_hash changed without hardware changing: %q then %q", hash, got)
	}
}

// A probe that did not finish hashes differently from the same ffmpeg probed
// successfully, so publishing it would announce a capability change that never
// happened — and cost the API a full capability refetch plus a planning-cache
// drop. A caller that gives up must leave the published hash alone.
func TestProxyCapabilitiesRejectsIncompleteProbeWithoutPublishing(t *testing.T) {
	const secret = "capability-secret"
	server := newCapabilityProxyServer(t, secret)
	server.refreshCapabilitySnapshot(context.Background())
	published := decodeProxyHealth(t, server).CapabilitiesHash
	if published == "" {
		t.Fatal("no capability hash was published before the canceled request")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil).WithContext(canceled)
	nodeauthtest.Sign(t, request, secret)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if got := decodeProxyHealth(t, server).CapabilitiesHash; got != published {
		t.Fatalf("health capabilities_hash = %q after an unfinished probe, want the previous %q", got, published)
	}
}

// newCapabilityProxyServer builds a proxy whose configured ffmpeg is a script
// with a known, successful answer for every listing the capability assembly
// runs.
//
// The capability tests must not depend on the host's toolchain. Left
// unconfigured, the probes shell out to whatever `ffmpeg` is on PATH — which
// asserts a 200 on a developer's machine and a 503 on CI, where no ffmpeg is
// installed, because ProbeTransformationRegistryWithToneMapV3Result reports the
// exec failure. Scripting the binary also makes the published capability hash
// deterministic, which is what the stability assertions here depend on.
func newCapabilityProxyServer(t *testing.T, secret string) *Server {
	t.Helper()
	server, _ := newCapabilityProxyServerRecordingFFmpeg(t, secret, true)
	return server
}

// newCapabilityProxyServerRecordingFFmpeg is newCapabilityProxyServer with the
// scripted binary's argv appended to a log, and returns the log's path so a test
// can assert what the assembly did and did not run.
//
// aacAvailable scripts whether `-encoders` lists the AAC encoder, which is the
// cheapest way to make this proxy's advertised transformations genuinely differ.
func newCapabilityProxyServerRecordingFFmpeg(t *testing.T, secret string, aacAvailable bool) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	invocations := filepath.Join(dir, "invocations.log")
	encoders := " V..... libx264 H.264\\n"
	if aacAvailable {
		encoders += " A..... aac AAC\\n"
	}
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + invocations + "\n" +
		"case \"$*\" in\n" +
		"  *-bsfs*) echo 'dovi_rpu'; exit 0 ;;\n" +
		"  *-encoders*) printf '" + encoders + "'; exit 0 ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	w := testWatcher(secret)
	w.Config().Playback.FFmpegPath = ffmpegPath
	return NewServer(w, nil), invocations
}

// fetchProxyCapabilities reads the served capability report.
func fetchProxyCapabilities(t *testing.T, server *Server, secret string) playback.HWAccelInfo {
	t.Helper()
	recorder := serveProxy(t, server, http.MethodGet, "/hw-capabilities", secret)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var info playback.HWAccelInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	return info
}

// The hash still has to move for the one thing it now tracks, or a proxy whose
// ffmpeg lost the AAC encoder would keep being planned for audio remuxes it can
// no longer run.
func TestProxyCapabilityHashTracksTransformations(t *testing.T) {
	const secret = "capability-secret"
	capable, _ := newCapabilityProxyServerRecordingFFmpeg(t, secret, true)
	degraded, _ := newCapabilityProxyServerRecordingFFmpeg(t, secret, false)

	capableInfo := fetchProxyCapabilities(t, capable, secret)
	degradedInfo := fetchProxyCapabilities(t, degraded, secret)

	if len(capableInfo.Transformations) == len(degradedInfo.Transformations) {
		t.Fatalf("both ffmpeg builds advertised %d transformations; the fixture proves nothing",
			len(capableInfo.Transformations))
	}
	if capableInfo.CapabilityHash == degradedInfo.CapabilityHash {
		t.Fatalf("capability_hash = %s for both builds, but their transformations differ",
			capableInfo.CapabilityHash)
	}
}
