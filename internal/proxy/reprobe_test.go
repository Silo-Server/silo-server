package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A proxy runs ffmpeg for remux recipes, so it gets the same escape hatch a
// transcode node has: re-probe that binary now, publish the result, and let
// health advertise it immediately instead of at the next 15-minute tick. It is
// not a hardware re-verification — a proxy reports no hardware — but the route
// and its publishing contract are unchanged.
func TestProxyReprobeCapabilitiesRecomputesAndStoresHash(t *testing.T) {
	const secret = "capability-secret"
	server := newCapabilityProxyServer(t, secret)
	server.storeCapabilityHash("sha256:stale")

	request := httptest.NewRequest(http.MethodPost, "/admin/reprobe-capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var result reprobeCapabilitiesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode re-probe result: %v", err)
	}
	if result.CapabilityHash == "" || result.CapabilityHash == "sha256:stale" {
		t.Fatalf("capability_hash = %q, want a recomputed hash", result.CapabilityHash)
	}
	if got := decodeProxyHealth(t, server).CapabilitiesHash; got != result.CapabilityHash {
		t.Fatalf("health capabilities_hash = %q, want the re-probed %q", got, result.CapabilityHash)
	}
}

// A re-probe that cannot finish must answer 503 and keep the published hash: an
// unfinished probe is not evidence the proxy's ffmpeg lost anything.
func TestProxyReprobeCapabilitiesKeepsHashOnIncompleteProbe(t *testing.T) {
	const secret = "capability-secret"
	server := newCapabilityProxyServer(t, secret)
	server.refreshCapabilitySnapshot(context.Background())
	published := decodeProxyHealth(t, server).CapabilitiesHash
	if published == "" {
		t.Fatal("no capability hash was published before the canceled re-probe")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/admin/reprobe-capabilities", nil).WithContext(canceled)
	request.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", recorder.Code, recorder.Body.String())
	}
	if got := decodeProxyHealth(t, server).CapabilitiesHash; got != published {
		t.Fatalf("health capabilities_hash = %q after an unfinished re-probe, want %q", got, published)
	}
}

// The route executes ffmpeg, so it stays inside the bearer-authed admin group.
func TestProxyReprobeCapabilitiesRequiresBearer(t *testing.T) {
	server := newCapabilityProxyServer(t, "capability-secret")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/admin/reprobe-capabilities", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a bearer token", recorder.Code)
	}
}

// A probe outlives its caller by design, so a capability request abandoned
// mid-probe releases capabilityBuildMu while ffmpeg is still running. A
// re-probe arriving then would start a second matrix beside the first, and two
// contending for one card publish a hardware failure for hardware that is fine.
// A proxy no longer starts the walk that made this expensive, but the gate is
// the shared contract with the transcode node's route and still holds here.
func TestProxyReprobeCapabilitiesRefusedWhileProbesAreRunning(t *testing.T) {
	const secret = "capability-secret"
	server := newCapabilityProxyServer(t, secret)
	server.storeCapabilityHash("sha256:previous")
	server.countProbesInFlight = func() int { return 1 }

	request := httptest.NewRequest(http.MethodPost, "/admin/reprobe-capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 while a probe is still running", recorder.Code)
	}
	if got := server.storedCapabilityHash(); got != "sha256:previous" {
		t.Fatalf("stored hash = %q, want the previous report untouched", got)
	}

	// And it is not a permanent refusal: once the detached probe lands, the
	// re-probe an operator asked for goes through.
	server.countProbesInFlight = func() int { return 0 }
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d after the probes finished, want 200", recorder.Code)
	}
}
