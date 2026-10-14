package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestOutputSweepReclaimsStoppedOrdinarySessionsBeyondByteBudget(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example.test", PublicKey: public, Enabled: true}
	store, registry := NewMemoryStore(), NewMemoryOutputAuthorities()
	claims := make(map[string]*streamtoken.Claims)
	service, err := New(Options{Origin: "https://core.example.test", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, OutputAuthorities: registry, VerifyToken: func(_ context.Context, token string) (*streamtoken.Claims, error) { return claims[token], nil }})
	if err != nil {
		t.Fatal(err)
	}
	// Forty admitted tokens exceed the byte quota twice over if stopped
	// sessions are retained for the customer's full 24-hour token lifetime.
	for i := range 40 {
		work := fmt.Sprintf("playback-%d", i)
		a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: node.ID, WorkID: work, Path: "/transcode/start"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Complete(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		if err := store.Activate(t.Context(), node.ID, work, a.Generation); err != nil {
			t.Fatal(err)
		}
		token := strings.Repeat("t", 60<<10) + work
		claims[token] = &streamtoken.Claims{OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: "login"}, SessionID: work, TranscodeNode: node.URL, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour))}}
		_, err = registry.Register(t.Context(), OutputAuthorityRecord{Token: token, NodeID: node.ID, WorkID: work, Generation: a.Generation, Epoch: "epoch", ExpiresAt: claims[token].ExpiresAt.Unix()})
		if err != nil {
			t.Fatalf("stopped sessions exhausted authority capacity at play %d: %v", i, err)
		}
		if err := store.Stop(t.Context(), node.ID, work); err != nil {
			t.Fatal(err)
		}
		if _, err := service.currentOutputs(t.Context(), node); err != nil {
			t.Fatal(err)
		}
		records, err := registry.List(t.Context(), node.ID)
		if err != nil || len(records) != 0 {
			t.Fatalf("stopped play %d retained %d private authority records: %v", i, len(records), err)
		}
	}
}

func TestOutputAuthorityRenewalFencesStaleDeletion(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testOutputAuthorityRenewalFencesDeletion(t, NewMemoryOutputAuthorities()) })
	t.Run("redis", func(t *testing.T) {
		raw := os.Getenv("SILO_TEST_REDIS_URL")
		if raw == "" {
			t.Skip("SILO_TEST_REDIS_URL is not set")
		}
		options, err := redis.ParseURL(raw)
		if err != nil {
			t.Fatal(err)
		}
		client := redis.NewClient(options)
		t.Cleanup(func() { _ = client.Close() })
		prefix := "silo:output-lifecycle-test:" + uuid.NewString() + ":"
		t.Cleanup(func() {
			keys, err := client.Keys(context.Background(), prefix+"*").Result()
			if err == nil && len(keys) > 0 {
				_ = client.Del(context.Background(), keys...).Err()
			}
		})
		testOutputAuthorityRenewalFencesDeletion(t, NewRedisOutputAuthorities(client, prefix), NewRedisOutputAuthorities(client, prefix))
		t.Run("pre-revision stopped record", func(t *testing.T) { testLegacyRedisOutputSweep(t, client, prefix) })
	})
}

type outputLookupFailureStore struct {
	AssignmentStore
	err error
}

func (s *outputLookupFailureStore) Get(ctx context.Context, node int, work string) (workerwire.Assignment, bool, error) {
	if s.err != nil {
		return workerwire.Assignment{}, false, s.err
	}
	return s.AssignmentStore.Get(ctx, node, work)
}

