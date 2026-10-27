package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/go-chi/chi/v5"
)

// stubCapabilityRefresher stands in for the health checker's on-demand refresh.
type stubCapabilityRefresher struct {
	calls atomic.Int32
	err   error
}

func (s *stubCapabilityRefresher) RefreshNodeCapabilities(context.Context, *nodepool.Node) error {
	s.calls.Add(1)
	return s.err
}

func reprobeRequest(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/nodes/1/reprobe", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "1")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

// fakeNodeServer answers the node-side re-probe route with the given status and
// body. signedWith reports whether the request it saw was signed with secret.
func fakeNodeServer(t *testing.T, status int, body string) (url string, signedWith func(secret string) bool) {
	t.Helper()
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/reprobe-capabilities" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requestBody, _ := io.ReadAll(r.Body)
		seen = r.Clone(context.WithoutCancel(r.Context()))
		seen.Body = io.NopCloser(bytes.NewReader(requestBody))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL, func(secret string) bool { return seen != nil && nodeauthtest.Signed(seen, secret) }
}

// The node publishes the budget its own probe matrix needs, so a node with
// slower hardware is not abandoned on a cluster-wide guess. A node that has
// never been inventoried falls back to the generous constant.
func TestNodeReprobeTimeoutPrefersTheNodeAdvertisedBudget(t *testing.T) {
	handler := NewNodeHandler(&stubNodeRepository{}, nil, nil, nil, nil, nil, "secret")
	// Above the fallback, which is a floor: a node that says it needs less than
	// the constant this handler is willing to spend does not shorten it.
	advertised := playback.HWAccelInfo{ProbeRequestTimeoutMillis: 200_000}
	payload, err := json.Marshal(advertised)
	if err != nil {
		t.Fatal(err)
	}
	if nodeReprobeFallbackTimeout >= 200*time.Second {
		t.Fatalf("fixture is inert: the fallback %s must stay under the advertised 200s", nodeReprobeFallbackTimeout)
	}
	if got := handler.nodeReprobeTimeout(&nodepool.Node{Capabilities: payload}); got != 200*time.Second {
		t.Fatalf("timeout = %s, want the node-advertised 200s", got)
	}
	if got := handler.nodeReprobeTimeout(&nodepool.Node{}); got != nodeReprobeFallbackTimeout {
		t.Fatalf("timeout = %s, want the fallback %s for a node with no report", got, nodeReprobeFallbackTimeout)
	}
	if got := handler.nodeReprobeTimeout(&nodepool.Node{Capabilities: json.RawMessage(`not json`)}); got != nodeReprobeFallbackTimeout {
		t.Fatalf("timeout = %s, want the fallback for an unreadable report", got)
	}
}

// A re-probe discards every cache on the node, so it runs the full matrix for
// whatever device set the node is configured for *now*. A report stored before
// an operator widened hw_device_override advertises the old, smaller budget, and
// honoring it would cancel the very request that would have replaced it.
func TestNodeReprobeTimeoutRepricesAWidenedDeviceOverride(t *testing.T) {
	devices := "/dev/dri/renderD128,/dev/dri/renderD129,/dev/dri/renderD130,/dev/dri/renderD131"
	backend := tonemap.BackendQSV
	handler := NewNodeHandler(&stubNodeRepository{}, nil, nil, nil, nil, nil, "secret")
	handler.SetClusterPlaybackPolicy(func() config.PlaybackConfig {
		return config.PlaybackConfig{HWAccel: tonemap.BackendQSV, HWDevice: "/dev/dri/renderD128"}
	})
	stored, err := json.Marshal(playback.HWAccelInfo{
		ProbeRequestTimeoutMillis: playback.CapabilityRequestTimeout(backend, "/dev/dri/renderD128").Milliseconds(),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := playback.CapabilityRequestTimeout(backend, devices)
	got := handler.nodeReprobeTimeout(&nodepool.Node{
		Capabilities: stored, HWAccelOverride: &backend, HWDeviceOverride: &devices,
	})
	if got != want {
		t.Fatalf("re-probe timeout = %s, want the four-device %s", got, want)
	}
	if want <= nodeReprobeFallbackTimeout {
		t.Fatalf("fixture is inert: the four-device budget %s must exceed the fallback %s", want, nodeReprobeFallbackTimeout)
	}
}

// boundedCapabilityRefresher is a refresher that also reports how long its
// refresh may take, as the real health checker does.
type boundedCapabilityRefresher struct {
	stubCapabilityRefresher
	bound time.Duration
}

func (b *boundedCapabilityRefresher) CapabilityRefreshBound(*nodepool.Node) time.Duration {
	return b.bound
}

// The connection has to stay open across both long calls: the node's re-probe
// and the capability refresh that stores its result. The refresh's bound is
// derived from the node's own advertised probe budget, so on a node with many
// devices it runs well past the exported five-minute floor — and reserving the
// floor would let the write deadline fire after the re-probe succeeded but
// before its response was written, telling an operator an action failed that
// has already changed the node.
func TestReprobeWriteDeadlineReservesTheNodesOwnRefreshBound(t *testing.T) {
	node := &nodepool.Node{ID: 1, Name: "gpu-1", URL: "http://gpu-1", Type: nodepool.NodeTypeTranscode}
	handler := NewNodeHandler(&stubNodeRepository{}, nil, nil, nil, nil, nil, "secret")

	// No refresher: the floor is all this handler can promise.
	if got := handler.capabilityRefreshBound(node); got != nodepool.CapabilityRefreshTimeout {
		t.Fatalf("bound without a refresher = %s, want the exported floor %s", got, nodepool.CapabilityRefreshTimeout)
	}

	// A refresher that will hold the fetch open past the floor.
	bound := nodepool.CapabilityRefreshTimeout + 4*time.Minute
	handler.SetCapabilityRefresher(&boundedCapabilityRefresher{bound: bound})
	if got := handler.capabilityRefreshBound(node); got != bound {
		t.Fatalf("bound = %s, want the refresher's own %s", got, bound)
	}

	// And it is what the deadline reserves, on top of the probe budget.
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	probeBudget := 3 * time.Minute
	before := time.Now()
	handler.extendReprobeWriteDeadline(recorder, reprobeRequest(t), node, probeBudget)
	want := probeBudget + bound + nodeReprobeWriteSlack
	if reserved := recorder.deadline.Sub(before); reserved < want {
		t.Fatalf("reserved %s, want at least the probe budget plus the refresh bound (%s)", reserved, want)
	}
}

// deadlineRecorder captures the write deadline the handler sets.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(at time.Time) error {
	d.deadline = at
	return nil
}
