package config

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type readResult struct {
	values map[string]string
	err    error
}

// fakeManyReader answers GetMany from values/err, or from a per-call result.
// Each read reports its call number on entered when it begins and on done when
// it returns; a gate for that call number holds the read until closed.
type fakeManyReader struct {
	mu      sync.Mutex
	values  map[string]string
	err     error
	results map[int32]readResult
	gates   map[int32]chan struct{}
	keys    []string
	calls   atomic.Int32
	entered chan int32
	done    chan int32
}

func newFakeManyReader(values map[string]string) *fakeManyReader {
	return &fakeManyReader{
		values: values, results: map[int32]readResult{}, gates: map[int32]chan struct{}{},
		entered: make(chan int32, 64), done: make(chan int32, 64),
	}
}

func (f *fakeManyReader) GetMany(_ context.Context, keys ...string) (map[string]string, error) {
	call := f.calls.Add(1)
	f.entered <- call
	defer func() { f.done <- call }()
	f.mu.Lock()
	gate := f.gates[call]
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = keys
	if result, ok := f.results[call]; ok {
		return result.values, result.err
	}
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]string{}
	for _, key := range keys {
		if value, ok := f.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (f *fakeManyReader) set(key, value string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key], f.err = value, err
}

// gate holds read number call until the returned channel is closed.
func (f *fakeManyReader) gate(call int32) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{})
	f.gates[call] = ch
	return ch
}

func (f *fakeManyReader) waitDone(t *testing.T, call int32) {
	t.Helper()
	for got := range f.done {
		if got == call {
			return
		}
	}
}

// waitPublished waits for read number call to return and then for the refresh
// in flight to finish. The fake reports a read done before AdminDefaults
// stores its result, so a test that asserts on the snapshot right after
// waitDone would race the publish.
func waitPublished(t *testing.T, a *AdminDefaults, reader *fakeManyReader, call int32) {
	t.Helper()
	reader.waitDone(t, call)
	a.mu.Lock()
	wait := a.inflight
	a.mu.Unlock()
	if wait != nil {
		<-wait
	}
}

const (
	nextUpModeDefault  = "defaults.ui.next_up_mode"
	newReleasesDefault = "defaults.ui.next_up_new_releases_first"
)

func newTestAdminDefaults(t *testing.T, reader *fakeManyReader) (*AdminDefaults, *atomic.Int64) {
	t.Helper()
	a := NewAdminDefaults(reader)
	var clock atomic.Int64
	clock.Store(time.Unix(1_000_000, 0).UnixNano())
	a.now = func() time.Time { return time.Unix(0, clock.Load()) }
	return a, &clock
}

func advance(clock *atomic.Int64, d time.Duration) { clock.Add(int64(d)) }

func get(t *testing.T, a *AdminDefaults, key string) string {
	t.Helper()
	value, err := a.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%s): %v", key, err)
	}
	return value
}

// Every administrator default comes from one query, and the snapshot answers
// later reads of any key until it expires; an expired snapshot keeps answering
// while one background read replaces it.
func TestAdminDefaultsReadsEveryKeyInOneQueryPerTTL(t *testing.T) {
	reader := newFakeManyReader(map[string]string{nextUpModeDefault: `"separate"`, newReleasesDefault: "true"})
	a, clock := newTestAdminDefaults(t, reader)

	for range 3 {
		if got := get(t, a, nextUpModeDefault); got != `"separate"` {
			t.Fatalf("next up default = %q", got)
		}
		if got := get(t, a, newReleasesDefault); got != "true" {
			t.Fatalf("new releases default = %q", got)
		}
	}
	if n := reader.calls.Load(); n != 1 {
		t.Fatalf("reads = %d, want 1", n)
	}
	for _, want := range []string{nextUpModeDefault, newReleasesDefault, "defaults.ui.title_art"} {
		found := false
		for _, key := range reader.keys {
			found = found || key == want
		}
		if !found {
			t.Fatalf("query keys %v miss %s", reader.keys, want)
		}
	}

	reader.set(nextUpModeDefault, `"combined"`, nil)
	advance(clock, AdminDefaultsCacheTTL)
	if got := get(t, a, nextUpModeDefault); got != `"separate"` {
		t.Fatalf("expired read = %q, want the previous snapshot while refreshing", got)
	}
	waitPublished(t, a, reader, 2)
	if got := get(t, a, nextUpModeDefault); got != `"combined"` {
		t.Fatalf("after the refresh = %q, want the new value", got)
	}

	// A key that holds no administrator default is never read.
	if got := get(t, a, "defaults.ui.theme"); got != "" {
		t.Fatalf("non-admin-default key = %q", got)
	}
	if n := reader.calls.Load(); n != 2 {
		t.Fatalf("reads = %d, want 2", n)
	}
}

