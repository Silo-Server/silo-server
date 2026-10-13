package autoscan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// stateSuppressor is an in-memory Suppressor with the claim semantics of the
// Redis script: a claim is refused only when the live claim holds the same
// observed state. The window never expires within a test.
type stateSuppressor struct {
	claims   map[string]string
	released []string
}

func newStateSuppressor() *stateSuppressor {
	return &stateSuppressor{claims: map[string]string{}}
}

func (s *stateSuppressor) ShouldScan(_ context.Context, key, state string, _ time.Duration) (bool, error) {
	if current, ok := s.claims[key]; ok && current == state {
		return false, nil
	}
	s.claims[key] = state
	return true, nil
}

func (s *stateSuppressor) Release(_ context.Context, key, state string) error {
	s.released = append(s.released, key)
	if s.claims[key] == state {
		delete(s.claims, key)
	}
	return nil
}

// fakeFS stands in for the library filesystem the service observes.
type fakeFS struct {
	files map[string]string // path -> state token
	dirs  map[string]bool
}

func newFakeFS() *fakeFS { return &fakeFS{files: map[string]string{}, dirs: map[string]bool{}} }

func (f *fakeFS) observe(path string) (string, bool) {
	if f.dirs[path] {
		return "", false
	}
	if state, ok := f.files[path]; ok {
		return state, true
	}
	return stateAbsent, true
}

func newDebounceService(store Store, q Queuer, sup Suppressor, fs *fakeFS) *Service {
	svc := NewService(store, &fakeProvider{}, passthroughConnRes{}, fakeResolver{}, q, sup, nil)
	svc.observe = fs.observe
	return svc
}

func ingestFile(t *testing.T, svc *Service, eventType, path string) IngestResult {
	t.Helper()
	result, err := svc.IngestChanges(context.Background(), ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: eventType,
		Changes:           []Change{{SourcePath: path, Scope: ChangeScopeFile}},
	})
	if err != nil {
		t.Fatalf("IngestChanges(%s): %v", eventType, err)
	}
	return result
}

func TestDebounceSuppressesFloodOfIdenticalReports(t *testing.T) {
	const path = "/mnt/media/Show/Season 01/e01.mkv"
	fs := newFakeFS()
	fs.files[path] = "file:100:1"
	q := &recordingQueuer{}
	svc := newDebounceService(webhookTestStore(), q, newStateSuppressor(), fs)

	suppressed := 0
	for range 20 {
		suppressed += ingestFile(t, svc, "Download", path).Suppressed
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("enqueued = %d, want 1 scan for 20 identical reports", len(q.enqueued))
	}
	if suppressed != 19 {
		t.Fatalf("suppressed = %d, want 19", suppressed)
	}
}

func TestDebounceScansDeleteAfterImportWithinWindow(t *testing.T) {
	// Sonarr renames an episode (the new path is claimed and scanned), then
	// the episode file is deleted inside the debounce window. The delete is a
	// new change at the same path and must scan so the catalog drops it.
	const path = "/mnt/media/Better Call Saul (2015)/Season 01/S01E01 - Uno.mkv"
	fs := newFakeFS()
	fs.files[path] = "file:100:1"
	q := &recordingQueuer{}
	svc := newDebounceService(webhookTestStore(), q, newStateSuppressor(), fs)

	if r := ingestFile(t, svc, "Rename", path); r.Enqueued != 1 || r.Suppressed != 0 {
		t.Fatalf("rename result = %+v, want one scan", r)
	}
	delete(fs.files, path)
	if r := ingestFile(t, svc, "EpisodeFileDelete", path); r.Enqueued != 1 || r.Suppressed != 0 {
		t.Fatalf("delete result = %+v, want one scan and no suppression", r)
	}
	// A redelivery of the same delete is a duplicate.
	if r := ingestFile(t, svc, "EpisodeFileDelete", path); r.Enqueued != 0 || r.Suppressed != 1 {
		t.Fatalf("duplicate delete result = %+v, want suppressed", r)
	}
	if len(q.enqueued) != 2 {
		t.Fatalf("enqueued = %+v, want 2 scans", q.enqueued)
	}
}

func TestDebounceScansReplacementAtSamePath(t *testing.T) {
	const path = "/mnt/media/Movie (2020)/Movie (2020).mkv"
	fs := newFakeFS()
	q := &recordingQueuer{}
	svc := newDebounceService(webhookTestStore(), q, newStateSuppressor(), fs)

	steps := []struct {
		name  string
		state string // "" removes the file
		scan  bool
	}{
		{name: "import", state: "file:100:1:10", scan: true},
		{name: "duplicate import", state: "file:100:1:10", scan: false},
		{name: "replacement with new size and mtime", state: "file:200:2:11", scan: true},
		{name: "rewrite with new mtime only", state: "file:200:3:11", scan: true},
		{name: "replacement with same size and mtime", state: "file:200:3:12", scan: true},
		{name: "duplicate of the replacement", state: "file:200:3:12", scan: false},
		{name: "delete", state: "", scan: true},
		{name: "re-import of the original file", state: "file:100:1:10", scan: true},
	}
	for _, step := range steps {
		if step.state == "" {
			delete(fs.files, path)
		} else {
			fs.files[path] = step.state
		}
		before := len(q.enqueued)
		r := ingestFile(t, svc, "Download", path)
		if scanned := len(q.enqueued) > before; scanned != step.scan {
			t.Fatalf("%s: scanned = %v, want %v (result %+v)", step.name, scanned, step.scan, r)
		}
	}
}

