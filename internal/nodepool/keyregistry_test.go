package nodepool

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

// newKeyedTranscodePool is NewTranscodePool with every node counting as
// keyed, for fixtures that are not about keys.
func newKeyedTranscodePool() *TranscodePool {
	pool := NewTranscodePool()
	pool.SetKeyCheck(func(int) bool { return true })
	return pool
}

// fakeCredentialRows is a loader the test can rewrite between reloads.
type fakeCredentialRows struct {
	mu    sync.Mutex
	rows  []nodeCredentialRow
	err   error
	loads atomic.Int32
}

func (f *fakeCredentialRows) set(rows ...nodeCredentialRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = rows
}

func (f *fakeCredentialRows) load(context.Context) ([]nodeCredentialRow, error) {
	f.loads.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nodeCredentialRow(nil), f.rows...), f.err
}

func keyedRow(t *testing.T, id int, nodeURL string) (nodeCredentialRow, string) {
	t.Helper()
	key, err := GenerateNodeKey(id)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := EncryptNodeKey(testDataCipher(t), id, key)
	if err != nil {
		t.Fatal(err)
	}
	return nodeCredentialRow{ID: id, Type: NodeTypeTranscode, URL: nodeURL, Ciphertext: &ciphertext}, key
}

// manualClock lets a test move the registry's clock without sleeping.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestKeyRegistry(t *testing.T, rows *fakeCredentialRows) (*KeyRegistry, *manualClock) {
	t.Helper()
	registry := newKeyRegistry(rows.load, testDataCipher(t))
	clock := &manualClock{now: time.Unix(1_760_000_000, 0)}
	registry.now = clock.Now
	if err := registry.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	return registry, clock
}

func TestKeyringSignsEachNodeTypeWithItsOwnKey(t *testing.T) {
	transcode, key := keyedRow(t, 1, "http://gpu-1:8080/")
	rows := &fakeCredentialRows{}
	rows.set(
		transcode,
		nodeCredentialRow{ID: 2, Type: NodeTypeTranscode, URL: "http://gpu-2:8080"},
		nodeCredentialRow{ID: 3, Type: NodeTypeProxy, URL: "http://proxy-1:8081"},
	)
	registry, clock := newTestKeyRegistry(t, rows)
	shared := "shared-jwt-secret"
	keys := registry.Keyring(func() string { return shared })

	// The registry canonicalizes trailing slashes the way the pools do.
	for _, nodeURL := range []string{"http://gpu-1:8080", "http://gpu-1:8080/", " http://gpu-1:8080 "} {
		if got, err := keys.KeyFor(nodeauth.TargetTranscode, nodeURL); err != nil || got != key {
			t.Fatalf("KeyFor(%q) = %q, %v; want node 1's key", nodeURL, got, err)
		}
	}
	if got, err := keys.KeyFor(nodeauth.TargetProxy, "http://proxy-1:8081/"); err != nil || got != shared {
		t.Fatalf("proxy KeyFor = %q, %v; want the shared secret", got, err)
	}
	// The shared secret is read per call, so a rotation applies at once.
	shared = "rotated-jwt-secret"
	if got, _ := keys.KeyFor(nodeauth.TargetProxy, "http://proxy-1:8081"); got != shared {
		t.Fatalf("proxy KeyFor after rotation = %q", got)
	}
	clock.Advance(time.Hour)
	if got, err := keys.KeyFor(nodeauth.TargetTranscode, "http://gpu-2:8080"); !errors.Is(err, nodeauth.ErrNoNodeKey) || got != "" {
		t.Fatalf("keyless KeyFor = %q, %v; want ErrNoNodeKey and never the shared secret", got, err)
	}
	clock.Advance(time.Hour)
	if got, err := keys.KeyFor(nodeauth.TargetTranscode, "http://stranger:8080"); !errors.Is(err, nodeauth.ErrUnknownNode) || got != "" {
		t.Fatalf("unknown KeyFor = %q, %v; want ErrUnknownNode", got, err)
	}

	if got, err := registry.NodeKey(1); err != nil || got != key {
		t.Fatalf("NodeKey(1) = %q, %v", got, err)
	}
	if _, err := registry.NodeKey(2); !errors.Is(err, nodeauth.ErrNoNodeKey) {
		t.Fatalf("NodeKey(keyless) = %v, want ErrNoNodeKey", err)
	}
	if _, err := registry.NodeKey(3); !errors.Is(err, ErrNodeKeyNotIssuable) {
		t.Fatalf("NodeKey(proxy) = %v, want ErrNodeKeyNotIssuable", err)
	}
	if _, err := registry.NodeKey(99); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("NodeKey(unknown) = %v, want ErrNodeNotFound", err)
	}
}

