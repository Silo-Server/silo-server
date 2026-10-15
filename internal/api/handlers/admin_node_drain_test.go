package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodepool/nodepooltest"
)

// drainNodeRepository is a node repository that records drain requests.
type drainNodeRepository struct {
	stubNodeRepository
	requests []bool
}

func (r *drainNodeRepository) SetDrainRequested(_ context.Context, _ int, requested bool) (*nodepool.Node, error) {
	r.requests = append(r.requests, requested)
	n := *r.node
	n.DrainRequested = requested
	r.node = &n
	return r.node, nil
}

// Draining a transcode node takes effect on this server's pool before the
// request returns, and the pool change is announced for the other servers and
// the node itself.
func TestSetAdminNodeDrainUpdatesPoolsAndAnnounces(t *testing.T) {
	node := &nodepool.Node{ID: 3, Name: "tc", Type: nodepool.NodeTypeTranscode, URL: "http://tc", Enabled: true, Healthy: true}
	repo := &drainNodeRepository{stubNodeRepository: stubNodeRepository{node: node}}
	transcodes := nodepooltest.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{node})
	lister := &ctxAwareLister{}
	bus := &recordingEventBus{}
	handler := NewNodeHandler(repo, nodepool.NewProxyPool(), transcodes, lister, bus, nil, nodeauthtest.SharedSecret("secret"))

	lister.nodes = []*nodepool.Node{{ID: 3, Name: "tc", Type: nodepool.NodeTypeTranscode, URL: "http://tc", Enabled: true, Healthy: true, DrainRequested: true}}
	got, err := handler.SetAdminNodeDrain(context.Background(), 3, true)
	if err != nil || !got.DrainRequested {
		t.Fatalf("SetAdminNodeDrain = %+v, %v", got, err)
	}
	if !transcodes.Nodes()[0].Draining() {
		t.Fatal("this server's pool does not see the drain")
	}
	if !slices.Contains(bus.types(), cache.EventNodePoolChanged) {
		t.Fatalf("published %v, want node_pool_changed", bus.types())
	}
	if len(repo.requests) != 1 || !repo.requests[0] {
		t.Fatalf("stored requests = %v", repo.requests)
	}
}

func TestSetAdminNodeDrainRefusesProxies(t *testing.T) {
	repo := &drainNodeRepository{stubNodeRepository: stubNodeRepository{node: &nodepool.Node{ID: 1, Type: nodepool.NodeTypeProxy}}}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, nodeauthtest.SharedSecret("secret"))
	if _, err := handler.SetAdminNodeDrain(context.Background(), 1, true); !errors.Is(err, ErrNodeNotDrainable) {
		t.Fatalf("draining a proxy = %v, want ErrNodeNotDrainable", err)
	}
	if len(repo.requests) != 0 {
		t.Fatal("a proxy drain request was stored")
	}
	if _, err := NewNodeHandler(&drainNodeRepository{}, nil, nil, nil, nil, nil, nodeauthtest.SharedSecret("")).SetAdminNodeDrain(context.Background(), 9, true); !errors.Is(err, nodepool.ErrNodeNotFound) {
		t.Fatalf("draining an unknown node = %v, want ErrNodeNotFound", err)
	}
}

// A transcode node reads its drain request in its configuration, so a change
// is pushed to it at once (/admin/reload-config) rather than left to its next
// poll; repeating the same request pushes nothing.
func TestSetAdminNodeDrainPushesTheChangeToTheNode(t *testing.T) {
	reloads := make(chan string, 4)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reloads <- r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(worker.Close)
	node := &nodepool.Node{ID: 3, Name: "tc", Type: nodepool.NodeTypeTranscode, URL: worker.URL, Enabled: true, Healthy: true}
	repo := &drainNodeRepository{stubNodeRepository: stubNodeRepository{node: node}}
	handler := NewNodeHandler(repo, nodepool.NewProxyPool(), nodepooltest.NewTranscodePool(), &ctxAwareLister{}, &recordingEventBus{}, nil, nodeauthtest.SharedSecret("secret"))

	if _, err := handler.SetAdminNodeDrain(context.Background(), 3, true); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-reloads:
		if path != "/admin/reload-config" {
			t.Fatalf("pushed to %s, want /admin/reload-config", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the drain request was not pushed to the node")
	}
	if _, err := handler.SetAdminNodeDrain(context.Background(), 3, true); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.SetAdminNodeDrain(context.Background(), 3, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reloads:
	case <-time.After(5 * time.Second):
		t.Fatal("lifting the drain was not pushed to the node")
	}
	select {
	case <-reloads:
		t.Fatal("repeating an unchanged drain request pushed it again")
	case <-time.After(200 * time.Millisecond):
	}
}