func TestDebounceNeverSuppressesExistingDirectoryReports(t *testing.T) {
	// A directory's own metadata cannot show a change deeper in its tree, so
	// repeated subtree reports always reach the queue, which coalesces them.
	const dir = "/mnt/media/Show"
	fs := newFakeFS()
	fs.dirs[dir] = true
	q := &recordingQueuer{}
	sup := newStateSuppressor()
	svc := newDebounceService(webhookTestStore(), q, sup, fs)

	for range 2 {
		result, err := svc.IngestChanges(context.Background(), ChangeIngest{
			SourceID:          "s1",
			ProviderEventType: "Download",
			Changes:           []Change{{SourcePath: dir, Scope: ChangeScopeSubtree}},
		})
		if err != nil {
			t.Fatalf("IngestChanges: %v", err)
		}
		if result.Enqueued != 1 || result.Suppressed != 0 {
			t.Fatalf("result = %+v, want one scan", result)
		}
	}
	if got := sup.claims["7|"+dir]; got != stateUnobserved {
		t.Fatalf("directory claim = %q, want %q so no later report matches it", got, stateUnobserved)
	}
}

func TestDebounceScansSecondDeleteAfterDirectoryRecreated(t *testing.T) {
	// A series folder is deleted, re-created with new episodes, and deleted
	// again inside one window. The re-created directory's report must replace
	// the "absent" claim, or the second delete would match it and be dropped.
	const dir = "/mnt/media/Show"
	fs := newFakeFS()
	q := &recordingQueuer{}
	svc := newDebounceService(webhookTestStore(), q, newStateSuppressor(), fs)
	report := func(step string) {
		t.Helper()
		result, err := svc.IngestChanges(context.Background(), ChangeIngest{
			SourceID:          "s1",
			ProviderEventType: "Download",
			Changes:           []Change{{SourcePath: dir, Scope: ChangeScopeSubtree}},
		})
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if result.Enqueued != 1 || result.Suppressed != 0 {
			t.Fatalf("%s: result = %+v, want one scan", step, result)
		}
	}

	report("first delete")
	fs.dirs[dir] = true
	report("re-created")
	delete(fs.dirs, dir)
	report("second delete")
}

func TestPollOnceLegacyPathsClaimEachReportedPath(t *testing.T) {
	const e01, e02 = "/mnt/media/Show/S01/E01.mkv", "/mnt/media/Show/S01/E02.mkv"
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{"arr": {e01, e02}}, nextMarker: "m1"}
	fs := newFakeFS()
	fs.files[e01] = "file:100:1"
	fs.files[e02] = "file:100:1"
	q := &recordingQueuer{}
	sup := newStateSuppressor()
	svc := newService(store, prov, q, sup)
	svc.observe = fs.observe

	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 || q.enqueued[0].Path != "/mnt/media/Show/S01" {
		t.Fatalf("enqueued = %+v, want one directory scan", q.enqueued)
	}
	if _, ok := sup.claims["7|"+e01]; !ok {
		t.Fatalf("claims = %v, want one per reported path", sup.claims)
	}
	if _, ok := sup.claims["7|"+e02]; !ok {
		t.Fatalf("claims = %v, want one per reported path", sup.claims)
	}

	// The next window reports both again: E01 is unchanged, E02 was deleted.
	delete(fs.files, e02)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 2 {
		t.Fatalf("enqueued = %+v, want the deletion to scan the directory again", q.enqueued)
	}
	ev := store.events[len(store.events)-1]
	if ev.ChangesResolved != 2 || ev.ScansSuppressed != 1 || ev.TargetsClaimed != 1 {
		t.Fatalf("second event = %+v, want 2 resolved, 1 suppressed, 1 target", ev)
	}
}

func TestDebounceReleasesStateClaimWhenEnqueueFails(t *testing.T) {
	const path = "/mnt/media/Movie/movie.mkv"
	fs := newFakeFS()
	fs.files[path] = "file:100:1"
	sup := newStateSuppressor()
	svc := newDebounceService(webhookTestStore(), failingQueuer{}, sup, fs)

	result, err := svc.IngestChanges(context.Background(), ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: "Download",
		Changes:           []Change{{SourcePath: path, Scope: ChangeScopeFile}},
	})
	if err != nil {
		t.Fatalf("IngestChanges: %v", err)
	}
	if !result.Pending {
		t.Fatalf("result = %+v, want the delivery left pending for retry", result)
	}
	if len(sup.released) != 1 || sup.released[0] != "7|"+path || len(sup.claims) != 0 {
		t.Fatalf("released = %v claims = %v, want the claim dropped", sup.released, sup.claims)
	}
}

