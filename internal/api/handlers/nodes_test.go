package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/go-chi/chi/v5"
)

type stubNodeRepository struct {
	nodes []*nodepool.Node
	// updateResult is what Update returns once validation passes; nil keeps the
	// default "unknown node" answer.
	updateResult *nodepool.Node
	updated      *nodepool.UpdateNodeInput
	// node is what GetByID returns; nil keeps the default "unknown node" answer.
	node *nodepool.Node
}

func (s *stubNodeRepository) List(context.Context) ([]*nodepool.Node, error) { return s.nodes, nil }

func (s *stubNodeRepository) GetByID(context.Context, int) (*nodepool.Node, error) {
	if s.node == nil {
		return nil, nodepool.ErrNodeNotFound
	}
	return s.node, nil
}

func (s *stubNodeRepository) Create(context.Context, nodepool.CreateNodeInput) (*nodepool.Node, error) {
	return nil, nodepool.ErrNodeNotFound
}

// Update mirrors Repository.Update's order of operations — validate, then
// write — so handler tests see the same errors production returns.
func (s *stubNodeRepository) Update(_ context.Context, _ int, input nodepool.UpdateNodeInput) (*nodepool.Node, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	s.updated = &input
	if s.updateResult == nil {
		return nil, nodepool.ErrNodeNotFound
	}
	return s.updateResult, nil
}

func (s *stubNodeRepository) Delete(context.Context, int) error { return nil }

func (s *stubNodeRepository) UpdateHealth(context.Context, int, string, bool, int, int, []byte) error {
	return nil
}

