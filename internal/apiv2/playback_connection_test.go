package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

type connectionPlaybackFake struct {
	fakePlaybackService
	device string
	clear  bool
}

func (f *connectionPlaybackFake) PlaybackConnection(_ context.Context, device string, reset bool) (connectionstore.State, error) {
	f.device = device
	f.clear = reset
	return connectionstore.State{Connection: "remote"}, nil
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
