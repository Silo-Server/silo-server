package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type outputSnapshotStore struct {
	*MemoryStore
	afterSnapshot func()
	snapshotErr   error
	leaseErr      error
	getCalls      int
}

func (s *outputSnapshotStore) HasLease(ctx context.Context, node int, work string, generation uint64) (bool, error) {
	if s.leaseErr != nil {
		return false, s.leaseErr
	}
	return s.MemoryStore.HasLease(ctx, node, work, generation)
}

func (s *outputSnapshotStore) Snapshot(ctx context.Context, node int) (AuthoritySnapshot, error) {
	if s.snapshotErr != nil {
		return AuthoritySnapshot{}, s.snapshotErr
	}
	state, err := s.MemoryStore.Snapshot(ctx, node)
	if s.afterSnapshot != nil {
		s.afterSnapshot()
	}
	return state, err
}
func (s *outputSnapshotStore) Get(ctx context.Context, node int, work string) (workerwire.Assignment, bool, error) {
	s.getCalls++
	return s.MemoryStore.Get(ctx, node, work)
}

type outputSnapshotFixture struct {
	service                   *Service
	store                     *outputSnapshotStore
	registry                  *MemoryOutputAuthorities
	node                      Node
	nodePrivate               ed25519.PrivateKey
	corePublic                ed25519.PublicKey
	claims                    map[string]*streamtoken.Claims
	failures                  map[string]error
	verifyErr, errorExecution error
	denyExecution             bool
	verifyHook                func()
}

