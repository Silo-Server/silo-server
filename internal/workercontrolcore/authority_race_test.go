package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type authorityGatedNodes struct {
	testNodes
	entered chan struct{}
	resume  chan struct{}
	calls   atomic.Int64
}

func (n *authorityGatedNodes) ByID(ctx context.Context, id int) (Node, bool, error) {
	if n.calls.Add(1) == 1 {
		close(n.entered)
		select {
		case <-n.resume:
		case <-ctx.Done():
			return Node{}, false, ctx.Err()
		}
	}
	return n.testNodes.ByID(ctx, id)
}

func authorityTestService(t *testing.T) (*Service, *MemoryStore, Node) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	service, err := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, node
}

func activateAuthorityWork(t *testing.T, store *MemoryStore, work string) workerwire.Assignment {
	t.Helper()
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: work})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), 7, work, a.Generation); err != nil {
		t.Fatal(err)
	}
	return a
}

func awaitAuthorityGate(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("authority lookup did not reach its gate")
	}
}

func awaitAuthorityResult(t *testing.T, result <-chan bool) bool {
	t.Helper()
	select {
	case active := <-result:
		return active
	case <-time.After(3 * time.Second):
		t.Fatal("authority lookup did not complete")
		return false
	}
}

func TestAuthorityInvalidationDoesNotDenyUnchangedLiveWork(t *testing.T) {
	for _, change := range []string{"unrelated-work", "finish-admission", "older-generation-cutoff"} {
		t.Run(change, func(t *testing.T) {
			service, store, node := authorityTestService(t)
			a := activateAuthorityWork(t, store, "live")
			old := a
			if change == "older-generation-cutoff" {
				a = activateAuthorityWork(t, store, "live")
			}
			nodes := &authorityGatedNodes{testNodes: testNodes{[]Node{node}}, entered: make(chan struct{}), resume: make(chan struct{})}
			service.options.Nodes = nodes
			release := sync.OnceFunc(func() { close(nodes.resume) })
			defer release()
			result := make(chan bool, 1)
			go func() { result <- service.WorkActive(t.Context(), node.ID, a.WorkID, a.Generation) }()
			awaitAuthorityGate(t, nodes.entered)
			switch change {
			case "unrelated-work":
				service.invalidateCached(node.ID, "other")
			case "finish-admission":
				service.finishAdmission(a)
			case "older-generation-cutoff":
				service.invalidateThrough(node.ID, a.WorkID, old.Generation)
			}
			release()
			if !awaitAuthorityResult(t, result) {
				t.Fatal("unchanged live source authority was transiently denied")
			}
			if change == "finish-admission" && nodes.calls.Load() < 2 {
				t.Fatal("same-work invalidation did not revalidate authority")
			}
			if change != "finish-admission" && nodes.calls.Load() != 1 {
				t.Fatal("an unrelated scope forced another authority lookup")
			}
		})
	}
}

func TestAuthorityRevalidationFencesChangesAfterStoreLookup(t *testing.T) {
	for _, change := range []string{"stop", "replacement", "policy", "disabled-node", "canceled-context"} {
		t.Run(change, func(t *testing.T) {
			service, store, node := authorityTestService(t)
			a := activateAuthorityWork(t, store, "live")
			entered, resume := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(resume) })
			defer release()
			var once sync.Once
			var policy atomic.Bool
			policy.Store(true)
			service.options.ExecutionActive = func(context.Context, Node, workerwire.Assignment) bool {
				allowed := policy.Load()
				once.Do(func() {
					close(entered)
					<-resume
				})
				return allowed
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan bool, 1)
			go func() { result <- service.WorkActive(ctx, node.ID, a.WorkID, a.Generation) }()
			awaitAuthorityGate(t, entered)
			switch change {
			case "stop":
				if err := service.stop(t.Context(), node, a.WorkID); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				_ = activateAuthorityWork(t, store, a.WorkID)
				service.invalidateCached(node.ID, a.WorkID)
			case "policy":
				policy.Store(false)
				service.invalidateCached(node.ID, a.WorkID)
			case "disabled-node":
				// The first lookup already copied the node. Replace the registry
				// before releasing the policy callback to require a fresh check.
				node.Enabled = false
				service.options.Nodes = testNodes{[]Node{node}}
				service.invalidateCached(node.ID, a.WorkID)
			case "canceled-context":
				cancel()
			}
			release()
			if awaitAuthorityResult(t, result) {
				t.Fatal("stale or canceled lookup authorized the source")
			}
			if change == "canceled-context" && !service.WorkActive(t.Context(), node.ID, a.WorkID, a.Generation) {
				t.Fatal("canceled lookup poisoned live authority for another request")
			}
		})
	}
}

