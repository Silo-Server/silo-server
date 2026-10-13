package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func raceRuntime(t *testing.T) *Runtime {
	t.Helper()
	corePublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, nodePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: "http://core.example.test", CorePublicKey: corePublic, NodePrivateKey: nodePrivate, NodeURL: "http://worker.example.test", AllowHTTP: true}, Scratch: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := r.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return r
}

func raceAssignment(generation uint64) workerwire.Assignment {
	return workerwire.Assignment{Version: 1, NodeID: 17, WorkID: "work", Generation: generation, Sources: map[string]workerwire.Source{"source:media": {Ref: workerwire.SourceRef{ID: "media", Kind: "media", Revision: "revision"}, Origin: "http://core.example.test", URL: "http://core.example.test/source", Grant: "grant", ETag: `"revision"`, Size: 10, NodeID: 17, WorkID: "work", Generation: generation}}}
}

func raceSnapshot(r *Runtime, sequence, generation uint64, validity int) workerwire.Snapshot {
	return workerwire.Snapshot{Version: 1, NodeID: 17, BootID: r.bootID, Nonce: "test-nonce", Sequence: sequence, Active: map[string]uint64{"work": generation}, ValidForMillis: validity}
}

func TestExpiryLookupAndOpenSourcesDoNotDeadlock(t *testing.T) {
	r := raceRuntime(t)
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	sent := time.Now()
	if err := r.authority.Apply(raceSnapshot(r, 1, 4, 20), sent); err != nil {
		t.Fatal(err)
	}
	handles, err := r.openSources(raceAssignment(4))
	if err != nil {
		t.Fatal(err)
	}
	h := handles["source:media"]
	// Hold the generation fence while the expiry callback needs that same
	// fence. Input lookups must still return instead of waiting on teardown.
	r.mu.Lock()
	expired := time.NewTimer(time.Until(sent.Add(25 * time.Millisecond)))
	<-expired.C
	done := make(chan bool, 1)
	go func() {
		ctx := workerinput.WithContext(t.Context(), r.inputs)
		_, found := workerinput.Lookup(ctx, h.URL())
		done <- found || r.inputs.Allowed(h.URL())
	}()
	select {
	case allowed := <-done:
		if allowed {
			r.mu.Unlock()
			t.Fatal("expired input remained allowed")
		}
	case <-time.After(time.Second):
		r.mu.Unlock()
		t.Fatal("expiry lookup waited for generation teardown")
	}
	r.mu.Unlock()
	opened := make(chan error, 1)
	go func() { _, err := r.openSources(raceAssignment(5)); opened <- err }()
	select {
	case err := <-opened:
		if err == nil {
			t.Fatal("source opened with expired authority")
		}
	case <-time.After(time.Second):
		t.Fatal("openSources deadlocked with expiry callback")
	}
}

func TestDelayedRevocationCannotCloseSuccessorInputs(t *testing.T) {
	r := raceRuntime(t)
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	if err := r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); err != nil {
		t.Fatal(err)
	}
	old, err := r.openSources(raceAssignment(4))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.authority.Apply(raceSnapshot(r, 2, 5, 2000), time.Now()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 16 {
			r.revoked("work", 4)
		}
	}()
	go func() { defer wg.Done(); _, err := r.openSources(raceAssignment(5)); errors <- err }()
	wg.Wait()
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	successor := r.handles["work"]["source:media"]
	generation := r.generations["work"]
	r.mu.Unlock()
	if generation != 5 || successor == nil {
		t.Fatal("old revocation removed replacement generation")
	}
	ctx := workerinput.WithContext(t.Context(), r.inputs)
	if _, found := workerinput.Lookup(ctx, successor.URL()); !found {
		t.Fatal("old revocation closed replacement input")
	}
	if _, found := workerinput.Lookup(ctx, old["source:media"].URL()); found {
		t.Fatal("old generation input survived replacement")
	}
}

func TestConcurrentSameGenerationReconstructionKeepsPublishedInput(t *testing.T) {
	r := raceRuntime(t)
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	if err := r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.inputs.Close(); err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(release)
	var calls atomic.Int64
	adapter, err := workerinput.New(workerinput.Options{
		Origins: []string{"http://core.example.test"}, AllowHTTP: true,
		Allowed: func(string, uint64) bool {
			if calls.Add(1) == 1 {
				close(entered)
				<-resume
			}
			return true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.inputs = adapter
	type result struct {
		handles map[string]*workerinput.Handle
		err     error
	}
	slow := make(chan result, 1)
	go func() {
		handles, err := r.openSources(raceAssignment(4))
		slow <- result{handles: handles, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first reconstruction did not enter source admission")
	}
	// Both calls passed the first absence check. The second call publishes
	// while the first still opens redundant sources outside Runtime.mu.
	first, err := r.openSources(raceAssignment(4))
	if err != nil {
		t.Fatal(err)
	}
	published := first["source:media"]
	if !adapter.Allowed(published.URL()) {
		t.Fatal("first published input is unavailable")
	}
	release()
	var second result
	select {
	case second = <-slow:
	case <-time.After(time.Second):
		t.Fatal("redundant reconstruction did not finish")
	}
	if second.err != nil {
		t.Fatal(second.err)
	}
	if second.handles["source:media"] != published || !adapter.Allowed(published.URL()) {
		t.Fatal("same-generation reconstruction replaced or closed an input already in use")
	}
	r.renewals.prune()
	r.renewals.mu.Lock()
	_, tracked := r.renewals.entries[published]
	count := len(r.renewals.entries)
	r.renewals.mu.Unlock()
	if !tracked || count != 1 {
		t.Fatalf("published source renewal lost or redundant source retained: tracked%v count%d", tracked, count)
	}
	r.revoked("work", 4)
	if adapter.Allowed(published.URL()) {
		t.Fatal("reused generation survived its revocation")
	}
}
