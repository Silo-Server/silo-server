package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

type connectionPlaybackFake struct {
	fakePlaybackService
	device string
	clear  bool
	state  connectionstore.State
}

func (f *connectionPlaybackFake) PlaybackConnection(_ context.Context, device string, reset bool) (connectionstore.State, error) {
	f.device = device
	f.clear = reset
	if f.state.Connection == "" {
		return connectionstore.State{Connection: "remote"}, nil
	}
	return f.state, nil
}

// The connection's times are v2 instants: UTC with exactly three fractional digits.
func TestPlaybackConnectionTimesAreInstants(t *testing.T) {
	deps, _ := catalogDeps(t)
	at := time.Date(2026, 10, 21, 16, 21, 40, 123456789, time.FixedZone("EDT", -4*3600))
	deps.Playback = &connectionPlaybackFake{state: connectionstore.State{
		Connection:     "remote",
		Probe:          &connectionstore.Probe{Kbps: 48000, MeasuredAt: at, Transport: "wifi"},
		PlaybackSample: &connectionstore.Sample{Kbps: 20000, MeasuredAt: at},
		LearnedLimit:   &connectionstore.Limit{Kbps: 4200, ObservedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour)},
	}}
	response := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/playback/connection", "", viewerHeaders())
	body := response.Body.String()
	if response.Code != 200 || strings.Count(body, `"2026-10-21T20:21:40.123Z"`) != 3 || !strings.Contains(body, `"expires_at":"2026-10-28T20:21:40.123Z"`) {
		t.Fatalf("get %d %s", response.Code, body)
	}
}
func TestPlaybackConnectionUsesDeclaredDevice(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &connectionPlaybackFake{}
	deps.Playback = fake
	h := newTestHandler(t, deps)
	headers := viewerHeaders()
	headers["X-Silo-Device-Id"] = "declared-device"
	headers["X-Device-ID"] = "different-playback-id"
	response := do(t, h, http.MethodGet, Prefix+"/playback/connection", "", headers)
	if response.Code != 200 || fake.device != "declared-device" || !strings.Contains(response.Body.String(), `"learned_limit":null`) {
		t.Fatalf("get %d %s device=%s", response.Code, response.Body.String(), fake.device)
	}
	response = do(t, h, http.MethodDelete, Prefix+"/playback/connection/learned-limit", "", viewerHeaders())
	if response.Code != 422 || fake.clear {
		t.Fatalf("reset without a device %d %s", response.Code, response.Body.String())
	}
	response = do(t, h, http.MethodDelete, Prefix+"/playback/connection/learned-limit", "", headers)
	if response.Code != 204 || !fake.clear {
		t.Fatalf("reset %d %s", response.Code, response.Body.String())
	}
}

func TestPlaybackEstimateSourceRequiresEstimate(t *testing.T) {
	deps, _ := catalogDeps(t)
	deps.Playback = &fakePlaybackService{}
	body := playbackStartFixture(t)
	body["bandwidth_estimate_source"] = "observed_rebuffer"
	delete(body, "bandwidth_estimate_kbps")
	body["client_features"] = []string{"playback_plan_v3", "buffering_quality_v1"}
	response := do(t, newTestHandler(t, deps), http.MethodPost, Prefix+"/playback/start", playbackJSON(t, body), viewerHeaders())
	if response.Code != 400 {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
}

func (f *connectionPlaybackFake) ReportPlaybackProbe(_ context.Context, device string, result connectionstore.ProbeResult) error {
	f.device = device
	if result.Kbps != 48000 || result.Bytes != 12000000 || result.DurationMS != 2000 || result.Transport != "wifi" {
		panic("unexpected measurement")
	}
	return nil
}
func TestProbeResultsContract(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &connectionPlaybackFake{}
	deps.Playback = fake
	h := newTestHandler(t, deps)
	headers := viewerHeaders()
	headers["X-Silo-Device-Id"] = "probe-device"
	path := Prefix + "/playback/bandwidth-probe/results"
	body := `{"kbps":48000,"bytes":12000000,"duration_ms":2000,"transport":"wifi"}`
	res := do(t, h, http.MethodPost, path, body, headers)
	if res.Code != 204 || fake.device != "probe-device" {
		t.Fatalf("report %d %s", res.Code, res.Body.String())
	}
	res = do(t, h, http.MethodPost, path, strings.Replace(body, `"wifi"`, `"satellite"`, 1), headers)
	if res.Code != 422 {
		t.Fatalf("invalid transport %d", res.Code)
	}
	// Under 100 kbps no start could send it as bandwidth_estimate_kbps.
	res = do(t, h, http.MethodPost, path, strings.Replace(body, `48000`, `99`, 1), headers)
	if res.Code != 422 {
		t.Fatalf("99 kbps result %d", res.Code)
	}
	res = do(t, h, http.MethodPost, path, body, nil)
	if res.Code != 401 {
		t.Fatalf("anonymous report %d", res.Code)
	}
}

func (f *connectionPlaybackFake) AdminPlaybackConnections(context.Context, int, string) ([]connectionstore.AdminConnection, error) {
	return []connectionstore.AdminConnection{}, nil
}
func (f *connectionPlaybackFake) ClearAdminPlaybackLimit(_ context.Context, user int, device, key string) error {
	if user != 2 || key != "local" {
		panic("wrong reset target")
	}
	f.device = device
	f.clear = true
	return nil
}
func TestAdminConnectionResetRequiresAdmin(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &connectionPlaybackFake{}
	deps.Playback = fake
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/devices/2/probe-device/connections/local/learned-limit"
	res := do(t, h, http.MethodDelete, path, "", viewerHeaders())
	if res.Code != 403 || fake.clear {
		t.Fatalf("viewer reset: %d", res.Code)
	}
	res = do(t, h, http.MethodDelete, path, "", bearer(adminToken))
	if res.Code != 204 || !fake.clear || fake.device != "probe-device" {
		t.Fatalf("admin reset: %d %s", res.Code, res.Body.String())
	}
}