func TestAuthorityRepeatedInvalidationIsBoundedAndCancelable(t *testing.T) {
	for _, cancelDuringRetry := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelDuringRetry), func(t *testing.T) {
			service, store, node := authorityTestService(t)
			a := activateAuthorityWork(t, store, "changing")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var checks int
			service.options.ExecutionActive = func(context.Context, Node, workerwire.Assignment) bool {
				checks++
				service.invalidateCached(node.ID, a.WorkID)
				if cancelDuringRetry && checks == 2 {
					cancel()
				}
				return true
			}
			if service.WorkActive(ctx, node.ID, a.WorkID, a.Generation) {
				t.Fatal("continuously invalidated lookup returned stale authority")
			}
			if checks < 2 || checks > 3 {
				t.Fatalf("unbounded or missing revalidation: %d checks", checks)
			}
			if len(service.authorityChecks) != 0 || len(service.authority) != 0 {
				t.Fatal("abandoned validation retained state or cached a result")
			}
		})
	}
}

func TestAuthorityCacheEvictionDoesNotFenceUnrelatedInFlightLookup(t *testing.T) {
	service, store, node := authorityTestService(t)
	a := activateAuthorityWork(t, store, "live")
	nodes := &authorityGatedNodes{testNodes: testNodes{[]Node{node}}, entered: make(chan struct{}), resume: make(chan struct{})}
	service.options.Nodes = nodes
	release := sync.OnceFunc(func() { close(nodes.resume) })
	defer release()
	result := make(chan bool, 1)
	go func() { result <- service.WorkActive(t.Context(), node.ID, a.WorkID, a.Generation) }()
	awaitAuthorityGate(t, nodes.entered)
	for i := range 10001 {
		work := fmt.Sprintf("historical-%d", i)
		if service.WorkActive(t.Context(), node.ID, work, 1) {
			t.Fatal("missing historical work was authorized")
		}
	}
	for i := range 10001 {
		service.invalidateCached(node.ID, fmt.Sprintf("historical-%d", i))
	}
	release()
	if !awaitAuthorityResult(t, result) {
		t.Fatal("historical work churn fenced another work's live lookup")
	}
	if len(service.authorityChecks) != 0 {
		t.Fatal("completed historical lookups retained revision tickets")
	}
	if len(service.authority) > 10000 {
		t.Fatal("authority cache exceeded its bound")
	}
}

type authorityBlockedNodes struct {
	testNodes
	entered chan struct{}
	resume  chan struct{}
	calls   atomic.Int64
}

func (n *authorityBlockedNodes) ByID(ctx context.Context, id int) (Node, bool, error) {
	n.calls.Add(1)
	n.entered <- struct{}{}
	select {
	case <-n.resume:
		return n.testNodes.ByID(ctx, id)
	case <-ctx.Done():
		return Node{}, false, ctx.Err()
	}
}

func TestAuthorityConcurrentLookupTicketsAreBoundedAndReleased(t *testing.T) {
	service, _, node := authorityTestService(t)
	const capacity = 10000
	nodes := &authorityBlockedNodes{testNodes: testNodes{[]Node{node}}, entered: make(chan struct{}, capacity+1), resume: make(chan struct{})}
	service.options.Nodes = nodes
	release := sync.OnceFunc(func() { close(nodes.resume) })
	var waiting sync.WaitGroup
	defer func() {
		release()
		waiting.Wait()
	}()
	for i := range capacity {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			if service.WorkActive(t.Context(), node.ID, fmt.Sprintf("pending-%d", i), 1) {
				t.Error("missing work was authorized")
			}
		}()
	}
	for range capacity {
		awaitAuthorityGate(t, nodes.entered)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if service.WorkActive(ctx, node.ID, "over-capacity", 1) || nodes.calls.Load() != capacity {
		t.Fatal("full ticket registry admitted another backend lookup")
	}
	release()
	waiting.Wait()
	if len(service.authorityChecks) != 0 || len(service.authority) > capacity {
		t.Fatal("finished concurrent lookups leaked tickets or exceeded the cache bound")
	}
	if service.WorkActive(t.Context(), node.ID, "after-release", 1) {
		t.Fatal("missing work was authorized after capacity recovered")
	}
	if nodes.calls.Load() != capacity+1 {
		t.Fatal("completed lookups did not release ticket capacity")
	}
}
