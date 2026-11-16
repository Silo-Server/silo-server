package nodepool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
)

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

func (f *fakeCredentialRows) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeCredentialRows) load(context.Context) ([]nodeCredentialRow, error) {
	f.loads.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nodeCredentialRow(nil), f.rows...), f.err
}

func credentialRow(id int, nodeType, nodeURL string) nodeCredentialRow {
	return nodeCredentialRow{Identity: Identity{ID: id, Name: "node", Type: nodeType, URL: nodeURL}}
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
	row := credentialRow(id, NodeTypeTranscode, nodeURL)
	row.Ciphertext = &ciphertext
	return row, key
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

func TestKeyRegistryTracksKeysRotationAndDeletion(t *testing.T) {
	keyed, key := keyedRow(t, 1, "http://gpu-1")
	undecryptable, _ := keyedRow(t, 2, "http://gpu-2")
	undecryptable.ID = 4 // node 2's ciphertext on node 4's row
	rows := &fakeCredentialRows{}
	rows.set(keyed,
		credentialRow(2, NodeTypeTranscode, "http://gpu-2"),
		credentialRow(3, NodeTypeProxy, "http://proxy-3"),
		undecryptable)
	registry, clock := newTestKeyRegistry(t, rows)

	for _, tc := range []struct {
		id   int
		want string
		err  error
	}{
		{1, key, nil},
		{2, "", ErrNoNodeKey},
		{3, "", ErrNodeKeyNotIssuable},
		{4, "", ErrNoNodeKey},
		{99, "", ErrNodeNotFound},
	} {
		if got, err := registry.NodeKey(tc.id); got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("NodeKey(%d) = %q, %v; want %q, %v", tc.id, got, err, tc.want, tc.err)
		}
	}

	rotated, rotatedKey := keyedRow(t, 1, "http://gpu-1")
	rows.set(rotated)
	if err := registry.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := registry.NodeKey(1); got != rotatedKey {
		t.Fatal("the rotated key was not adopted on refresh")
	}
	if registry.HasKey(2) {
		t.Fatal("a deleted node still has a key")
	}

	// A failed reload keeps what the registry already had, and Identity
	// answers from the last load without reading again.
	rows.fail(errors.New("database unavailable"))
	if err := registry.Refresh(t.Context()); err == nil {
		t.Fatal("a failed reload reported success")
	}
	clock.Advance(time.Hour)
	loads := rows.loads.Load()
	if got, ok := registry.Identity(1); !ok || got != rotated.Identity {
		t.Fatalf("Identity(1) = %+v, %v", got, ok)
	}
	if _, ok := registry.Identity(2); ok {
		t.Fatal("Identity answered for a deleted node")
	}
	if rows.loads.Load() != loads {
		t.Fatal("Identity reloaded the registry")
	}
	if got, _ := registry.NodeKey(1); got != rotatedKey {
		t.Fatal("a failed reload dropped the loaded credentials")
	}
}