// The node list is the admin's inventory view, so it must carry the stored
// capability report, its age, and the derived GPU identities beside the
// existing node fields.
func TestHandleListNodesIncludesCapabilities(t *testing.T) {
	hash := "sha256:abc"
	refreshedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	repo := &stubNodeRepository{nodes: []*nodepool.Node{
		{
			ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: "http://gpu-1", Enabled: true, Healthy: true,
			Capabilities: json.RawMessage(`{"boot_id":"boot-1","resolved":"nvenc","render_device_details":[` +
				`{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-aaa"}]}`),
			CapabilitiesHash:        &hash,
			CapabilitiesRefreshedAt: &refreshedAt,
			// Production derives this in the node store's row scanner (covered
			// by TestScanNodeDerivesPhysicalGPUKeys); the stub stands in for it
			// so this test can assert the handler passes the field through.
			PhysicalGPUKeys: []string{"GPU-aaa"},
		},
		{ID: 2, Name: "old-node", Type: nodepool.NodeTypeProxy, URL: "http://old", Enabled: true},
	}}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	recorder := httptest.NewRecorder()
	handler.HandleListNodes(recorder, httptest.NewRequest(http.MethodGet, "/admin/nodes", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("returned %d nodes, want 2", len(items))
	}
	// Existing fields must still be present: this response is embedded, not
	// rebuilt, so a client reading it today keeps working.
	if items[0]["name"] != "gpu-1" || items[0]["healthy"] != true {
		t.Fatalf("node fields were lost: %v", items[0])
	}
	if items[0]["capabilities_hash"] != "sha256:abc" {
		t.Fatalf("capabilities_hash = %v", items[0]["capabilities_hash"])
	}
	if items[0]["capabilities_refreshed_at"] == nil {
		t.Fatalf("capabilities_refreshed_at missing: %v", items[0])
	}
	if items[0]["capabilities"] == nil {
		t.Fatalf("capabilities missing: %v", items[0])
	}
	keys, ok := items[0]["physical_gpu_keys"].([]any)
	if !ok || len(keys) != 1 || keys[0] != "GPU-aaa" {
		t.Fatalf("physical_gpu_keys = %v", items[0]["physical_gpu_keys"])
	}
	// A node that never reported capabilities carries none of the new fields
	// rather than empty ones a client would have to special-case.
	for _, field := range []string{"capabilities", "capabilities_hash", "capabilities_refreshed_at", "physical_gpu_keys"} {
		if _, present := items[1][field]; present {
			t.Fatalf("node without capabilities carries %q: %v", field, items[1])
		}
	}
}

func updateNodeRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/admin/nodes/1", strings.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "1")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

// A per-node override may only name a backend the cluster-wide setting could
// also name. Rejecting it here is what turns a CHECK-constraint violation into
// an answer the admin UI can show.
func TestHandleUpdateNodeRejectsUnknownHWAccelOverride(t *testing.T) {
	repo := &stubNodeRepository{updateResult: &nodepool.Node{ID: 1, Name: "gpu-1"}}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	recorder := httptest.NewRecorder()
	awaitNodeUpdate(t, handler, recorder, `{"hw_accel_override":"videotoolbox"}`)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.updated != nil {
		t.Fatalf("rejected input still reached the store: %+v", repo.updated)
	}
	if !strings.Contains(recorder.Body.String(), "hw_accel_override") {
		t.Fatalf("error body does not name the field: %s", recorder.Body.String())
	}
}

// Setting an override and clearing it again are both ordinary updates; the
// clear has to survive JSON decoding as a clear rather than as "unchanged".
func TestHandleUpdateNodeAcceptsAndClearsHWOverrides(t *testing.T) {
	accel, device := "vaapi", "/dev/dri/renderD129"
	tests := []struct {
		name       string
		body       string
		wantAccel  *string
		wantDevice *string
	}{
		{
			name:       "sets both",
			body:       `{"hw_accel_override":"vaapi","hw_device_override":"/dev/dri/renderD129"}`,
			wantAccel:  &accel,
			wantDevice: &device,
		},
		{
			name:       "explicit null clears both",
			body:       `{"hw_accel_override":null,"hw_device_override":null}`,
			wantAccel:  new(string),
			wantDevice: new(string),
		},
		{
			name: "omitted leaves both alone",
			body: `{"name":"gpu-1"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stored := "qsv"
			repo := &stubNodeRepository{updateResult: &nodepool.Node{ID: 1, Name: "gpu-1", HWAccelOverride: &stored}}
			handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

			recorder := httptest.NewRecorder()
			awaitNodeUpdate(t, handler, recorder, test.body)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
			}
			if repo.updated == nil {
				t.Fatal("update never reached the store")
			}
			if !equalStringPointer(repo.updated.HWAccelOverride, test.wantAccel) {
				t.Fatalf("HWAccelOverride = %v, want %v", repo.updated.HWAccelOverride, test.wantAccel)
			}
			if !equalStringPointer(repo.updated.HWDeviceOverride, test.wantDevice) {
				t.Fatalf("HWDeviceOverride = %v, want %v", repo.updated.HWDeviceOverride, test.wantDevice)
			}
			// The response is the stored row, so the admin UI sees the effective
			// policy without a second read.
			var response map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response["hw_accel_override"] != "qsv" {
				t.Fatalf("response hw_accel_override = %v, want the stored value", response["hw_accel_override"])
			}
		})
	}
}

func equalStringPointer(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// A node re-reads its own row on a 60s config poll, but this server starts
// dispatching the new backend the moment its pool reloads. An operator moving a
// node from QSV on a render node to NVENC on a CUDA index would otherwise get
// up to a minute of start requests pairing the new backend with the old device,
// so the node is nudged to reload before the updated policy is published.
//
// The nudge targets /admin/reload-config, never /admin/force-reload: the latter
// tears down every live playback session on a transcode node.
func TestHandleUpdateNodeReloadsTheNodeAfterAnOverrideChange(t *testing.T) {
	reloaded := make(chan string, 4)
	var destructive atomic.Bool
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/reload-config":
			reloaded <- r.Header.Get("Authorization")
		case "/admin/force-reload":
			destructive.Store(true)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(node.Close)

	qsv := "qsv"
	before := &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL, HWAccelOverride: &qsv}
	nvenc := "nvenc"
	after := &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL, HWAccelOverride: &nvenc}
	repo := &stubNodeRepository{updateResult: after, node: before}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	recorder := httptest.NewRecorder()
	awaitNodeUpdate(t, handler, recorder, `{"hw_accel_override":"nvenc"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	select {
	case authorization := <-reloaded:
		if authorization != "Bearer secret" {
			t.Fatalf("node saw authorization %q, want the bearer secret", authorization)
		}
	default:
		t.Fatal("the node was not asked to reload after its overrides changed")
	}
	if destructive.Load() {
		t.Fatal("a policy edit hit the destructive force-reload route")
	}
	// The route answers 204, so a client that accepted only 200 would warn on
	// every successful reload — a standing false alarm on the ordinary path.
	if logged := recorder.Body.String(); strings.Contains(logged, "refused") {
		t.Fatalf("body mentions a refusal: %s", logged)
	}
}

// The node reload route answers 204. Treating anything outside 2xx as a refusal
// is what keeps a successful reload from logging a failure an operator would
// then go looking for.
func TestReloadNodeConfigAcceptsNoContent(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusAccepted} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			called := make(chan struct{}, 1)
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called <- struct{}{}
				w.WriteHeader(status)
			}))
			t.Cleanup(node.Close)

			handler := NewNodeHandler(&stubNodeRepository{}, nil, nil, nil, nil, nil, "secret")
			handler.reloadNodeConfig(context.Background(), &nodepool.Node{ID: 1, Name: "gpu-1", URL: node.URL})

			select {
			case <-called:
			default:
				t.Fatal("the node was never called")
			}
		})
	}
}