// Two rows that canonicalize to one URL must not let a proxy row hand the
// shared secret to the transcode node behind the other.
func TestKeyringPrefersTheTranscodeNodeOnACanonicalURLTie(t *testing.T) {
	transcode, key := keyedRow(t, 5, "http://shared-host/")
	for _, order := range [][]nodeCredentialRow{
		{transcode, {ID: 6, Type: NodeTypeProxy, URL: "http://shared-host"}},
		{{ID: 6, Type: NodeTypeProxy, URL: "http://shared-host"}, transcode},
	} {
		rows := &fakeCredentialRows{}
		rows.set(order...)
		registry, _ := newTestKeyRegistry(t, rows)
		if got, err := registry.Keyring(func() string { return "shared" }).KeyFor(nodeauth.TargetTranscode, "http://shared-host"); err != nil || got != key {
			t.Fatalf("KeyFor = %q, %v; want the transcode node's key", got, err)
		}
	}
}

func TestKeyRegistryTracksRotationAndDeletion(t *testing.T) {
	first, firstKey := keyedRow(t, 1, "http://gpu-1")
	rows := &fakeCredentialRows{}
	rows.set(first)
	registry, _ := newTestKeyRegistry(t, rows)
	keys := registry.Keyring(nil)
	if got, _ := keys.KeyFor(nodeauth.TargetTranscode, "http://gpu-1"); got != firstKey {
		t.Fatal("first key not loaded")
	}

	rotated, rotatedKey := keyedRow(t, 1, "http://gpu-1")
	rows.set(rotated)
	if err := registry.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := keys.KeyFor(nodeauth.TargetTranscode, "http://gpu-1"); got != rotatedKey {
		t.Fatal("rotated key not adopted on refresh")
	}
	if got, _ := registry.NodeKey(1); got != rotatedKey {
		t.Fatal("NodeKey kept the old key after rotation")
	}

	rows.set()
	if err := registry.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.KeyFor(nodeauth.TargetTranscode, "http://gpu-1"); !errors.Is(err, nodeauth.ErrUnknownNode) {
		t.Fatalf("deleted node KeyFor = %v, want ErrUnknownNode", err)
	}
	if _, err := registry.NodeKey(1); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("deleted node NodeKey = %v, want ErrNodeNotFound", err)
	}

	// A failed reload keeps what the registry already had.
	rows.set(rotated)
	if err := registry.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows.err = errors.New("database unavailable")
	if err := registry.Refresh(t.Context()); err == nil {
		t.Fatal("a failed reload reported success")
	}
	if got, _ := registry.NodeKey(1); got != rotatedKey {
		t.Fatal("a failed reload dropped the loaded credentials")
	}
}