func newOutputSnapshotFixture(t *testing.T) *outputSnapshotFixture {
	t.Helper()
	f := &outputSnapshotFixture{store: &outputSnapshotStore{MemoryStore: NewMemoryStore()}, registry: NewMemoryOutputAuthorities(), claims: map[string]*streamtoken.Claims{}, failures: map[string]error{}}
	var corePrivate ed25519.PrivateKey
	f.corePublic, corePrivate, _ = ed25519.GenerateKey(rand.Reader)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	f.nodePrivate = private
	f.node = Node{ID: 7, URL: "https://worker.example.test", PublicKey: public, Enabled: true}
	var err error
	f.service, err = New(Options{Origin: "https://core.example.test", PrivateKey: corePrivate, Nodes: testNodes{[]Node{f.node}}, Store: f.store, OutputAuthorities: f.registry, VerifyTokens: func(_ context.Context, tokens []string) ([]TokenVerification, error) {
		if f.verifyHook != nil {
			f.verifyHook()
		}
		if f.verifyErr != nil {
			return nil, f.verifyErr
		}
		out := make([]TokenVerification, len(tokens))
		for i, token := range tokens {
			out[i] = TokenVerification{Claims: f.claims[token], Err: f.failures[token]}
		}
		return out, nil
	}, ExecutionStatus: func(context.Context, Node, workerwire.Assignment) (bool, error) {
		return !f.denyExecution, f.errorExecution
	}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestOutputSnapshotCleanupPreservesConcurrentExactRegistration(t *testing.T) {
	f := newOutputSnapshotFixture(t)
	a := f.assignment(t, "work", true)
	id := f.record(t, a, "viewer")
	old, err := f.registry.List(t.Context(), 7)
	if err != nil || len(old) != 1 {
		t.Fatal("missing original record")
	}
	f.failures["viewer"] = ErrOutputRevoked
	f.verifyHook = func() {
		f.verifyHook = nil
		if _, err := f.registry.Register(t.Context(), old[0]); err != nil {
			t.Fatal(err)
		}
	}
	if f.snapshot(t, 200).Outputs[id] {
		t.Fatal("revoked observation retained viewer permission")
	}
	current, err := f.registry.List(t.Context(), 7)
	if err != nil || len(current) != 1 || current[0].ID != id || current[0].Revision == old[0].Revision {
		t.Fatal("stale snapshot cleanup deleted concurrent registration")
	}
	delete(f.failures, "viewer")
	if !f.snapshot(t, 200).Outputs[id] {
		t.Fatal("retained concurrent registration could not recover")
	}
}

func TestOutputSnapshotPendingLeaseUnknownDoesNotRenew(t *testing.T) {
	f := newOutputSnapshotFixture(t)
	a := f.assignment(t, "pending-work", false)
	id := f.record(t, a, "pending-viewer")
	f.store.leaseErr = errors.New("shared lease unavailable")
	f.snapshot(t, 503)
	if records, err := f.registry.List(t.Context(), 7); err != nil || len(records) != 1 {
		t.Fatal("unknown pending lease deleted viewer authority")
	}
	f.store.leaseErr = nil
	if !f.snapshot(t, 200).Outputs[id] {
		t.Fatal("healthy pending lease did not recover")
	}
}

type outputSnapshotRedisCounter struct {
	*RedisStore
	gets, active, candidates, snapshots int
}

func (s *outputSnapshotRedisCounter) Get(ctx context.Context, node int, work string) (workerwire.Assignment, bool, error) {
	s.gets++
	return s.RedisStore.Get(ctx, node, work)
}
func (s *outputSnapshotRedisCounter) IsActive(ctx context.Context, node int, work string, generation uint64) (bool, error) {
	s.active++
	return s.RedisStore.IsActive(ctx, node, work, generation)
}
func (s *outputSnapshotRedisCounter) Candidate(ctx context.Context, node int, work string) (workerwire.Assignment, bool, error) {
	s.candidates++
	return s.RedisStore.Candidate(ctx, node, work)
}
func (s *outputSnapshotRedisCounter) Snapshot(ctx context.Context, node int) (AuthoritySnapshot, error) {
	s.snapshots++
	return s.RedisStore.Snapshot(ctx, node)
}

func TestRedisOutputSnapshotUsesOneAssignmentReadFor128Viewers(t *testing.T) {
	f := newOutputSnapshotFixture(t)
	base, _ := bindingRedis(t)
	store := &outputSnapshotRedisCounter{RedisStore: base}
	f.service.options.Store = store
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "snapshot-viewers", Path: "/transcode/start"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), 7, a.WorkID, a.Generation); err != nil {
		t.Fatal(err)
	}
	for i := range 128 {
		f.record(t, a, fmt.Sprintf("viewer-%d", i))
	}
	snapshot := f.snapshot(t, 200)
	if len(snapshot.Outputs) != 128 || store.snapshots != 1 || store.gets != 0 || store.active != 0 || store.candidates != 0 {
		t.Fatalf("Redis snapshot repeated viewer reads: outputs=%d Snapshot=%d Get=%d IsActive=%d Candidate=%d", len(snapshot.Outputs), store.snapshots, store.gets, store.active, store.candidates)
	}
}

func (f *outputSnapshotFixture) assignment(t *testing.T, work string, activate bool) workerwire.Assignment {
	t.Helper()
	a, err := f.store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: work, Path: "/transcode/start"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if activate {
		if err = f.store.Activate(t.Context(), 7, work, a.Generation); err != nil {
			t.Fatal(err)
		}
	} else {
		if err = f.store.TouchLease(t.Context(), 7, work, a.Generation, 2*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	return a
}
func (f *outputSnapshotFixture) record(t *testing.T, a workerwire.Assignment, token string) string {
	t.Helper()
	claims := &streamtoken.Claims{OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: "login"}, SessionID: a.WorkID, TranscodeNode: f.node.URL, RoutingExecutionNodeID: 7, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	f.claims[token] = claims
	id, err := f.registry.Register(t.Context(), OutputAuthorityRecord{Token: token, NodeID: 7, WorkID: a.WorkID, Generation: a.Generation, Epoch: "epoch", ExpiresAt: claims.ExpiresAt.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func (f *outputSnapshotFixture) snapshot(t *testing.T, wantStatus int) workerwire.Snapshot {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "https://core.example.test/api/internal/workers/7/snapshot", strings.NewReader(`{"boot_id":"boot","nonce":"batch","sequence":0}`))
	if err := workeridentity.Sign(request, "https://core.example.test", workeridentity.DomainNodeProof, f.nodePrivate, 7, false); err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	f.service.Handler().ServeHTTP(out, request)
	if out.Code != wantStatus {
		t.Fatalf("snapshot=%d want=%d %s", out.Code, wantStatus, out.Body.String())
	}
	if wantStatus != 200 {
		if strings.Contains(out.Body.String(), `"grant"`) {
			t.Fatal("unknown check renewed signed authority")
		}
		return workerwire.Snapshot{}
	}
	var reply GrantReply
	if err := json.Unmarshal(out.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	grant, err := workeridentity.DecodeGrant(reply.Grant, workeridentity.DomainSnapshotGrant, f.node.URL, f.corePublic, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot workerwire.Snapshot
	if err = json.Unmarshal(grant.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ValidForMillis != 2000 || grant.ExpiresAt > time.Now().Add(2*time.Second).Unix() {
		t.Fatal("snapshot lease changed")
	}
	return snapshot
}

func TestOutputSnapshotListsRecordsBeforeAssignmentSnapshot(t *testing.T) {
	f := newOutputSnapshotFixture(t)
	var id string
	f.store.afterSnapshot = func() {
		f.store.afterSnapshot = nil
		a := f.assignment(t, "concurrent-work", true)
		id = f.record(t, a, "concurrently-registered")
	}
	f.snapshot(t, 200)
	records, err := f.registry.List(t.Context(), 7)
	if err != nil || len(records) != 1 || records[0].ID != id {
		t.Fatal("assignment snapshot captured before records destructively retired a concurrent registration")
	}
	if !f.snapshot(t, 200).Outputs[id] {
		t.Fatal("subsequent fresh snapshot failed to publish retained concurrent viewer")
	}
	if f.store.getCalls != 0 {
		t.Fatal("snapshot fell back to per-viewer Store.Get")
	}
}

func TestOutputSnapshotPreservesActiveFirstAndReadyPendingFallback(t *testing.T) {
	f := newOutputSnapshotFixture(t)
	old := f.assignment(t, "work", true)
	oldID := f.record(t, old, "old-viewer")
	candidate := f.assignment(t, "work", false)
	candidateID := f.record(t, candidate, "candidate-viewer")
	snapshot := f.snapshot(t, 200)
	if snapshot.Active[old.WorkID] != old.Generation || snapshot.Pending[old.WorkID] != candidate.Generation || !snapshot.Outputs[oldID] || snapshot.Outputs[candidateID] {
		t.Fatal("snapshot changed active-first viewer output semantics")
	}
	if records, err := f.registry.List(t.Context(), 7); err != nil || len(records) != 1 || records[0].ID != oldID {
		t.Fatal("superseded viewer was not reclaimed exactly")
	}
	if err := f.store.Stop(t.Context(), 7, "work"); err != nil {
		t.Fatal(err)
	}
	pending := f.assignment(t, "pending-only", false)
	id := f.record(t, pending, "pending-only-viewer")
	if !f.snapshot(t, 200).Outputs[id] {
		t.Fatal("ready-owned pending assignment no longer falls back without a live assignment")
	}
}

func TestOutputSnapshotUnknownChecksRetainRecordsAndDoNotRenewLease(t *testing.T) {
	f := newOutputSnapshotFixture(t)
	a := f.assignment(t, "work", true)
	id := f.record(t, a, "viewer")
	for _, failure := range []string{"assignment", "execution", "verification", "single-result"} {
		err := errors.New("dependency unavailable")
		switch failure {
		case "assignment":
			f.store.snapshotErr = err
		case "execution":
			f.errorExecution = err
		case "verification":
			f.verifyErr = err
		case "single-result":
			f.failures["viewer"] = err
		}
		f.snapshot(t, 503)
		if records, err := f.registry.List(t.Context(), 7); err != nil || len(records) != 1 {
			t.Fatalf("unknown %s check deleted recoverable viewer authority", failure)
		}
		f.store.snapshotErr, f.errorExecution, f.verifyErr = nil, nil, nil
		delete(f.failures, "viewer")
		if !f.snapshot(t, 200).Outputs[id] {
			t.Fatalf("unknown %s check prevented automatic recovery", failure)
		}
	}
	f.failures["viewer"] = ErrDenied
	if f.snapshot(t, 200).Outputs[id] {
		t.Fatal("definite content denial retained permission")
	}
	delete(f.failures, "viewer")
	if !f.snapshot(t, 200).Outputs[id] {
		t.Fatal("content policy restoration lost retained viewer")
	}
	f.failures["viewer"] = ErrOutputRevoked
	if f.snapshot(t, 200).Outputs[id] {
		t.Fatal("definite source revocation retained permission")
	}
	if records, err := f.registry.List(t.Context(), 7); err != nil || len(records) != 0 {
		t.Fatal("definite source revoke was not reclaimed")
	}
}
