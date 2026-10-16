package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestOutputAuthoritiesDeduplicateBindScopeAndRetainLongDownload(t *testing.T) {
	registry := NewMemoryOutputAuthorities()
	r := OutputAuthorityRecord{Token: "core-only-customer-token", NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	ids := make(chan string, 32)
	errs := make(chan error, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() { id, err := registry.Register(t.Context(), r); ids <- id; errs <- err })
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first || len(id) != 64 || strings.Contains(id, r.Token) {
			t.Fatal("authority identifier unstable or leaks token")
		}
	}
	got, err := registry.List(t.Context(), 7)
	if err != nil || len(got) != 1 || got[0].Token != r.Token || got[0].ExpiresAt != r.ExpiresAt {
		t.Fatalf("deduplication lost private authority: %+v %v", got, err)
	}
	// Registering a fresh five-minute output request grant must retain a longer
	// customer authority horizon for an already-open healthy download.
	if got[0].ExpiresAt <= time.Now().Add(5*time.Minute).Unix() {
		t.Fatal("long download authority truncated to request grant window")
	}
	for _, change := range []func(*OutputAuthorityRecord){func(r *OutputAuthorityRecord) { r.NodeID++ }, func(r *OutputAuthorityRecord) { r.WorkID = "other" }, func(r *OutputAuthorityRecord) { r.Generation++ }, func(r *OutputAuthorityRecord) { r.Epoch = "new" }, func(r *OutputAuthorityRecord) { r.Token = "other-token" }} {
		other := r
		change(&other)
		id, err := registry.Register(t.Context(), other)
		if err != nil || id == first {
			t.Fatal("distinct scope reused authority identifier")
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := registry.Register(canceled, r); err == nil {
		t.Fatal("canceled registration accepted")
	}
}
func TestOutputAuthoritiesBoundRecordsAndEncodedBytes(t *testing.T) {
	registry := NewMemoryOutputAuthorities()
	r := OutputAuthorityRecord{Token: "token", NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	for i := range 4096 {
		r.Generation = uint64(i + 1)
		if _, err := registry.Register(t.Context(), r); err != nil {
			t.Fatalf("valid record %d refused: %v", i, err)
		}
	}
	r.Generation++
	if _, err := registry.Register(t.Context(), r); err == nil {
		t.Fatal("per-node record capacity exceeded")
	}
	r.NodeID = 8
	if _, err := registry.Register(t.Context(), r); err != nil {
		t.Fatal("another node inherited full node quota")
	}
	registry = NewMemoryOutputAuthorities()
	r.NodeID = 7
	r.Token = strings.Repeat("t", 64<<10)
	var admitted int
	for i := range 32 {
		r.Generation = uint64(i + 1)
		if _, err := registry.Register(t.Context(), r); err != nil {
			break
		}
		admitted++
	}
	if admitted == 0 || admitted >= 32 {
		t.Fatalf("encoded byte budget not enforced: %d", admitted)
	}
	records, err := registry.List(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	var size int
	for _, record := range records {
		b, _ := json.Marshal(record)
		size += len(b)
	}
	if size > 1<<20 {
		t.Fatalf("registry retained %d encoded bytes", size)
	}
	invalid := r
	invalid.Token += "x"
	if _, err := registry.Register(t.Context(), invalid); err == nil {
		t.Fatal("oversized JWT retained")
	}
	invalid = r
	invalid.ExpiresAt = time.Now().Add(-time.Second).Unix()
	if _, err := registry.Register(t.Context(), invalid); err == nil {
		t.Fatal("expired JWT retained")
	}
}

func TestOutputAuthorityLongDownloadExpiresAtCustomerHorizon(t *testing.T) {
	registry := NewMemoryOutputAuthorities()
	now := time.Now()
	registry.now = func() time.Time { return now }
	r := OutputAuthorityRecord{Token: "hour-token", NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: now.Add(time.Hour).Unix()}
	id, err := registry.Register(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	records, err := registry.List(t.Context(), 7)
	if err != nil || len(records) != 1 || records[0].ID != id {
		t.Fatal("healthy download lost authority at request grant deadline")
	}
	now = now.Add(time.Hour)
	records, err = registry.List(t.Context(), 7)
	if err != nil || len(records) != 0 {
		t.Fatal("expired customer authority was retained")
	}
	if _, err = registry.Register(t.Context(), r); err == nil {
		t.Fatal("expired authority resurrected")
	}
}
func TestRedisOutputAuthoritySurvivesCoreReplacementAndPrunesAtomically(t *testing.T) {
	raw := os.Getenv("SILO_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("SILO_TEST_REDIS_URL is not set")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	prefix := "silo:output-authority-test:" + uuid.NewString() + ":"
	first, second := NewRedisOutputAuthorities(client, prefix), NewRedisOutputAuthorities(client, prefix)
	t.Cleanup(func() {
		keys, err := client.Keys(context.Background(), prefix+"*").Result()
		if err == nil && len(keys) > 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
	})
	now := time.Now()
	first.now = func() time.Time { return now }
	second.now = func() time.Time { return now }
	record := OutputAuthorityRecord{Token: "private-customer-token", NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: now.Add(time.Hour).Unix()}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range 32 {
		wg.Go(func() {
			owner := first
			if i%2 != 0 {
				owner = second
			}
			_, err := owner.Register(t.Context(), record)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(6 * time.Minute)
	records, err := second.List(t.Context(), 7)
	if err != nil || len(records) != 1 {
		t.Fatal("replacement core lost healthy download authority")
	}
	now = now.Add(time.Hour)
	records, err = second.List(t.Context(), 7)
	if err != nil || len(records) != 0 {
		t.Fatal("expired shared customer authority was retained")
	}
	record.ExpiresAt = now.Add(time.Minute).Unix()
	record.Token = strings.Repeat("p", 64<<10)
	errs = make(chan error, 32)
	var admitted int
	var mu sync.Mutex
	for i := range 32 {
		wg.Go(func() {
			r := record
			r.Generation = uint64(i + 1)
			owner := first
			if i%2 != 0 {
				owner = second
			}
			if _, err := owner.Register(t.Context(), r); err == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if admitted == 0 || admitted >= 32 {
		t.Fatalf("shared encoded budget ineffective: %d", admitted)
	}
	records, err = second.List(t.Context(), 7)
	if err != nil || len(records) != admitted {
		t.Fatal("shared quota partially published a record")
	}
	var bytes int
	for _, r := range records {
		encoded, _ := json.Marshal(r)
		bytes += len(encoded)
	}
	if bytes > maxOutputAuthorityBytes {
		t.Fatal("atomic shared byte quota oversubscribed")
	}
	// A lost accounting key must not reset quota while private records survive.
	if err := client.Del(t.Context(), first.keys(7)[2]).Err(); err != nil {
		t.Fatal(err)
	}
	record.Generation = 100
	if _, err := first.Register(t.Context(), record); err == nil {
		t.Fatal("lost accounting key reset retained record byte quota")
	}
	if _, err := second.List(t.Context(), 7); err == nil {
		t.Fatal("unaccounted private record map was published")
	}

}
func TestPreparedOutputSnapshotRechecksEachViewerAndExposesOnlyOpaqueIDs(t *testing.T) {
	testOutputSnapshotRechecksEachViewer(t, true)
}

func TestOrdinaryOutputSnapshotRechecksEachViewerAndExposesOnlyOpaqueIDs(t *testing.T) {
	testOutputSnapshotRechecksEachViewer(t, false)
}

func testOutputSnapshotRechecksEachViewer(t *testing.T, prepared bool) {
	t.Helper()
	corePublic, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	nodePublic, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example.test", PublicKey: nodePublic, Enabled: true}
	store := NewMemoryStore()
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "artifact", Path: preparePath})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), 7, a.WorkID, a.Generation); err != nil {
		t.Fatal(err)
	}
	registry := NewMemoryOutputAuthorities()
	claims := streamtoken.Claims{OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: "origin-login"}, DownloadArtifactID: "artifact", TranscodeNode: node.URL, RoutingExecutionNodeID: 7, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	if !prepared {
		claims.SessionID = claims.DownloadArtifactID
		claims.DownloadArtifactID = ""
	}
	denied := false
	service, err := New(Options{Origin: "https://core.example.test", PrivateKey: coreKey, Nodes: testNodes{[]Node{node}}, Store: store, OutputAuthorities: registry, VerifyToken: func(_ context.Context, token string) (*streamtoken.Claims, error) {
		if denied && token == "private-viewer-a" {
			return nil, ErrDenied
		}
		snapshot := claims
		return &snapshot, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	aID, err := registry.Register(t.Context(), OutputAuthorityRecord{Token: "private-viewer-a", NodeID: 7, WorkID: "artifact", Generation: a.Generation, Epoch: "epoch", ExpiresAt: claims.ExpiresAt.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	bID, err := registry.Register(t.Context(), OutputAuthorityRecord{Token: "private-viewer-b", NodeID: 7, WorkID: "artifact", Generation: a.Generation, Epoch: "epoch", ExpiresAt: claims.ExpiresAt.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func(seq uint64) workerwire.Snapshot {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"boot_id": "boot", "nonce": "snapshot-nonce", "sequence": seq})
		req := httptest.NewRequest(http.MethodPost, "https://core.example.test/api/internal/workers/7/snapshot", strings.NewReader(string(body)))
		if err := workeridentity.Sign(req, "https://core.example.test", workeridentity.DomainNodeProof, nodeKey, 7, false); err != nil {
			t.Fatal(err)
		}
		out := httptest.NewRecorder()
		service.Handler().ServeHTTP(out, req)
		if out.Code != 200 {
			t.Fatalf("snapshot=%d %s", out.Code, out.Body.String())
		}
		var reply GrantReply
		if err := json.Unmarshal(out.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		grant, err := workeridentity.DecodeGrant(reply.Grant, workeridentity.DomainSnapshotGrant, node.URL, corePublic, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(grant.Body), "private-viewer") {
			t.Fatal("snapshot exposed customer token")
		}
		var result workerwire.Snapshot
		if err = json.Unmarshal(grant.Body, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	initial := snapshot(0)
	if !initial.Outputs[aID] || !initial.Outputs[bID] {
		t.Fatal("valid independent viewers were omitted")
	}
	for sequence, origin := range []string{"https://WORKER.Example.Test", "https://worker.example.test:443", "https://worker.example.test/"} {
		claims.TranscodeNode = origin
		if equivalent := snapshot(uint64(sequence + 1)); !equivalent.Outputs[aID] || !equivalent.Outputs[bID] {
			t.Fatalf("equivalent admitted origin %q lost viewer authority", origin)
		}
	}
	claims.TranscodeNode = node.URL
	denied = true
	updated := snapshot(4)
	if updated.Outputs[aID] || !updated.Outputs[bID] || updated.Active[a.WorkID] != a.Generation {
		t.Fatal("viewer revoke either retained stale authority or stopped shared artifact")
	}
}