// This server's cached view of what a node can do — the v3 planning inventory —
// is keyed by node URL and holds the tone-map executors and transformations the
// *previous* backend advertised. Changing the policy without dropping it plans
// the next minute's sessions against filters the worker has already moved off,
// and the worker then rejects the start.
func TestHandleUpdateNodeInvalidatesCapabilityCacheAfterAnOverrideChange(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(node.Close)

	qsv, nvenc := "qsv", "nvenc"
	before := &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL, HWAccelOverride: &qsv}
	after := &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL, HWAccelOverride: &nvenc}
	repo := &stubNodeRepository{updateResult: after, node: before}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	invalidated := make(chan string, 4)
	handler.SetCapabilityInvalidator(func(url string) { invalidated <- url })

	awaitNodeUpdate(t, handler, httptest.NewRecorder(), `{"hw_accel_override":"nvenc"}`)

	select {
	case url := <-invalidated:
		if url != node.URL {
			t.Fatalf("invalidated %q, want the node's URL %q", url, node.URL)
		}
	default:
		t.Fatal("the capability cache was not dropped after the policy changed")
	}
}

// A node that does not confirm the reload is out of step: its backend now comes
// from this server's pool while its device still comes from its own
// configuration. The policy is published regardless — withholding it would
// strand an override the operator can see stored, since nothing else re-reads
// the column — so what this pins down is that the update still succeeds and the
// caller learns the node did not confirm.
func TestReloadNodeConfigReportsAnUnconfirmedNode(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(node.Close)

	handler := NewNodeHandler(&stubNodeRepository{}, nil, nil, nil, nil, nil, "secret")
	if handler.reloadNodeConfig(context.Background(), &nodepool.Node{ID: 1, Name: "gpu-1", URL: node.URL}) {
		t.Fatal("a 500 from the node reported the reload as confirmed")
	}
	if handler.reloadNodeConfig(context.Background(), &nodepool.Node{ID: 1, Name: "gpu-1", URL: "http://127.0.0.1:1"}) {
		t.Fatal("an unreachable node reported the reload as confirmed")
	}
}

