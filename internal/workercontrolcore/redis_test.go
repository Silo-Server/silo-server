package workercontrolcore

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

//nolint:funlen,gocyclo // One Redis timeline correlates concurrent admission, generation fences, revocation, and replay assertions.
func TestRedisDurableConcurrentAdmissionRevocationAndReplay(t *testing.T) {
	address := os.Getenv("SILO_WORKER_TEST_REDIS")
	if address == "" {
		t.Skip("requires disposable Redis SILO_WORKER_TEST_REDIS")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	prefix := "silo:worker-test:" + uuid.NewString() + ":"
	first := NewRedisStore(client, prefix)
	second := NewRedisStore(client, prefix)
	t.Cleanup(func() {
		keys, err := client.Keys(t.Context(), prefix+"*").Result()
		if err == nil && len(keys) > 0 {
			_ = client.Del(t.Context(), keys...).Err()
		}
	})
	a, err := first.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "session", Body: []byte(`{"original_started_at_unix_nano":1791910800123456789,"input_path":"source:opaque"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = first.BindInputs(t.Context(), 7, "session", a.Generation, map[string]string{"source:opaque": "digest"}); err != nil {
		t.Fatal(err)
	}
	if err = first.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = first.Activate(t.Context(), 7, "session", a.Generation); err != nil {
		t.Fatal(err)
	}
	reconstructed, ok, err := second.Get(t.Context(), 7, "session")
	if err != nil || !ok {
		t.Fatal(err)
	}
	var facts struct {
		Timestamp int64 `json:"original_started_at_unix_nano"`
	}
	if err = json.Unmarshal(reconstructed.Body, &facts); err != nil || facts.Timestamp != 1791910800123456789 {
		t.Fatalf("recipe timestamp lost precision: %+v %v", facts, err)
	}
	bindings, err := second.InputBindings(t.Context(), 7, "session", a.Generation)
	if err != nil || bindings["source:opaque"] != "digest" {
		t.Fatal("core private source binding lost on restart")
	}
	var wait sync.WaitGroup
	generations := make(chan uint64, 32)
	for range 32 {
		wait.Go(func() {
			candidate, e := second.Begin(t.Context(), a)
			if e != nil {
				t.Error(e)
				return
			}
			generations <- candidate.Generation
		})
	}
	wait.Wait()
	close(generations)
	seen := map[uint64]bool{}
	var highest uint64
	for gen := range generations {
		if seen[gen] {
			t.Fatal("duplicate generation")
		}
		seen[gen] = true
		if gen > highest {
			highest = gen
		}
	}
	if len(seen) != 32 {
		t.Fatal("missing concurrent admission")
	}
	live, ok, err := second.Get(t.Context(), 7, "session")
	if err != nil || !ok || live.Generation != a.Generation {
		t.Fatal("concurrent candidates removed healthy live")
	}
	candidate, ok, err := first.Candidate(t.Context(), 7, "session")
	if err != nil || !ok || candidate.Generation != highest {
		t.Fatal("latest candidate not durable")
	}
	snapshot, err := second.Snapshot(t.Context(), 7)
	if err != nil || snapshot.Active["session"].Generation != a.Generation {
		t.Fatal("non-atomic or missing live snapshot")
	}
	if err = first.TouchLease(t.Context(), 7, "session", candidate.Generation, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if leased, e := second.HasLease(t.Context(), 7, "session", candidate.Generation); e != nil || !leased {
		t.Fatal("pending lease not shared")
	}
	if err = first.Complete(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	snapshot, err = second.Snapshot(t.Context(), 7)
	if err != nil || snapshot.Active["session"].Generation != a.Generation || snapshot.Pending["session"].Generation != candidate.Generation {
		t.Fatal("ready candidate omitted live snapshot")
	}
	if err = second.Stop(t.Context(), 7, "session"); err != nil {
		t.Fatal(err)
	}
	if leased, e := first.HasLease(t.Context(), 7, "session", candidate.Generation); e != nil || leased {
		t.Fatal("stop left shared pending authority")
	}
	if err = first.Activate(t.Context(), 7, "session", candidate.Generation); !errors.Is(err, ErrFenced) {
		t.Fatalf("revoked candidate published: %v", err)
	}
	if _, ok, err = first.Get(t.Context(), 7, "session"); err != nil || ok {
		t.Fatal("stopped assignment reconstructed")
	}
	next, err := second.Begin(t.Context(), a)
	if err != nil || next.Generation <= highest+1 {
		t.Fatal("stop high water not durable")
	}
	nonce := "0123456789abcdef0123456789abcdef"
	if err = first.ClaimNonce(t.Context(), 7, nonce, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = second.ClaimNonce(t.Context(), 7, nonce, time.Now().Add(time.Minute)); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-core replay accepted: %v", err)
	}
}