// A stored key that does not decrypt (wrong SECRET_KEY, a ciphertext copied
// from another row) signs nothing rather than falling back.
func TestKeyRegistryTreatsAnUndecryptableKeyAsMissing(t *testing.T) {
	row, _ := keyedRow(t, 1, "http://gpu-1")
	row.ID = 2 // node 1's ciphertext on node 2's row
	rows := &fakeCredentialRows{}
	rows.set(row)
	registry, _ := newTestKeyRegistry(t, rows)
	if _, err := registry.Keyring(func() string { return "shared" }).KeyFor(nodeauth.TargetTranscode, "http://gpu-1"); !errors.Is(err, nodeauth.ErrNoNodeKey) {
		t.Fatalf("KeyFor = %v, want ErrNoNodeKey", err)
	}
	if _, err := registry.NodeKey(2); !errors.Is(err, nodeauth.ErrNoNodeKey) {
		t.Fatalf("NodeKey = %v, want ErrNoNodeKey", err)
	}
}

func TestKeyRegistryReloadsOnAMissAtMostOncePerSecond(t *testing.T) {
	rows := &fakeCredentialRows{}
	registry, clock := newTestKeyRegistry(t, rows)
	keys := registry.Keyring(nil)
	loadsAfterStart := rows.loads.Load()

	// Within a second of the last load, misses do not query.
	for range 5 {
		if _, err := keys.KeyFor(nodeauth.TargetTranscode, "http://new-node"); !errors.Is(err, nodeauth.ErrUnknownNode) {
			t.Fatalf("KeyFor = %v", err)
		}
		if _, err := registry.NodeKey(4); !errors.Is(err, ErrNodeNotFound) {
			t.Fatalf("NodeKey = %v", err)
		}
	}
	if got := rows.loads.Load(); got != loadsAfterStart {
		t.Fatalf("misses inside the interval loaded %d times", got-loadsAfterStart)
	}

	// A node created since: the next miss after the interval finds it.
	created, key := keyedRow(t, 4, "http://new-node")
	rows.set(created)
	clock.Advance(keyRegistryMissInterval)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if got, err := keys.KeyFor(nodeauth.TargetTranscode, "http://new-node"); err != nil || got != key {
				t.Errorf("KeyFor after the interval = %q, %v", got, err)
			}
		})
	}
	wg.Wait()
	if got := rows.loads.Load() - loadsAfterStart; got != 1 {
		t.Fatalf("concurrent misses loaded %d times, want 1", got)
	}

	// A hit never reloads.
	clock.Advance(time.Hour)
	if _, err := registry.NodeKey(4); err != nil {
		t.Fatal(err)
	}
	if got := rows.loads.Load() - loadsAfterStart; got != 1 {
		t.Fatalf("a hit reloaded (%d loads)", got)
	}

	// A keyless node is a miss too: its key may have just been issued.
	rows.set(nodeCredentialRow{ID: 5, Type: NodeTypeTranscode, URL: "http://keyless"})
	if err := registry.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	issued, issuedKey := keyedRow(t, 5, "http://keyless")
	rows.set(issued)
	clock.Advance(keyRegistryMissInterval)
	if got, err := registry.NodeKey(5); err != nil || got != issuedKey {
		t.Fatalf("NodeKey after issue = %q, %v; want the new key on the miss reload", got, err)
	}
}

