package handlers

import (
	"context"
	"errors"
	"sync"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
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

func (s *stubNodeRepository) UpdateHealth(context.Context, int, string, bool, int, int, []byte, netaccess.NodeNetworkAccess) error {
	return nil
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

// failingLister fails every read, as a database outage does.
type failingLister struct{}

func (failingLister) ListEnabled(context.Context, string) ([]*nodepool.Node, error) {
	return nil, errors.New("database unavailable")
}