func TestOutputSweepPreservesRecoverableDenialAndLongResponse(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example.test", PublicKey: public, Enabled: true}
	store, registry := NewMemoryStore(), NewMemoryOutputAuthorities()
	lookup := &outputLookupFailureStore{AssignmentStore: store}
	now := time.Now()
	registry.now = func() time.Time { return now }
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: node.ID, WorkID: "long-response", Path: "/transcode/start"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(t.Context(), node.ID, a.WorkID, a.Generation); err != nil {
		t.Fatal(err)
	}
	claims := streamtoken.Claims{OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: "login"}, SessionID: a.WorkID, TranscodeNode: node.URL, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}}
	id, err := registry.Register(t.Context(), OutputAuthorityRecord{Token: "long-customer-token", NodeID: node.ID, WorkID: a.WorkID, Generation: a.Generation, Epoch: "epoch", ExpiresAt: claims.ExpiresAt.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	var failure error
	service, err := New(Options{Origin: "https://core.example.test", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: lookup, OutputAuthorities: registry, VerifyToken: func(context.Context, string) (*streamtoken.Claims, error) { return &claims, failure }})
	if err != nil {
		t.Fatal(err)
	}
	check := func(wantAllowed bool, wantRecords int) {
		t.Helper()
		outputs, err := service.currentOutputs(t.Context(), node)
		if err != nil || outputs[id] != wantAllowed {
			t.Fatalf("allowed=%v want=%v: %v", outputs[id], wantAllowed, err)
		}
		records, err := registry.List(t.Context(), node.ID)
		if err != nil || len(records) != wantRecords {
			t.Fatalf("retained=%d want=%d: %v", len(records), wantRecords, err)
		}
	}
	check(true, 1)
	now = now.Add(6 * time.Minute)
	check(true, 1) // A healthy open response outlives request admission.
	for _, transient := range []error{errors.New("catalog unavailable"), ErrDenied} {
		failure = transient
		check(false, 1)
		failure = nil
		check(true, 1)
	}
	lookup.err = errors.New("assignment store unavailable")
	check(false, 1)
	lookup.err = nil
	check(true, 1)
	failure = ErrOutputRevoked
	check(false, 0)
}

func TestOutputAuthorityCapacityRetrySweepsRetiredRecords(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example.test", PublicKey: public, Enabled: true}
	store, registry := NewMemoryStore(), NewMemoryOutputAuthorities()
	service, err := New(Options{Origin: "https://core.example.test", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, OutputAuthorities: registry, VerifyToken: func(context.Context, string) (*streamtoken.Claims, error) { return nil, ErrDenied }})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		_, err := registry.Register(t.Context(), OutputAuthorityRecord{Token: strings.Repeat("t", 60<<10) + fmt.Sprint(i), NodeID: node.ID, WorkID: fmt.Sprintf("retired-%d", i), Generation: 1, Epoch: "epoch", ExpiresAt: time.Now().Add(time.Hour).Unix()})
		if errors.Is(err, ErrUnavailable) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	newRecord := OutputAuthorityRecord{Token: "fresh-customer-token", NodeID: node.ID, WorkID: "fresh-work", Generation: 1, Epoch: "epoch", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	// Ensure the test reaches the byte quota even for a short new token.
	newRecord.Token = strings.Repeat("f", 60<<10)
	if _, err := registry.Register(t.Context(), newRecord); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fixture did not exhaust hard byte quota: %v", err)
	}
	if _, err := service.registerOutputAuthority(t.Context(), node, newRecord); err != nil {
		t.Fatalf("retired records stranded a fresh viewer: %v", err)
	}
	records, err := registry.List(t.Context(), node.ID)
	if err != nil || len(records) != 1 || records[0].WorkID != newRecord.WorkID {
		t.Fatal("capacity retry failed to reclaim retired records")
	}
}

func testOutputAuthorityRenewalFencesDeletion(t *testing.T, registry OutputAuthorities, replicas ...OutputAuthorities) {
	t.Helper()
	deleter, ok := registry.(interface {
		Delete(context.Context, OutputAuthorityRecord) error
	})
	if !ok {
		t.Fatal("registry cannot reclaim an exact observed authority record")
	}
	renewal := registry
	if len(replicas) > 0 {
		renewal = replicas[0]
	}
	r := OutputAuthorityRecord{Token: "private-token", NodeID: 7, WorkID: "work", Generation: 1, Epoch: "epoch", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	id, err := registry.Register(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	old, err := registry.List(t.Context(), r.NodeID)
	if err != nil || len(old) != 1 {
		t.Fatal("missing original registration")
	}
	updatedID, err := renewal.Register(t.Context(), r)
	if err != nil || updatedID != id {
		t.Fatal("renewal changed opaque viewer identity")
	}
	current, err := registry.List(t.Context(), r.NodeID)
	if err != nil || len(current) != 1 || current[0] == old[0] {
		t.Fatal("renewal has no stale-deletion fence")
	}
	if err := deleter.Delete(t.Context(), old[0]); err != nil {
		t.Fatal(err)
	}
	kept, err := registry.List(t.Context(), r.NodeID)
	if err != nil || len(kept) != 1 || kept[0] != current[0] {
		t.Fatal("stale deletion removed a renewed viewer authority")
	}
	for range 8 {
		observed, err := registry.List(t.Context(), r.NodeID)
		if err != nil || len(observed) != 1 {
			t.Fatal("missing viewer before concurrent renewal")
		}
		start := make(chan struct{})
		errors := make(chan error, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		go func() { ready.Done(); <-start; _, err := renewal.Register(t.Context(), r); errors <- err }()
		go func() { ready.Done(); <-start; errors <- deleter.Delete(t.Context(), observed[0]) }()
		ready.Wait()
		close(start)
		for range 2 {
			if err := <-errors; err != nil {
				t.Fatal(err)
			}
		}
		after, err := registry.List(t.Context(), r.NodeID)
		if err != nil || len(after) != 1 {
			t.Fatal("concurrent cleanup lost renewed authority")
		}
	}
	current, err = registry.List(t.Context(), r.NodeID)
	if err != nil || len(current) != 1 {
		t.Fatal("missing current viewer")
	}
	if err := deleter.Delete(t.Context(), current[0]); err != nil {
		t.Fatal(err)
	}
	removed, err := registry.List(t.Context(), r.NodeID)
	if err != nil || len(removed) != 0 {
		t.Fatal("current deletion did not release authority quota")
	}
}

func testLegacyRedisOutputSweep(t *testing.T, client *redis.Client, prefix string) {
	t.Helper()
	registry := NewRedisOutputAuthorities(client, prefix)
	record := OutputAuthorityRecord{Token: "legacy-customer-token", NodeID: 7, WorkID: "legacy-work", Generation: 1, Epoch: "epoch", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	record.ID = outputAuthorityID(record)
	// This is the exact JSON written by the pre-revision implementation.
	raw := fmt.Sprintf(`{"id":%q,"token":%q,"work_id":%q,"epoch":%q,"node_id":7,"generation":1,"expires_at":%d}`, record.ID, record.Token, record.WorkID, record.Epoch, record.ExpiresAt)
	var valid OutputAuthorityRecord
	if err := json.Unmarshal([]byte(raw), &valid); err != nil {
		t.Fatal(err)
	}
	keys := registry.keys(record.NodeID)
	_, err := client.TxPipelined(t.Context(), func(pipe redis.Pipeliner) error {
		pipe.HSet(t.Context(), keys[0], record.ID, raw)
		pipe.ZAdd(t.Context(), keys[1], redis.Z{Score: float64(record.ExpiresAt), Member: record.ID})
		pipe.Set(t.Context(), keys[2], len(raw), 0)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: record.NodeID, URL: "https://worker.example.test", PublicKey: public, Enabled: true}
	service, err := New(Options{Origin: "https://core.example.test", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: NewMemoryStore(), OutputAuthorities: registry, VerifyToken: func(context.Context, string) (*streamtoken.Claims, error) { return nil, ErrDenied }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.currentOutputs(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	remaining, err := registry.List(t.Context(), node.ID)
	if err != nil || len(remaining) != 0 {
		t.Fatal("pre-revision stopped authority could not be reclaimed")
	}
}