func TestObservePathState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")

	if state, ok := observePathState(path); !ok || state != stateAbsent {
		t.Fatalf("missing file = (%q, %v), want (%q, true)", state, ok, stateAbsent)
	}

	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	first, ok := observePathState(path)
	if !ok || first == stateAbsent {
		t.Fatalf("existing file = (%q, %v), want a file state", first, ok)
	}
	if again, _ := observePathState(path); again != first {
		t.Fatalf("unchanged file state %q != %q", again, first)
	}

	if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	resized, _ := observePathState(path)
	if resized == first {
		t.Fatalf("size change kept state %q", resized)
	}

	later := mtime.Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if touched, _ := observePathState(path); touched == resized {
		t.Fatalf("mtime change kept state %q", touched)
	}

	if state, ok := observePathState(dir); ok {
		t.Fatalf("directory = (%q, true), want never debounced", state)
	}
}

func TestObservePathStateReplacementWithSameSizeAndMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	writeAt := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	writeAt(path, "abc")
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fileIdentity(info) == "" {
		t.Skip("no file identity on this platform; the state is size and mtime only")
	}
	original, ok := observePathState(path)
	if !ok || original == stateAbsent {
		t.Fatalf("existing file = (%q, %v), want a file state", original, ok)
	}

	// An importer writes the new file beside the old one and renames it into
	// place: same path, same size, same modification time, different file.
	tmp := filepath.Join(dir, "movie.mkv.partial")
	writeAt(tmp, "xyz")
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	replaced, ok := observePathState(path)
	if !ok {
		t.Fatalf("replaced file = (%q, false), want debounceable", replaced)
	}
	if replaced == original {
		t.Fatalf("replacement kept state %q", replaced)
	}
	if again, _ := observePathState(path); again != replaced {
		t.Fatalf("unchanged replacement state %q != %q", again, replaced)
	}
}

func TestRedisSuppressorWithoutRedisAlwaysScans(t *testing.T) {
	sup := NewRedisSuppressor(nil)
	for range 2 {
		ok, err := sup.ShouldScan(context.Background(), "7|/mnt/media/a.mkv", "file:1:1", time.Minute)
		if err != nil || !ok {
			t.Fatalf("ShouldScan without Redis = (%v, %v), want (true, nil)", ok, err)
		}
	}
}

func TestRedisSuppressorClaimsByObservedState(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("SILO_TEST_REDIS_URL required for the Redis suppressor integration test")
	}
	opts, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	ctx := t.Context()
	key := "7|/mnt/media/debounce-" + time.Now().Format("150405.000000000") + ".mkv"
	t.Cleanup(func() { client.Del(context.Background(), suppressRedisKey(key)) })

	sup := NewRedisSuppressor(client)
	claim := func(state string, ttl time.Duration) bool {
		t.Helper()
		ok, err := sup.ShouldScan(ctx, key, state, ttl)
		if err != nil {
			t.Fatalf("ShouldScan(%q): %v", state, err)
		}
		return ok
	}

	if !claim("file:100:1", time.Hour) {
		t.Fatal("first report must claim")
	}
	ttlAfterClaim, err := client.PTTL(ctx, suppressRedisKey(key)).Result()
	if err != nil || ttlAfterClaim <= 0 || ttlAfterClaim > time.Hour {
		t.Fatalf("claim TTL = %v (%v), want within one hour", ttlAfterClaim, err)
	}
	if claim("file:100:1", 2*time.Hour) {
		t.Fatal("identical report must be suppressed")
	}
	if ttl, _ := client.PTTL(ctx, suppressRedisKey(key)).Result(); ttl > ttlAfterClaim {
		t.Fatalf("duplicate extended the window: %v > %v", ttl, ttlAfterClaim)
	}
	if !claim(stateAbsent, time.Hour) {
		t.Fatal("deletion must claim")
	}
	if !claim("file:100:1", time.Hour) {
		t.Fatal("re-import of the earlier state must claim")
	}

	// A value left by an older release never matches a state token.
	if err := client.Set(ctx, suppressRedisKey(key), "1", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if !claim("file:100:1", time.Hour) {
		t.Fatal("legacy claim value must not suppress a state report")
	}

	// Release only drops the claim this report wrote, not a newer one.
	if err := sup.Release(ctx, key, stateAbsent); err != nil {
		t.Fatal(err)
	}
	if claim("file:100:1", time.Hour) {
		t.Fatal("release of another state must keep the live claim")
	}
	if err := sup.Release(ctx, key, "file:100:1"); err != nil {
		t.Fatal(err)
	}
	if !claim("file:100:1", time.Hour) {
		t.Fatal("released claim must allow the same state again")
	}
}