func TestKeyRegistryInvalidateReloadsInTheBackground(t *testing.T) {
	rows := &fakeCredentialRows{}
	registry, _ := newTestKeyRegistry(t, rows)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	registry.Start(ctx)

	row, key := keyedRow(t, 8, "http://gpu-8")
	rows.set(row)
	registry.Invalidate()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if cred := registry.lookupID(8); cred != nil && cred.key == key {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Invalidate did not reload the registry")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// gatedLoader reads the rows and then holds the load until the test releases
// it, so a test can keep a reload in flight that has already read. started
// receives once per load.
type gatedLoader struct {
	rows    *fakeCredentialRows
	started chan struct{}
	release chan struct{}
}

func newGatedLoader(rows *fakeCredentialRows) *gatedLoader {
	return &gatedLoader{rows: rows, started: make(chan struct{}, 64), release: make(chan struct{}, 64)}
}

func (g *gatedLoader) load(ctx context.Context) ([]nodeCredentialRow, error) {
	rows, err := g.rows.load(ctx)
	g.started <- struct{}{}
	<-g.release
	return rows, err
}

// One address written several ways is one node. The download preparer signs
// against a re-encoded URL ("/%77orker" becomes "/worker"), so a lookup that
// compared spellings would miss the node, or find a proxy registered under the
// other spelling and hand the transcode node the shared secret.
func TestKeyringMatchesEquivalentURLSpellings(t *testing.T) {
	transcode, key := keyedRow(t, 1, "http://GPU-1:80/%77orker/")
	rows := &fakeCredentialRows{}
	rows.set(transcode, nodeCredentialRow{ID: 2, Type: NodeTypeProxy, URL: "http://gpu-1/worker"})
	registry, _ := newTestKeyRegistry(t, rows)
	keys := registry.Keyring(func() string { return "shared" })
	for _, nodeURL := range []string{
		"http://gpu-1/worker", "http://gpu-1:80/worker/", "HTTP://Gpu-1/%77orker", " http://gpu-1/%77orker ",
	} {
		if got, err := keys.KeyFor(nodeauth.TargetTranscode, nodeURL); err != nil || got != key {
			t.Errorf("KeyFor(transcode, %q) = %q, %v; want node 1's key", nodeURL, got, err)
		}
		if got, err := keys.KeyFor(nodeauth.TargetProxy, nodeURL); !errors.Is(err, nodeauth.ErrUnknownNode) || got != "" {
			t.Errorf("KeyFor(proxy, %q) = %q, %v; a URL a transcode node shares is never a proxy's", nodeURL, got, err)
		}
	}
	for _, other := range []string{"https://gpu-1/worker", "http://gpu-1:8080/worker", "http://gpu-1/worker2", "http://gpu-1"} {
		if _, err := keys.KeyFor(nodeauth.TargetTranscode, other); !errors.Is(err, nodeauth.ErrUnknownNode) {
			t.Errorf("KeyFor(transcode, %q) = %v; a different address must not match", other, err)
		}
	}
	for in, want := range map[string]string{
		"https://H:443/a/":     "https://h/a",
		"http://[::1]:80/x":    "http://[::1]/x",
		"http://[::1]:8080/x/": "http://[::1]:8080/x",
		"http://h:8443":        "http://h:8443",
		"gpu-1/":               "gpu-1",
	} {
		if got := CanonicalNodeURL(in); got != want {
			t.Errorf("CanonicalNodeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// After a proxy's URL is given to a transcode node, a registry still holding
// the proxy must not answer a transcode lookup with the shared secret: the
// cached proxy entry is a miss, which reloads, and inside the miss cooldown it
// is refused.
func TestKeyringNeverSignsATranscodeTargetWithAStaleProxyEntry(t *testing.T) {
	rows := &fakeCredentialRows{}
	rows.set(nodeCredentialRow{ID: 1, Type: NodeTypeProxy, URL: "http://reused:8080"})
	registry, clock := newTestKeyRegistry(t, rows)
	keys := registry.Keyring(func() string { return "shared" })
	if got, err := keys.KeyFor(nodeauth.TargetProxy, "http://reused:8080"); err != nil || got != "shared" {
		t.Fatalf("proxy KeyFor = %q, %v", got, err)
	}

	transcode, key := keyedRow(t, 2, "http://reused:8080")
	rows.set(transcode) // proxy deleted, transcode node registered at its URL

	// Inside the cooldown the registry cannot reload: refused, never the
	// shared secret.
	if got, err := keys.KeyFor(nodeauth.TargetTranscode, "http://reused:8080"); !errors.Is(err, nodeauth.ErrUnknownNode) || got != "" {
		t.Fatalf("transcode KeyFor with a cached proxy entry = %q, %v; want ErrUnknownNode", got, err)
	}
	clock.Advance(keyRegistryMissInterval)
	if got, err := keys.KeyFor(nodeauth.TargetTranscode, "http://reused:8080"); err != nil || got != key {
		t.Fatalf("transcode KeyFor after the reload = %q, %v; want the node's own key", got, err)
	}
	if got, err := keys.KeyFor(nodeauth.TargetProxy, "http://reused:8080"); !errors.Is(err, nodeauth.ErrUnknownNode) || got != "" {
		t.Fatalf("proxy KeyFor at a transcode node's URL = %q, %v", got, err)
	}
}

// Concurrent misses share one reload, and the cooldown runs from when that
// reload finishes: a load slower than the cooldown must not let each waiting
// miss start another.
func TestKeyRegistryCoalescesMissReloadsAndCoolsDownAfterThem(t *testing.T) {
	rows := &fakeCredentialRows{}
	registry, clock := newTestKeyRegistry(t, rows)
	gate := newGatedLoader(rows)
	registry.load = gate.load
	keys := registry.Keyring(nil)
	created, key := keyedRow(t, 4, "http://new-node")
	rows.set(created)
	clock.Advance(keyRegistryMissInterval)
	before := rows.loads.Load()

	var wg sync.WaitGroup
	wg.Go(func() {
		if got, err := keys.KeyFor(nodeauth.TargetTranscode, "http://new-node"); err != nil || got != key {
			t.Errorf("first miss = %q, %v", got, err)
		}
	})
	<-gate.started
	// These look up a node that stays unknown, so each is a miss whether it
	// arrives during the reload or after it.
	for range 7 {
		wg.Go(func() {
			if _, err := keys.KeyFor(nodeauth.TargetTranscode, "http://unknown-node"); !errors.Is(err, nodeauth.ErrUnknownNode) {
				t.Errorf("concurrent miss = %v", err)
			}
		})
	}
	// The load takes longer than the cooldown.
	clock.Advance(3 * keyRegistryMissInterval / 2)
	// One release per miss, so a registry that let the waiting misses reload
	// one after another fails the count below instead of hanging.
	for range 8 {
		gate.release <- struct{}{}
	}
	wg.Wait()
	if got := rows.loads.Load() - before; got != 1 {
		t.Fatalf("8 concurrent misses loaded %d times, want 1", got)
	}

	// Inside the cooldown a miss neither loads nor waits.
	registry.load = func(context.Context) ([]nodeCredentialRow, error) {
		t.Error("a miss inside the cooldown reloaded")
		return nil, errors.New("unexpected load")
	}
	if _, err := keys.KeyFor(nodeauth.TargetTranscode, "http://another-node"); !errors.Is(err, nodeauth.ErrUnknownNode) {
		t.Fatalf("miss inside the cooldown = %v", err)
	}
	if _, err := registry.NodeKey(99); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("NodeKey miss inside the cooldown = %v", err)
	}
}

// A Refresh that arrives while a miss reload is running must still see rows
// written after that reload read them: it waits the running one out and runs
// one more, rather than queueing behind every request-path reload.
func TestKeyRegistryRefreshSeesRowsWrittenDuringARunningReload(t *testing.T) {
	rows := &fakeCredentialRows{}
	registry, clock := newTestKeyRegistry(t, rows)
	gate := newGatedLoader(rows)
	registry.load = gate.load
	clock.Advance(keyRegistryMissInterval)
	before := rows.loads.Load()

	var wg sync.WaitGroup
	wg.Go(func() { _, _ = registry.NodeKey(5) })
	// The miss reload has read the rows, without node 5, and not finished.
	<-gate.started
	row, key := keyedRow(t, 5, "http://gpu-5")
	rows.set(row)
	refreshed := make(chan error, 1)
	go func() { refreshed <- registry.Refresh(t.Context()) }()
	gate.release <- struct{}{}
	// Refresh's own reload, after the miss reload finished.
	<-gate.started
	gate.release <- struct{}{}
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if got, err := registry.NodeKey(5); err != nil || got != key {
		t.Fatalf("NodeKey after Refresh = %q, %v; want the row written during the earlier reload", got, err)
	}
	if got := rows.loads.Load() - before; got != 2 {
		t.Fatalf("loads = %d, want the running reload plus one", got)
	}
}

// A node signing with a key issued since this server last loaded is
// accepted after one reload; inside the cooldown a bad signature reloads
// nothing.
func TestKeyRegistryVerifiesARotatedNodeKeyAfterOneReload(t *testing.T) {
	row, oldKey := keyedRow(t, 3, "http://gpu-3")
	rows := &fakeCredentialRows{}
	rows.set(row)
	registry, clock := newTestKeyRegistry(t, rows)
	rotated, newKey := keyedRow(t, 3, "http://gpu-3")
	rows.set(rotated)
	clock.Advance(keyRegistryMissInterval)
	before := rows.loads.Load()

	signed := func(key string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/internal/nodes/self/report", strings.NewReader(`{"seq":1}`))
		nodeauthtest.SignFromNode(t, r, 3, key)
		return r
	}
	if id, err := nodeauth.VerifyFromNode(signed(newKey), registry); err != nil || id != 3 {
		t.Fatalf("rotated key = %d, %v; want node 3", id, err)
	}
	if got := rows.loads.Load() - before; got != 1 {
		t.Fatalf("loads = %d, want 1", got)
	}
	for range 5 {
		if _, err := nodeauth.VerifyFromNode(signed(oldKey), registry); !errors.Is(err, nodeauth.ErrUnauthorized) {
			t.Fatalf("old key after rotation = %v, want ErrUnauthorized", err)
		}
	}
	if got := rows.loads.Load() - before; got != 1 {
		t.Fatalf("bad signatures inside the cooldown loaded %d more times", got-1)
	}
}

// A transcode node without a key takes no new work on any placement path,
// even though as the node that never has a job it would otherwise win
// least-loaded selection; a key issued since makes it eligible once the
// registry reloads, without a pool reload.
func TestKeylessTranscodeNodesTakeNoNewWork(t *testing.T) {
	keyed, _ := keyedRow(t, 1, "http://gpu-1")
	rows := &fakeCredentialRows{}
	rows.set(keyed, nodeCredentialRow{ID: 2, Type: NodeTypeTranscode, URL: "http://gpu-2"})
	registry, _ := newTestKeyRegistry(t, rows)
	pool := NewTranscodePool()
	pool.SetKeyCheck(registry.HasKey)
	pool.SetNodes([]*Node{
		{ID: 1, Type: NodeTypeTranscode, URL: "http://gpu-1", Enabled: true, Healthy: true, ActiveJobs: 5},
		{ID: 2, Type: NodeTypeTranscode, URL: "http://gpu-2", Enabled: true, Healthy: true},
	})
	planner := NewPlanner(NewProxyPool(), pool)

	if plan := planner.PlanTranscodeSessionWithLocalEgress("s1", "", nil); plan.TranscodeNode == nil || plan.TranscodeNode.ID != 1 {
		t.Fatalf("local-egress plan = %+v, want node 1", plan.TranscodeNode)
	}
	if plan := planner.PlanSession("s2", "", true, 0); plan.TranscodeNode == nil || plan.TranscodeNode.ID != 1 {
		t.Fatalf("session plan = %+v, want node 1", plan.TranscodeNode)
	}
	if node, release := planner.ReserveTranscodeWork("w1"); node == nil || node.ID != 1 {
		t.Fatalf("ReserveTranscodeWork = %+v, want node 1", node)
	} else {
		release()
	}
	if planner.TranscodeWorkAvailableWith(func(n *Node) bool { return n.ID == 2 }) {
		t.Fatal("TranscodeWorkAvailableWith offered the keyless node")
	}
	if urls := planner.TranscodeNodeURLs(); len(urls) != 1 || urls[0] != "http://gpu-1" {
		t.Fatalf("TranscodeNodeURLs = %v, want only the keyed node", urls)
	}
	if node, release, outcome := (&Reservations{}).Reserve(pool.KeyedNodes(), 4); outcome != Reserved || node.ID != 1 {
		t.Fatalf("Reserve over KeyedNodes = %+v, %v; want node 1", node, outcome)
	} else {
		release()
	}
	if node := pool.Acquire(); node == nil || node.ID != 1 {
		t.Fatalf("Acquire = %+v, want node 1", node)
	}

	issued, _ := keyedRow(t, 2, "http://gpu-2")
	rows.set(keyed, issued)
	if err := registry.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	planner.ReleaseSession("s1")
	planner.ReleaseSession("s2")
	if node, release := planner.ReserveTranscodeWork("w2"); node == nil || node.ID != 2 {
		t.Fatalf("ReserveTranscodeWork after the key was issued = %+v, want the idle node 2", node)
	} else {
		release()
	}

	// Without a registry, the pooled KeyIssued decides.
	plain := NewTranscodePool()
	plain.SetNodes([]*Node{{ID: 3, Type: NodeTypeTranscode, URL: "http://gpu-3", Enabled: true, Healthy: true}})
	if node, _ := NewPlanner(NewProxyPool(), plain).ReserveTranscodeWork("w3"); node != nil {
		t.Fatalf("a node without KeyIssued was reserved: %+v", node)
	}
	plain.SetNodes([]*Node{{ID: 3, Type: NodeTypeTranscode, URL: "http://gpu-3", Enabled: true, Healthy: true, KeyIssued: true}})
	if node, _ := NewPlanner(NewProxyPool(), plain).ReserveTranscodeWork("w4"); node == nil || node.ID != 3 {
		t.Fatalf("a node with KeyIssued was not reserved: %+v", node)
	}
}

// The health sweep does not ask a keyless node for its capability report:
// nothing can sign that request, so it would fail on every sweep.
func TestHealthCheckerSkipsCapabilityFetchForKeylessNodes(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:new"}
	fixture := newCapabilityCheckerFixture(t, &Node{ID: 1, Name: "gpu-1", Type: NodeTypeTranscode, URL: url, Enabled: true}, fetcher)
	keyed := false
	fixture.pool.SetKeyCheck(func(int) bool { return keyed })

	fixture.sweep()
	if got := fetcher.callCount(); got != 0 {
		t.Fatalf("capability fetches for a keyless node = %d, want 0", got)
	}
	if !fixture.storedNode(t).Healthy {
		t.Fatal("a keyless node's health was not recorded")
	}
	keyed = true
	fixture.sweep()
	if got := fetcher.callCount(); got != 1 {
		t.Fatalf("capability fetches once keyed = %d, want 1", got)
	}
}

// Reports, logs and resolve read a node's name and URL on every call; the
// registry answers them from its last load, without touching the database,
// and says so when it does not hold the node.
func TestKeyRegistryAnswersIdentitiesFromItsLastLoad(t *testing.T) {
	rows := &fakeCredentialRows{}
	rows.set(nodeCredentialRow{ID: 4, Name: "gpu-4", Type: NodeTypeTranscode, URL: "http://gpu-4:8080"})
	registry, _ := newTestKeyRegistry(t, rows)
	loads := rows.loads.Load()
	rows.mu.Lock()
	rows.err = errors.New("database unavailable")
	rows.mu.Unlock()

	if got, ok := registry.Identity(4); !ok || got != (Identity{ID: 4, Name: "gpu-4", Type: NodeTypeTranscode, URL: "http://gpu-4:8080"}) {
		t.Fatalf("Identity(4) = %+v, %v", got, ok)
	}
	if _, ok := registry.Identity(5); ok {
		t.Fatal("Identity answered for a node the registry does not hold")
	}
	if n := rows.loads.Load(); n != loads {
		t.Fatalf("Identity reloaded the registry %d times", n-loads)
	}
}
