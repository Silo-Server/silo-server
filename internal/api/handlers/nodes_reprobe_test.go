package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
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
	handler := NewNodeHandler(&stubNodeRepository{}, nil, nil, nil, nil, nil, nodeauthtest.SameKey("secret"))

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