// The update itself still succeeds and the new policy still reaches the pool: a
// stored override that never reaches dispatch is a silent permanent
// misconfiguration, where the mismatch is loud, bounded by the node's poll, and
// self-healing.
func TestHandleUpdateNodePublishesPolicyEvenWhenTheNodeDoesNotConfirm(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(node.Close)

	qsv, nvenc := "qsv", "nvenc"
	before := &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL, HWAccelOverride: &qsv}
	after := &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL, HWAccelOverride: &nvenc}
	repo := &stubNodeRepository{updateResult: after, node: before}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	invalidated := make(chan string, 4)
	handler.SetCapabilityInvalidator(func(url string) { invalidated <- url })

	recorder := httptest.NewRecorder()
	awaitNodeUpdate(t, handler, recorder, `{"hw_accel_override":"nvenc"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want the update to succeed anyway", recorder.Code)
	}
	select {
	case <-invalidated:
	default:
		t.Fatal("the capability cache was not dropped when the node did not confirm")
	}
}

// An edit that moves neither override leaves the cache alone: re-probing every
// node on every rename would put ffmpeg execs behind an unrelated form save.
func TestHandleUpdateNodeKeepsCapabilityCacheWithoutAnOverrideChange(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(node.Close)

	stored := &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL}
	repo := &stubNodeRepository{updateResult: stored, node: stored}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	invalidated := make(chan string, 4)
	handler.SetCapabilityInvalidator(func(url string) { invalidated <- url })

	awaitNodeUpdate(t, handler, httptest.NewRecorder(), `{"name":"gpu-one"}`)

	select {
	case url := <-invalidated:
		t.Fatalf("a rename dropped the capability cache for %q", url)
	default:
	}
}

// The admin form posts both override fields on every transcode-node save, so
// their presence says nothing about them moving. Nudging on presence alone made
// an unrelated edit — a rename, a capacity change, or a plain resubmit — ask the
// node to re-read its config for nothing.
func TestHandleUpdateNodeDoesNotReloadWhenOverridesAreUnchanged(t *testing.T) {
	reloaded := make(chan struct{}, 4)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/reload") || strings.HasPrefix(r.URL.Path, "/admin/force") {
			reloaded <- struct{}{}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(node.Close)

	qsv := "qsv"
	device := "/dev/dri/renderD128"
	unchanged := func() *nodepool.Node {
		accel, path := qsv, device
		return &nodepool.Node{
			ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL,
			HWAccelOverride: &accel, HWDeviceOverride: &path,
		}
	}
	repo := &stubNodeRepository{updateResult: unchanged(), node: unchanged()}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	body := `{"name":"gpu-1","hw_accel_override":"qsv","hw_device_override":"/dev/dri/renderD128"}`
	awaitNodeUpdate(t, handler, httptest.NewRecorder(), body)

	select {
	case <-reloaded:
		t.Fatal("an edit that moved neither override still asked the node to reload")
	default:
	}
}

// An edit that touches no acceleration field must not cost a round trip to the
// node: renaming a node has nothing to do with what it probes.
func TestHandleUpdateNodeDoesNotReloadWithoutAnOverrideChange(t *testing.T) {
	reloaded := make(chan struct{}, 4)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/") {
			reloaded <- struct{}{}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(node.Close)

	stored := &nodepool.Node{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: node.URL}
	repo := &stubNodeRepository{updateResult: stored, node: stored}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	awaitNodeUpdate(t, handler, httptest.NewRecorder(), `{"name":"gpu-one"}`)

	select {
	case <-reloaded:
		t.Fatal("a rename asked the node to reload its configuration")
	default:
	}
}

// recordingEventBus captures publications so a test can assert the pool change
// actually reached the other replicas.
type recordingEventBus struct {
	mu     sync.Mutex
	events []cache.Event
}

func (b *recordingEventBus) Publish(_ context.Context, _ string, event cache.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	return nil
}

func (b *recordingEventBus) Subscribe(context.Context, string, cache.EventHandler) error { return nil }
func (b *recordingEventBus) Close() error                                                { return nil }

func (b *recordingEventBus) types() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	types := make([]string, 0, len(b.events))
	for _, event := range b.events {
		types = append(types, event.Type)
	}
	return types
}

// ctxAwareLister answers only for a live context, the way a database read does.
type ctxAwareLister struct{ nodes []*nodepool.Node }

func (l *ctxAwareLister) ListEnabled(ctx context.Context, _ string) ([]*nodepool.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return l.nodes, nil
}

// The pool reload runs after the response is written, so the request context is
// no longer the right lifetime: an admin whose browser gives up on a slow save
// cancels it, and the reload would then fail its reads and never publish. The
// row is already committed at that point, so this instance and every replica
// would keep dispatching under the old acceleration policy indefinitely —
// nothing else re-reads the column.
func TestReloadPoolsSurvivesRequestCancellation(t *testing.T) {
	bus := &recordingEventBus{}
	lister := &ctxAwareLister{nodes: []*nodepool.Node{{ID: 1, URL: "http://node", Enabled: true}}}
	handler := NewNodeHandler(&stubNodeRepository{}, nodepool.NewProxyPool(), nodepool.NewTranscodePool(), lister, bus, nil, "secret")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	handler.reloadPools(canceled)

	if got := bus.types(); len(got) != 1 || got[0] != string(cache.EventNodePoolChanged) {
		t.Fatalf("published %v, want a single %q after a canceled request", got, cache.EventNodePoolChanged)
	}
	if got := handler.transcodePool.Nodes(); len(got) != 1 {
		t.Fatalf("transcode pool holds %d nodes, want the reload to have landed", len(got))
	}
}

// Repointing a row at a different worker changes which machine those overrides
// apply to, even when the values are byte-identical. reloadPools publishes the
// new URL at once, so between that and the replacement's own 60s config poll
// this server dispatches the row's overridden backend to a worker still running
// on whatever it inherited — the same backend/device mismatch an override edit
// causes, reached by a different edit.
func TestHandleUpdateNodeReloadsTheReplacementWhenAURLMoves(t *testing.T) {
	reloaded := make(chan string, 4)
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/reload") {
			reloaded <- r.URL.Path
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(replacement.Close)

	qsv, device := "qsv", "/dev/dri/renderD128"
	repo := &stubNodeRepository{
		node: &nodepool.Node{
			ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: "http://retired-worker",
			HWAccelOverride: &qsv, HWDeviceOverride: &device,
		},
		updateResult: &nodepool.Node{
			ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: replacement.URL,
			HWAccelOverride: &qsv, HWDeviceOverride: &device,
		},
	}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	body := `{"name":"gpu-1","url":"` + replacement.URL + `","hw_accel_override":"qsv","hw_device_override":"/dev/dri/renderD128"}`
	awaitNodeUpdate(t, handler, httptest.NewRecorder(), body)

	select {
	case path := <-reloaded:
		if path != "/admin/reload-config" {
			t.Fatalf("nudged %q, want the non-destructive reload", path)
		}
	default:
		t.Fatal("a repointed row left the replacement worker on its inherited policy")
	}
}

// A trailing slash is not a repoint: the pools normalize URLs and the database
// column does not, so treating it as one would nudge on every unrelated save.
func TestNodePolicyTargetChangeIgnoresATrailingSlash(t *testing.T) {
	qsv := "qsv"
	before := &nodepool.Node{ID: 1, URL: "http://node/", HWAccelOverride: &qsv}
	sameAccel := qsv
	after := &nodepool.Node{ID: 1, URL: "http://node", HWAccelOverride: &sameAccel}

	if nodePolicyTargetChanged(before, after) {
		t.Fatal("a trailing-slash difference read as repointing the row")
	}
}

// A partial PUT that carries only a new url still repoints the row's existing
// overrides at a different worker. Loading the previous row only when an
// override field is present left nodePolicyTargetChanged with nothing to compare
// against, so the URL clause it grew for exactly this case never fired.
func TestHandleUpdateNodeReloadsOnAURLOnlyRepoint(t *testing.T) {
	reloaded := make(chan string, 4)
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/reload") {
			reloaded <- r.URL.Path
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(replacement.Close)

	qsv, device := "qsv", "/dev/dri/renderD128"
	repo := &stubNodeRepository{
		node: &nodepool.Node{
			ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: "http://retired-worker",
			HWAccelOverride: &qsv, HWDeviceOverride: &device,
		},
		updateResult: &nodepool.Node{
			ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: replacement.URL,
			HWAccelOverride: &qsv, HWDeviceOverride: &device,
		},
	}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")

	// Only the url: no acceleration field in the body at all.
	awaitNodeUpdate(t, handler, httptest.NewRecorder(), `{"url":"`+replacement.URL+`"}`)

	select {
	case path := <-reloaded:
		if path != "/admin/reload-config" {
			t.Fatalf("nudged %q, want the non-destructive reload", path)
		}
	default:
		t.Fatal("a url-only repoint left the replacement worker on its inherited policy")
	}
}

// awaitNodeUpdate runs one update and waits for the post-commit work it starts.
//
// HandleUpdateNode answers as soon as the row is committed and does the node
// nudge, cache drop and pool reload on their own goroutine — the nudge alone is
// bounded at ten seconds against a worker that may be unreachable, and an
// operator should not wait through that. Tests therefore wait on the handler's
// own completion signal rather than on the call returning.
func awaitNodeUpdate(t *testing.T, handler *NodeHandler, recorder *httptest.ResponseRecorder, body string) {
	t.Helper()
	done := make(chan struct{})
	handler.afterNodeUpdate = func() { close(done) }
	handler.HandleUpdateNode(recorder, updateNodeRequest(t, body))
	if recorder.Code < 200 || recorder.Code > 299 {
		// A rejected update commits nothing and starts no post-commit work, so
		// there is no signal coming; the test is asserting on the refusal.
		return
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for the post-commit node update work")
	}
}

// The advertised hash lives only on the pools — it is an observation from the
// health sweep, not stored state — while this endpoint serves database rows. If
// it is not carried across, the field is always absent and the admin page has
// no way to tell a report the node has already contradicted from a current one.
func TestHandleListNodesCarriesTheAdvertisedHashFromThePools(t *testing.T) {
	stored := "sha256:stored"
	repo := &stubNodeRepository{nodes: []*nodepool.Node{
		{ID: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: "http://gpu-1", CapabilitiesHash: &stored},
		{ID: 2, Name: "proxy-1", Type: nodepool.NodeTypeProxy, URL: "http://proxy-1"},
		{ID: 3, Name: "quiet", Type: nodepool.NodeTypeTranscode, URL: "http://quiet"},
	}}
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{ID: 1, URL: "http://gpu-1", Enabled: true},
		{ID: 3, URL: "http://quiet", Enabled: true},
	})
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{ID: 2, URL: "http://proxy-1", Enabled: true}})
	// The sweep learns each node's advertised hash on its health check.
	transcodes.ApplyHealth(1, "http://gpu-1", true, 0, 0, "sha256:newer", nil, time.Now())
	proxies.ApplyHealth(2, "http://proxy-1", true, 0, 0, "sha256:proxy", nil, time.Now())

	handler := NewNodeHandler(repo, proxies, transcodes, nil, nil, nil, "secret")
	recorder := httptest.NewRecorder()
	handler.HandleListNodes(recorder, httptest.NewRequest(http.MethodGet, "/admin/nodes", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var listed []struct {
		ID         int    `json:"id"`
		Hash       string `json:"capabilities_hash"`
		Advertised string `json:"advertised_capabilities_hash"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	byID := map[int]string{}
	for _, node := range listed {
		byID[node.ID] = node.Advertised
	}
	if byID[1] != "sha256:newer" {
		t.Fatalf("transcode advertised hash = %q, want the sweep's observation", byID[1])
	}
	if byID[2] != "sha256:proxy" {
		t.Fatalf("proxy advertised hash = %q, want the sweep's observation", byID[2])
	}
	// A node that has not been checked since this process started carries none,
	// and the field is omitted rather than reported as a mismatch.
	if byID[3] != "" {
		t.Fatalf("unchecked node advertised hash = %q, want it absent", byID[3])
	}
}

