package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
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
// body, recording the bearer token it was given.
func fakeNodeServer(t *testing.T, status int, body string) (url string, authorization *string) {
	t.Helper()
	seen := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/reprobe-capabilities" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		seen = r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL, &seen
}

// The point of the API-side action is that the operator does not have to wait a
// sweep interval: the node re-probes, and this server immediately refetches and
// stores the report so the list, the pools, and the planner agree with it.
func TestHandleReprobeNodeRefreshesStoredCapabilities(t *testing.T) {
	url, authorization := fakeNodeServer(t, http.StatusOK,
		`{"resolved":"qsv","capability_hash":"sha256:new"}`)
	repo := &stubNodeRepository{node: &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: url}}
	refresher := &stubCapabilityRefresher{}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")
	handler.SetCapabilityRefresher(refresher)

	recorder := httptest.NewRecorder()
	handler.HandleReprobeNode(recorder, reprobeRequest(t))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var result ReprobeNodeResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Status != "ok" || result.Error != "" {
		t.Fatalf("result = %+v, want a clean ok", result)
	}
	if result.Resolved != "qsv" || result.CapabilityHash != "sha256:new" {
		t.Fatalf("result did not carry the node's answer: %+v", result)
	}
	if !result.CapabilitiesRefreshed {
		t.Fatal("capabilities_refreshed = false, want the stored row refreshed on success")
	}
	if got := refresher.calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want exactly one", got)
	}
	if *authorization != "Bearer secret" {
		t.Fatalf("node saw authorization %q, want the bearer secret", *authorization)
	}
}

// A node whose probe could not complete answers 503 and keeps its previous
// hash. That has to reach the operator as a named failure, and must not trigger
// a capability refetch — refetching would store the report the node explicitly
// declined to republish.
func TestHandleReprobeNodeSurfacesNodeFailure(t *testing.T) {
	url, _ := fakeNodeServer(t, http.StatusServiceUnavailable, "capability probe unavailable\n")
	repo := &stubNodeRepository{node: &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: url}}
	refresher := &stubCapabilityRefresher{}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")
	handler.SetCapabilityRefresher(refresher)

	recorder := httptest.NewRecorder()
	handler.HandleReprobeNode(recorder, reprobeRequest(t))

	// The API request itself succeeded; the node is what failed, exactly as the
	// per-node check and force-reload routes report it.
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var result ReprobeNodeResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Status != "error" || result.Error == "" {
		t.Fatalf("result = %+v, want a reported node error", result)
	}
	if !strings.Contains(result.Error, "hardware probe") {
		t.Fatalf("error = %q, want it to explain the degraded probe", result.Error)
	}
	if result.CapabilitiesRefreshed {
		t.Fatal("capabilities_refreshed = true after a failed re-probe")
	}
	if got := refresher.calls.Load(); got != 0 {
		t.Fatalf("refresh calls = %d, want none after a failed re-probe", got)
	}
}

// A refresh failure is reported beside a successful re-probe rather than turning
// it into one: the node has already recomputed, and the next sweep will store
// the report.
func TestHandleReprobeNodeReportsRefreshFailureSeparately(t *testing.T) {
	url, _ := fakeNodeServer(t, http.StatusOK, `{"resolved":"none","capability_hash":"sha256:new"}`)
	repo := &stubNodeRepository{node: &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: url}}
	refresher := &stubCapabilityRefresher{err: nodepool.ErrCapabilityRefreshInFlight}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")
	handler.SetCapabilityRefresher(refresher)

	recorder := httptest.NewRecorder()
	handler.HandleReprobeNode(recorder, reprobeRequest(t))

	var result ReprobeNodeResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Status != "ok" || result.CapabilityHash != "sha256:new" {
		t.Fatalf("result = %+v, want the re-probe itself reported as ok", result)
	}
	if result.CapabilitiesRefreshed {
		t.Fatal("capabilities_refreshed = true despite a failed refresh")
	}
}