// While the settings table is unreachable, the last snapshot keeps answering
// and the node retries once per delay, not once per request.
func TestAdminDefaultsFailedRefreshKeepsSnapshotAndBacksOff(t *testing.T) {
	reader := newFakeManyReader(map[string]string{nextUpModeDefault: `"separate"`})
	a, clock := newTestAdminDefaults(t, reader)
	get(t, a, nextUpModeDefault)

	reader.set(nextUpModeDefault, "", errors.New("db down"))
	advance(clock, AdminDefaultsCacheTTL)
	get(t, a, nextUpModeDefault)
	reader.waitDone(t, 2)
	for range 5 {
		if got := get(t, a, nextUpModeDefault); got != `"separate"` {
			t.Fatalf("during outage = %q, want the last snapshot", got)
		}
	}
	if n := reader.calls.Load(); n != 2 {
		t.Fatalf("reads = %d, want one failed refresh after the initial read", n)
	}
	advance(clock, adminDefaultsRetryDelay)
	get(t, a, nextUpModeDefault)
	reader.waitDone(t, 3)
}

// Concurrent callers share one read, both for the first load and for the
// refresh of an expired snapshot.
func TestAdminDefaultsCoalescesConcurrentReads(t *testing.T) {
	reader := newFakeManyReader(map[string]string{nextUpModeDefault: `"separate"`})
	a, clock := newTestAdminDefaults(t, reader)

	hammer := func() []string {
		var wg sync.WaitGroup
		got := make([]string, 20)
		for i := range got {
			wg.Go(func() { got[i] = get(t, a, nextUpModeDefault) })
		}
		wg.Wait()
		return got
	}

	release := reader.gate(1)
	results := make(chan []string, 1)
	go func() { results <- hammer() }()
	<-reader.entered
	close(release)
	for _, got := range <-results {
		if got != `"separate"` {
			t.Fatalf("first load answered %q", got)
		}
	}
	if n := reader.calls.Load(); n != 1 {
		t.Fatalf("first load reads = %d, want 1", n)
	}

	advance(clock, AdminDefaultsCacheTTL)
	release = reader.gate(2)
	for _, got := range hammer() {
		if got != `"separate"` {
			t.Fatalf("while refreshing = %q, want the previous snapshot", got)
		}
	}
	<-reader.entered // the refresh, started by the first stale caller
	close(release)
	reader.waitDone(t, 2)
	if n := reader.calls.Load(); n != 2 {
		t.Fatalf("reads = %d, want 2", n)
	}
}

// An admin write's reload wins over a refresh that began before the write,
// even when the reload fails: the older read cannot publish and stretch the
// pre-write snapshot to a full TTL.
func TestAdminDefaultsChangedFencesOffOlderReads(t *testing.T) {
	reader := newFakeManyReader(map[string]string{newReleasesDefault: "false"})
	a, clock := newTestAdminDefaults(t, reader)
	get(t, a, newReleasesDefault)

	// Read 2: a background refresh that is slow and reads the old value.
	advance(clock, AdminDefaultsCacheTTL)
	slow := reader.gate(2)
	get(t, a, newReleasesDefault)
	<-reader.entered // the initial load
	<-reader.entered // read 2 is running
	reader.mu.Lock()
	reader.results[2] = readResult{values: map[string]string{newReleasesDefault: "false"}}
	reader.results[3] = readResult{err: errors.New("pool timeout")}
	reader.mu.Unlock()

	// Read 3: the admin write's reload, which fails.
	a.Changed(context.Background(), newReleasesDefault)
	close(slow)
	reader.waitDone(t, 2)

	// Without the fence read 2 would have published with a full TTL; instead
	// the next request after the retry delay reads again and sees the write.
	reader.set(newReleasesDefault, "true", nil)
	advance(clock, adminDefaultsRetryDelay)
	get(t, a, newReleasesDefault)
	waitPublished(t, a, reader, 4)
	if got := get(t, a, newReleasesDefault); got != "true" {
		t.Fatalf("after the retry = %q, want true", got)
	}

	// Changed with a key that holds no administrator default reads nothing.
	a.Changed(context.Background(), "server.name")
	if n := reader.calls.Load(); n != 4 {
		t.Fatalf("reads = %d, want 4", n)
	}
}

// An admin write's reload is visible to the next request at once.
func TestAdminDefaultsChangedRefreshesImmediately(t *testing.T) {
	reader := newFakeManyReader(map[string]string{newReleasesDefault: "false"})
	a, _ := newTestAdminDefaults(t, reader)
	get(t, a, newReleasesDefault)

	reader.set(newReleasesDefault, "true", nil)
	a.Changed(context.Background(), newReleasesDefault)
	if got := get(t, a, newReleasesDefault); got != "true" {
		t.Fatalf("after Changed = %q, want true", got)
	}
}

// A node that missed another node's change event and serves no requests still
// picks up the new default within the TTL, so a client's delayed re-read finds
// it instead of starting a refresh and getting the old snapshot back.
func TestAdminDefaultsRunRefreshesWithoutReads(t *testing.T) {
	reader := newFakeManyReader(map[string]string{nextUpModeDefault: `"combined"`})
	a := NewAdminDefaults(reader)
	a.ttl = 40 * time.Millisecond
	get(t, a, nextUpModeDefault)
	reader.waitDone(t, 1)

	// Another node wrote the default; this node never heard about it.
	reader.set(nextUpModeDefault, `"separate"`, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	waitPublished(t, a, reader, 2)

	if got := get(t, a, nextUpModeDefault); got != `"separate"` {
		t.Fatalf("after a background refresh with no reads = %q, want separate", got)
	}
}
