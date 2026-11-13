package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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
			if !handler.reloadNodeConfig(context.Background(), &nodepool.Node{ID: 1, Name: "gpu-1", URL: node.URL}) {
				t.Fatalf("HTTP %d was not accepted as a confirmed reload", status)
			}

			select {
			case <-called:
			default:
				t.Fatal("the node was never called")
			}
		})
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

func TestReadAdminNodesDoesNotMutateRepositoryRows(t *testing.T) {
	stored := &nodepool.Node{ID: 1, URL: "http://worker.example.test", Type: nodepool.NodeTypeProxy, Enabled: true}
	repo := &stubNodeRepository{nodes: []*nodepool.Node{stored}}
	pool := nodepool.NewProxyPool()
	pool.SetNodes([]*nodepool.Node{stored})
	pool.ApplyHealth(1, stored.URL, true, 0, 0, "advertised", nil, nil, time.Now())
	rows, err := NewNodeHandler(repo, pool, nil, nil, nil, nil, "").ReadAdminNodes(t.Context())
	if err != nil || len(rows) != 1 || rows[0] == stored || stored.AdvertisedCapabilitiesHash != nil || rows[0].AdvertisedCapabilitiesHash == nil || *rows[0].AdvertisedCapabilitiesHash != "advertised" {
		t.Fatal(rows, stored, err)
	}
}