// A manual check is a health check like the sweep's: whatever it learns has to
// reach the pool the planner and the Nodes page read, not just the row.
func TestHandleCheckNodePublishesResultToThePool(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"active_jobs":       3,
			"egress_kbps":       4200,
			"capabilities_hash": "sha256:after",
			"system":            map[string]any{"scratch_free_gb": 0.5},
		})
	}))
	defer node.Close()

	stale := "sha256:before"
	pooled := &nodepool.Node{
		ID: 7, Name: "gpu-7", Type: nodepool.NodeTypeTranscode, URL: node.URL, Enabled: true,
		Healthy: true, ActiveJobs: 99, CapabilitiesHash: &stale, AdvertisedCapabilitiesHash: &stale,
	}
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{pooled})

	repo := &stubNodeRepository{node: pooled}
	handler := NewNodeHandler(repo, nil, pool, nil, nil, nil, "secret")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/nodes/7/check", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "7")
	handler.HandleCheckNode(recorder, request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	updated := pool.Nodes()
	if len(updated) != 1 {
		t.Fatalf("pool holds %d nodes, want 1", len(updated))
	}
	if got := updated[0].AdvertisedCapabilitiesHash; got == nil || *got != "sha256:after" {
		t.Errorf("pool advertised hash = %v, want the hash this check just read", got)
	}
	if got := updated[0].ActiveJobs; got != 3 {
		t.Errorf("pool active jobs = %d, want 3", got)
	}
	// The stats decide whether the planner keeps admitting work; a check that
	// found the scratch volume nearly full has to reach the planner's copy.
	if len(updated[0].LastStats) == 0 {
		t.Error("pool node kept no stats from the manual check")
	}
	if updated[0].LastHealthCheck == nil {
		t.Error("pool node kept no check timestamp, so the row and the pool disagree on freshness")
	}
}