// A node that is transcoding refuses, because a probe's smoke encode competing
// with live sessions would be published as failed hardware. The operator has to
// read the node's own explanation, not a bare status code, or the only sensible
// next step — drain the node and retry — is invisible.
func TestHandleReprobeNodeSurfacesBusyNodeRefusal(t *testing.T) {
	url, _ := fakeNodeServer(t, http.StatusConflict,
		"node is running 2 transcode job(s); a re-probe smoke-encodes on the GPU. Retry when the node is idle.\n")
	repo := &stubNodeRepository{node: &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: url}}
	refresher := &stubCapabilityRefresher{}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")
	handler.SetCapabilityRefresher(refresher)

	recorder := httptest.NewRecorder()
	handler.HandleReprobeNode(recorder, reprobeRequest(t))

	var result ReprobeNodeResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("result = %+v, want the refusal reported as an error", result)
	}
	if !strings.Contains(result.Error, "idle") {
		t.Fatalf("error = %q, want the node's own explanation", result.Error)
	}
	if got := refresher.calls.Load(); got != 0 {
		t.Fatalf("refresh calls = %d, want none after a refused re-probe", got)
	}
}

// The action outlives the API listener's 120s WriteTimeout by design: the node's
// advertised probe budget reaches five minutes and the capability refetch adds
// two more. Without a deadline extension the response is written after the
// connection's deadline has passed, so the operator sees a torn connection and a
// "Re-probe failed" toast for an action that succeeded — and re-runs the whole
// cold FFmpeg matrix. This drives a real listener with a deadline far shorter
// than the node takes, which is the only way to observe the write actually
// landing.
func TestHandleReprobeNodeOutlivesTheListenerWriteTimeout(t *testing.T) {
	const nodeDelay = 400 * time.Millisecond
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(nodeDelay)
		_, _ = w.Write([]byte(`{"resolved":"qsv","capability_hash":"sha256:new"}`))
	}))
	defer node.Close()

	// The node advertises a budget, so the handler knows how long to hold the
	// connection open — exactly as a node that has been inventoried once does.
	report, err := json.Marshal(playback.HWAccelInfo{ProbeRequestTimeoutMillis: 30_000})
	if err != nil {
		t.Fatal(err)
	}
	repo := &stubNodeRepository{node: &nodepool.Node{
		ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL,
		Capabilities: report,
	}}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")
	handler.SetCapabilityRefresher(&stubCapabilityRefresher{})

	router := chi.NewRouter()
	router.Post("/admin/nodes/{id}/reprobe", handler.HandleReprobeNode)
	api := httptest.NewUnstartedServer(router)
	api.Config.WriteTimeout = nodeDelay / 4
	api.Start()
	defer api.Close()

	resp, err := api.Client().Post(api.URL+"/admin/nodes/1/reprobe", "", nil)
	if err != nil {
		t.Fatalf("re-probe response lost to the listener write deadline: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var result ReprobeNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Status != "ok" || result.CapabilityHash != "sha256:new" {
		t.Fatalf("result = %+v, want the node's answer delivered intact", result)
	}
}

func TestHandleReprobeNodeUnknownNode(t *testing.T) {
	handler := NewNodeHandler(&stubNodeRepository{}, nil, nil, nil, nil, nil, "secret")
	recorder := httptest.NewRecorder()
	handler.HandleReprobeNode(recorder, reprobeRequest(t))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown node", recorder.Code)
	}
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

// The route an operator triggers has to reach a node stored with a trailing
// slash, which the pools and the repository already treat as the same worker.
func TestHandleReprobeNodeReachesATrailingSlashBaseURL(t *testing.T) {
	var path string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		writeJSON(w, http.StatusOK, map[string]any{"resolved": "qsv", "capability_hash": "sha256:x"})
	}))
	defer node.Close()

	repo := &stubNodeRepository{node: &nodepool.Node{
		ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL + "/", Enabled: true,
	}}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	recorder := httptest.NewRecorder()
	handler.HandleReprobeNode(recorder, reprobeRequest(t))

	if path != "/admin/reprobe-capabilities" {
		t.Fatalf("node was asked for %q, want /admin/reprobe-capabilities", path)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
