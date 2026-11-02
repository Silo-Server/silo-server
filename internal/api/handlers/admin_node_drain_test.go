package handlers

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/nodepool"
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
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{node})
	lister := &ctxAwareLister{}
	bus := &recordingEventBus{}
	handler := NewNodeHandler(repo, nodepool.NewProxyPool(), transcodes, lister, bus, nil, "secret")

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
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, "secret")
	if _, err := handler.SetAdminNodeDrain(context.Background(), 1, true); !errors.Is(err, ErrNodeNotDrainable) {
		t.Fatalf("draining a proxy = %v, want ErrNodeNotDrainable", err)
	}
	if len(repo.requests) != 0 {
		t.Fatal("a proxy drain request was stored")
	}
	if _, err := NewNodeHandler(&drainNodeRepository{}, nil, nil, nil, nil, nil, "").SetAdminNodeDrain(context.Background(), 9, true); !errors.Is(err, nodepool.ErrNodeNotFound) {
		t.Fatalf("draining an unknown node = %v, want ErrNodeNotFound", err)
	}
}