// Three states, not two: a node that answers health checks with no hash is not
// the same as a node nobody has checked yet. The first is no longer standing
// behind the inventory stored for it — a build downgraded past capability
// reports — while the second is every node until the first sweep after a
// restart, and says nothing at all.
func TestHandleListNodesDistinguishesUncheckedFromUnreportedHashes(t *testing.T) {
	stored := "sha256:stored"
	none := ""
	repo := &stubNodeRepository{nodes: []*nodepool.Node{
		{ID: 1, Name: "checked", Type: nodepool.NodeTypeTranscode, URL: "http://gpu-1", CapabilitiesHash: &stored},
		{ID: 2, Name: "unchecked", Type: nodepool.NodeTypeTranscode, URL: "http://gpu-2", CapabilitiesHash: &stored},
	}}
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{
		{ID: 1, Name: "checked", Type: nodepool.NodeTypeTranscode, URL: "http://gpu-1", AdvertisedCapabilitiesHash: &none},
		{ID: 2, Name: "unchecked", Type: nodepool.NodeTypeTranscode, URL: "http://gpu-2"},
	})
	handler := NewNodeHandler(repo, nil, pool, nil, nil, nil, "secret")

	recorder := httptest.NewRecorder()
	handler.HandleListNodes(recorder, httptest.NewRequest(http.MethodGet, "/admin/nodes", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("returned %d nodes, want 2", len(items))
	}
	// Present and empty: the node was asked and named nothing.
	advertised, ok := items[0]["advertised_capabilities_hash"]
	if !ok || advertised != "" {
		t.Errorf("checked node advertised %v (present=%v), want an empty string", advertised, ok)
	}
	// Absent: nothing has asked, so the field must not claim the node said so.
	if _, ok := items[1]["advertised_capabilities_hash"]; ok {
		t.Errorf("unchecked node carried an advertised hash: %v", items[1])
	}
}