// Rows that share a canonical URL resolve the same way whatever order they
// load in: a transcode node over a proxy, then the lowest id. A proxy must
// never shadow a transcode node, or the shared secret could be sent to it.
func TestKeyRegistryURLIndexIsDeterministic(t *testing.T) {
	low, lowKey := keyedRow(t, 5, "http://Shared-Host:80/%77orker/")
	high, _ := keyedRow(t, 9, "http://shared-host/worker")
	proxy := credentialRow(2, NodeTypeProxy, "HTTP://shared-host/worker")
	for _, order := range [][]nodeCredentialRow{
		{low, high, proxy},
		{proxy, high, low},
		{high, proxy, low},
	} {
		rows := &fakeCredentialRows{}
		rows.set(order...)
		registry, _ := newTestKeyRegistry(t, rows)
		for _, spelling := range []string{"http://shared-host/worker", " http://SHARED-HOST:80/%77orker/ "} {
			if cred := registry.transcodeByURL(spelling); cred == nil || cred.key != lowKey {
				t.Fatalf("order %v: transcodeByURL(%q) = %+v; want node 5", []int{order[0].ID, order[1].ID, order[2].ID}, spelling, cred)
			}
			if registry.proxyByURL(spelling) {
				t.Fatalf("proxyByURL(%q) answered for a transcode node's URL", spelling)
			}
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

// After a proxy's URL is given to a transcode node, a cached proxy entry is a
// miss for a transcode lookup: it reloads, and inside the miss cooldown it
// finds nothing rather than the proxy.
func TestKeyRegistryNeverAnswersATranscodeLookupWithAStaleProxy(t *testing.T) {
	rows := &fakeCredentialRows{}
	rows.set(credentialRow(1, NodeTypeProxy, "http://reused:8080"))
	registry, clock := newTestKeyRegistry(t, rows)
	transcode, key := keyedRow(t, 2, "http://reused:8080")
	rows.set(transcode)

	if cred := registry.transcodeByURL("http://reused:8080"); cred.keyed() {
		t.Fatal("a transcode lookup inside the cooldown found a key it cannot have loaded")
	}
	clock.Advance(keyRegistryMissInterval)
	if cred := registry.transcodeByURL("http://reused:8080"); cred == nil || cred.key != key {
		t.Fatalf("transcodeByURL after the reload = %+v; want node 2", cred)
	}
	if registry.proxyByURL("http://reused:8080") {
		t.Fatal("the URL still answers as a proxy")
	}
}

func TestKeyRegistryReloadsOnAMissAtMostOncePerSecond(t *testing.T) {
	rows := &fakeCredentialRows{}
	registry, clock := newTestKeyRegistry(t, rows)
	loadsAfterStart := rows.loads.Load()

	// Within a second of the last load, misses do not query.
	for range 5 {
		if _, err := registry.NodeKey(4); !errors.Is(err, ErrNodeNotFound) {
			t.Fatalf("NodeKey = %v", err)
		}
	}
	if got := rows.loads.Load(); got != loadsAfterStart {
		t.Fatalf("misses inside the interval loaded %d times", got-loadsAfterStart)
	}

	// A keyless node is a miss too: its key may have just been issued.
	issued, key := keyedRow(t, 4, "http://gpu-4")
	rows.set(issued)
	clock.Advance(keyRegistryMissInterval)
	if got, err := registry.NodeKey(4); err != nil || got != key {
		t.Fatalf("NodeKey after the interval = %q, %v", got, err)
	}
	// A hit never reloads.
	clock.Advance(time.Hour)
	if _, err := registry.NodeKey(4); err != nil {
		t.Fatal(err)
	}
	if got := rows.loads.Load() - loadsAfterStart; got != 1 {
		t.Fatalf("loads = %d, want 1", got)
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

// Concurrent misses share one reload, and the cooldown runs from when that
// reload finishes: a load slower than the cooldown must not let each waiting
// miss start another.
func TestKeyRegistryCoalescesMissReloadsAndCoolsDownAfterThem(t *testing.T) {
	rows := &fakeCredentialRows{}
	registry, clock := newTestKeyRegistry(t, rows)
	gate := newGatedLoader(rows)
	registry.load = gate.load
	created, key := keyedRow(t, 4, "http://new-node")
	rows.set(created)
	clock.Advance(keyRegistryMissInterval)
	before := rows.loads.Load()

	var wg sync.WaitGroup
	wg.Go(func() {
		if got, err := registry.NodeKey(4); err != nil || got != key {
			t.Errorf("first miss = %q, %v", got, err)
		}
	})
	<-gate.started
	for range 7 {
		wg.Go(func() {
			if _, err := registry.NodeKey(99); !errors.Is(err, ErrNodeNotFound) {
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
	if _, err := registry.NodeKey(99); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("NodeKey miss inside the cooldown = %v", err)
	}
}

// A Refresh that arrives while a miss reload is running must still see rows
// written after that reload read them: it waits the running one out and runs
// one more. The key issue handler relies on this to drop the old key.
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

// node_pool_changed reaches the registry through Invalidate, which reloads in
// the Start loop without the caller waiting.
func TestKeyRegistryInvalidateReloadsInTheBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rows := &fakeCredentialRows{}
		registry, _ := newTestKeyRegistry(t, rows)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		registry.Start(ctx)

		row, key := keyedRow(t, 8, "http://gpu-8")
		rows.set(row)
		registry.Invalidate()
		synctest.Wait()
		if cred := registry.lookupID(8); cred == nil || cred.key != key {
			t.Fatal("Invalidate did not reload the registry")
		}
		if got := rows.loads.Load(); got != 2 {
			t.Fatalf("loads = %d, want the initial load and one reload", got)
		}
	})
}

// The keyring never hands the shared secret to a transcode target or a node
// key to a proxy target, and an unknown or keyless node is an error rather
// than a reason to fall back to the shared secret.
func TestKeyringKeepsTranscodeNodesOffTheSharedSecret(t *testing.T) {
	const shared = "shared-jwt-secret"
	keyed, key := keyedRow(t, 1, "http://gpu-1")
	rows := &fakeCredentialRows{}
	rows.set(keyed,
		credentialRow(2, NodeTypeTranscode, "http://gpu-2"),
		credentialRow(3, NodeTypeProxy, "http://proxy-3"))
	registry, _ := newTestKeyRegistry(t, rows)
	keys := registry.Keyring(func() string { return shared })

	for _, tc := range []struct {
		target nodeauth.Target
		url    string
		want   string
		err    error
	}{
		{nodeauth.TargetTranscode, "http://gpu-1/", key, nil},
		{nodeauth.TargetTranscode, "http://gpu-2", "", nodeauth.ErrNoNodeKey},
		{nodeauth.TargetTranscode, "http://proxy-3", "", nodeauth.ErrUnknownNode},
		{nodeauth.TargetTranscode, "http://nowhere", "", nodeauth.ErrUnknownNode},
		{nodeauth.TargetProxy, "http://proxy-3", shared, nil},
		{nodeauth.TargetProxy, "http://gpu-1", "", nodeauth.ErrUnknownNode},
		{nodeauth.TargetProxy, "http://gpu-2", "", nodeauth.ErrUnknownNode},
		{nodeauth.TargetProxy, "http://nowhere", "", nodeauth.ErrUnknownNode},
	} {
		got, err := keys.KeyFor(tc.target, tc.url)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("KeyFor(%v, %q) = %q, %v; want %q, %v", tc.target, tc.url, got, err, tc.want, tc.err)
		}
	}
	// Callers test one sentinel for a keyless node, whichever package
	// reported it.
	if _, err := registry.NodeKey(2); !errors.Is(err, nodeauth.ErrNoNodeKey) {
		t.Fatalf("NodeKey(2) = %v, want nodeauth.ErrNoNodeKey", err)
	}
}

// A relay whose signature a transcode node refused asks the keyring to
// reload: the registry picks up a key rotated on another server at once,
// without the relay waiting, and a stream of refusals reloads at most once a
// second.
func TestKeyringReloadsAfterANodeRefusesItsSignature(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old, _ := keyedRow(t, 8, "http://gpu-8")
		rows := &fakeCredentialRows{}
		rows.set(old)
		registry, clock := newTestKeyRegistry(t, rows)
		keys := registry.Keyring(func() string { return "shared" })
		before := rows.loads.Load()

		rotated, key := keyedRow(t, 8, "http://gpu-8")
		rows.set(rotated)
		clock.Advance(keyRegistryMissInterval)
		for range 20 {
			nodeauth.ReloadAfterRefusal(keys)
		}
		synctest.Wait()
		if got, err := keys.KeyFor(nodeauth.TargetTranscode, "http://gpu-8"); err != nil || got != key {
			t.Fatalf("KeyFor after a refusal = %q, %v; want the rotated key", got, err)
		}
		if got := rows.loads.Load() - before; got != 1 {
			t.Fatalf("20 refusals loaded %d times, want 1", got)
		}
		for range 20 {
			nodeauth.ReloadAfterRefusal(keys)
		}
		synctest.Wait()
		if got := rows.loads.Load() - before; got != 1 {
			t.Fatalf("refusals inside the interval loaded %d more times", got-1)
		}
	})
}
