package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type configurationSnapshot struct {
	nodes []*nodepool.Node
	err   error
}
type scriptedNodeConfiguration struct {
	snapshots chan configurationSnapshot
	reads     chan struct{}
	// update, when set, is the committed row and locked pre-image Update returns.
	update *struct{ before, after *nodepool.Node }
}

func (s *scriptedNodeConfiguration) Snapshot(ctx context.Context) ([]*nodepool.Node, int64, error) {
	select {
	case s.reads <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	select {
	case out := <-s.snapshots:
		return out.nodes, 1, out.err
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
}
func (s *scriptedNodeConfiguration) Create(context.Context, nodepool.CreateNodeInput) (*nodepool.Node, error) {
	return nil, errors.New("unused")
}
func (s *scriptedNodeConfiguration) Update(context.Context, int, nodepool.UpdateNodeInput, func(int64) error) (*nodepool.Node, *nodepool.Node, error) {
	if s.update == nil {
		return nil, nil, errors.New("unused")
	}
	return s.update.after, s.update.before, nil
}
func (s *scriptedNodeConfiguration) Delete(context.Context, int, func(int64) error) error {
	return errors.New("unused")
}
func TestNodeConfigurationReconciliation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &scriptedNodeConfiguration{snapshots: make(chan configurationSnapshot, 3), reads: make(chan struct{}, 3)}
	pool := nodepool.NewProxyPool()
	h := &NodeHandler{proxyPool: pool}
	h.SetConfigurationStore(s)
	node := &nodepool.Node{ID: 1, AdminRevision: 1, Type: nodepool.NodeTypeProxy, URL: "http://node.invalid/", Enabled: true}
	pool.SetNodes([]*nodepool.Node{{ID: 9, URL: "http://stale.invalid", Enabled: true}})
	s.snapshots <- configurationSnapshot{err: errors.New("synthetic read failure")}
	h.StartConfigurationReconciliation(ctx)
	select {
	case <-s.reads:
	case <-time.After(time.Second):
		t.Fatal("no startup reconciliation")
	}
	s.snapshots <- configurationSnapshot{nodes: []*nodepool.Node{node}}
	h.configurationChanged()
	waitNodeConfiguration(t, func() bool { return pool.FindByURL("http://node.invalid") != nil })
	if pool.FindByURL("http://stale.invalid") != nil {
		t.Fatal("stale pool retained")
	}
	if node.URL != "http://node.invalid/" {
		t.Fatal("pool mutated stored snapshot")
	}
	// A late bridge/event read can repopulate old nodes. Another reconciliation
	// reapplies the authoritative empty list even with the same generation.
	pool.SetNodes([]*nodepool.Node{{ID: 9, URL: "http://stale.invalid", Enabled: true}})
	s.snapshots <- configurationSnapshot{}
	h.configurationChanged()
	waitNodeConfiguration(t, func() bool { return pool.FindByURL("http://stale.invalid") == nil })
}

func waitNodeConfiguration(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-deadline.C:
			t.Fatal("configuration did not converge")
		case <-tick.C:
		}
	}
}

// The v2 update keeps the v1 post-commit behavior: a change to the URL or an
// acceleration override nudges the worker's /admin/reload-config and drops this
// server's cached capabilities for the node; a rename does neither. The response
// is the committed row either way and does not wait on the worker.
func TestUpdateAdminNodeNudgesWorkerOnlyOnPolicyTargetChange(t *testing.T) {
	reloaded := make(chan string, 4)
	var destructive atomic.Bool
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/reload-config":
			reloaded <- r.Header.Get("Authorization")
		case "/admin/force-reload":
			destructive.Store(true)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(worker.Close)
	qsv, nvenc := "qsv", "nvenc"
	base := nodepool.Node{ID: 1, AdminRevision: 1, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: worker.URL, HWAccelOverride: &qsv}
	run := func(t *testing.T, after nodepool.Node, input nodepool.UpdateNodeInput) (reloads, invalidations int) {
		before := base
		after.AdminRevision = 2
		s := &scriptedNodeConfiguration{update: &struct{ before, after *nodepool.Node }{&before, &after}}
		h := NewNodeHandler(&stubNodeRepository{}, nil, nil, nil, nil, nil, "secret")
		h.SetConfigurationStore(s)
		invalidated := make(chan string, 4)
		h.SetCapabilityInvalidator(func(url string) { invalidated <- url })
		done := make(chan struct{})
		h.afterNodeUpdate = func() { close(done) }
		node, err := h.UpdateAdminNode(t.Context(), 1, input, func(int64) error { return nil })
		if err != nil || node.AdminRevision != 2 {
			t.Fatalf("update: %v %v", node, err)
		}
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("post-commit work did not finish")
		}
		for len(reloaded) > 0 {
			if auth := <-reloaded; auth != "Bearer secret" {
				t.Fatalf("authorization %q", auth)
			}
			reloads++
		}
		for len(invalidated) > 0 {
			if url := <-invalidated; url != worker.URL {
				t.Fatalf("invalidated %q, want %q", url, worker.URL)
			}
			invalidations++
		}
		return reloads, invalidations
	}
	t.Run("override change nudges and invalidates", func(t *testing.T) {
		after := base
		after.HWAccelOverride = &nvenc
		if r, i := run(t, after, nodepool.UpdateNodeInput{HWAccelOverride: &nvenc}); r != 1 || i != 1 {
			t.Fatalf("reloads=%d invalidations=%d, want 1 and 1", r, i)
		}
	})
	t.Run("rename does neither", func(t *testing.T) {
		after := base
		after.Name = "gpu-renamed"
		if r, i := run(t, after, nodepool.UpdateNodeInput{Name: &after.Name}); r != 0 || i != 0 {
			t.Fatalf("reloads=%d invalidations=%d, want 0 and 0", r, i)
		}
	})
	if destructive.Load() {
		t.Fatal("a policy edit hit the destructive force-reload route")
	}
}
