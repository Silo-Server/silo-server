package recommendations

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// fakeLocker grants or refuses every lock and records what was asked of it.
type fakeLocker struct {
	mu       sync.Mutex
	held     bool  // another server holds every lock
	err      error // taking a lock fails
	keys     []int64
	released int
}

func (l *fakeLocker) TryLock(_ context.Context, key int64) (clusterLock, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = append(l.keys, key)
	if l.err != nil || l.held {
		return nil, false, l.err
	}
	return fakeClusterLock{take: l.TryLock, release: func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.released++
	}}, true, nil
}

// fakeClusterLock is a test clusterLock. Its extra keys go through take, as
// if each were a lock of its own.
type fakeClusterLock struct {
	take    func(ctx context.Context, key int64) (clusterLock, bool, error)
	release func()
}

func (l fakeClusterLock) TryAlso(ctx context.Context, key int64) (func(), bool, error) {
	other, acquired, err := l.take(ctx, key)
	if err != nil || !acquired {
		return nil, false, err
	}
	return other.Release, true, nil
}

func (l fakeClusterLock) Check(context.Context) error { return nil }

func (l fakeClusterLock) Release() { l.release() }

func (l *fakeLocker) snapshot() (keys []int64, released int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int64(nil), l.keys...), l.released
}

// fakeHistory stores runs in memory and signals each insert on inserted.
type fakeHistory struct {
	mu       sync.Mutex
	runs     []taskmanager.ExecutionResult
	inserted chan taskmanager.ExecutionResult
	err      error
}

func newFakeHistory() *fakeHistory {
	return &fakeHistory{inserted: make(chan taskmanager.ExecutionResult, 16)}
}

func (h *fakeHistory) Insert(_ context.Context, run taskmanager.ExecutionResult) error {
	h.mu.Lock()
	h.runs = append(h.runs, run)
	h.mu.Unlock()
	h.inserted <- run
	return nil
}

func (h *fakeHistory) GetLatest(_ context.Context, key string) (*taskmanager.ExecutionResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return nil, h.err
	}
	for i := len(h.runs) - 1; i >= 0; i-- {
		if h.runs[i].TaskKey == key {
			run := h.runs[i]
			return &run, nil
		}
	}
	return nil, nil
}

func (h *fakeHistory) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.runs)
}

func newJobTestWorker(locker jobLocker, history JobHistory) *Worker {
	w := &Worker{
		locker:                locker,
		running:               make(map[JobName]bool),
		profileRefreshCh:      make(chan profileRefreshRequest, 1),
		profileRefreshPending: make(map[string]struct{}),
	}
	if history != nil {
		w.history = history
	}
	return w
}

type countsResult struct {
	Done   int `json:"done"`
	Failed int `json:"failed"`
}

func (r countsResult) failures() int { return r.Failed }

func waitRun(t *testing.T, h *fakeHistory) taskmanager.ExecutionResult {
	t.Helper()
	select {
	case run := <-h.inserted:
		return run
	case <-time.After(10 * time.Second):
		t.Fatal("job run was not recorded")
		return taskmanager.ExecutionResult{}
	}
}

func TestRunJobLockFailureIsNotABusyJob(t *testing.T) {
	w := newJobTestWorker(&fakeLocker{err: errors.New("connection refused")}, newFakeHistory())
	err := w.runJob(w.jobFor(JobTasteProfiles), true)
	if err == nil || errors.Is(err, ErrJobRunning) || errors.Is(err, ErrJobRunningElsewhere) {
		t.Fatalf("error = %v, want a lock failure", err)
	}
	if w.IsRunning(JobTasteProfiles) {
		t.Fatal("a job that never started is still marked running")
	}
}

func TestRunJobAppliesTheJobTimeout(t *testing.T) {
	history := newFakeHistory()
	w := newJobTestWorker(&fakeLocker{}, history)
	if err := w.runJob(job{name: JobCowatch, timeout: time.Millisecond, run: func(ctx context.Context, _ clusterLock) (jobResult, error) {
		<-ctx.Done()
		return countsResult{}, ctx.Err()
	}}, false); err != nil {
		t.Fatal(err)
	}
	if run := waitRun(t, history); run.Status != runStatusFailed || !strings.Contains(run.ErrorMessage, "deadline") {
		t.Fatalf("timed-out run = %+v", run)
	}
}

// fakeGlobalRows serves the global row queries, failing the ones named in
// fail, and records what it cached and which cached rows it extended.
type fakeGlobalRows struct {
	cached    bool
	cacheErr  error
	fail      map[string]bool
	written   map[string][]ScoredItem
	expires   string
	extended  []string // "type" or "type*" for a prefix, with its expiry
	extendErr error
	deleted   []string // "type" or "type*" for a prefix, with the kept types
	// popularArgs, recentArgs and genreLimit record what the build asked
	// for.
	popularArgs [3]int // days, minAccounts, limit
	recentArgs  [2]int // days, limit
	genreLimit  int
	deleteErr   error
}

func (f *fakeGlobalRows) result(name string, items []ScoredItem) ([]ScoredItem, error) {
	if f.fail[name] {
		return nil, errors.New(name + " query failed")
	}
	return items, nil
}

func (f *fakeGlobalRows) GetPopularItems(_ context.Context, days, minAccounts, limit int) ([]ScoredItem, error) {
	f.popularArgs = [3]int{days, minAccounts, limit}
	return f.result("popular", []ScoredItem{{MediaItemID: "p"}})
}

func (f *fakeGlobalRows) GetRecentlyAddedItems(_ context.Context, days, limit int) ([]ScoredItem, error) {
	f.recentArgs = [2]int{days, limit}
	return f.result("recent", []ScoredItem{{MediaItemID: "r"}})
}

func (f *fakeGlobalRows) GetTopGenres(_ context.Context, limit int) ([]string, error) {
	f.genreLimit = limit
	if f.fail["genres"] {
		return nil, errors.New("genres query failed")
	}
	return []string{"Drama", "Comedy"}, nil
}

func (f *fakeGlobalRows) GetGenreSamplerItems(_ context.Context, genre string, _ int) ([]ScoredItem, error) {
	return f.result(genre, []ScoredItem{{MediaItemID: genre}})
}

func (f *fakeGlobalRows) UpsertRecommendationCache(_ context.Context, userID int, profileID, recType, _ string, items []ScoredItem, expires string) error {
	if userID != GlobalCacheUserID || profileID != GlobalCacheProfileID {
		return errors.New("global row written to a profile")
	}
	if f.fail["write:"+recType] {
		return errors.New("write failed")
	}
	if f.written == nil {
		f.written = map[string][]ScoredItem{}
	}
	f.written[recType] = items
	f.expires = expires
	return nil
}

func (f *fakeGlobalRows) ExtendGlobalRecommendationCache(_ context.Context, recType string, prefix bool, expires string) (int64, error) {
	if prefix {
		recType += "*"
	}
	f.extended = append(f.extended, recType+"@"+expires)
	return 1, f.extendErr
}

func (f *fakeGlobalRows) DeleteGlobalRecommendationCache(_ context.Context, recType string, prefix bool, keep []string) (int64, error) {
	if prefix {
		recType += "*"
	}
	f.deleted = append(f.deleted, recType+"-"+strings.Join(keep, "+"))
	return 1, f.deleteErr
}

func (f *fakeGlobalRows) HasGlobalRecommendationCache(context.Context) (bool, error) {
	return f.cached, f.cacheErr
}

// closedPoolEngine returns an engine whose every database call fails.
func closedPoolEngine(t *testing.T) *Engine {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://silo@127.0.0.1:1/silo")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	return NewEngine(pool, nil, nil, nil, nil, config.RecommendationsConfig{})
}

func TestStaleSweepSkipsWhileAnotherServerSweeps(t *testing.T) {
	locker := &fakeLocker{held: true}
	// No engine: a sweep that went ahead would dereference it.
	w := newJobTestWorker(locker, nil)
	w.refreshStaleProfiles(t.Context())
	if keys, _ := locker.snapshot(); len(keys) != 1 || keys[0] != staleSweepLock {
		t.Fatalf("sweep lock requests = %#x", keys)
	}
}

// recordingStaleMarker records stale marks and how many refreshes were
// already queued when each one happened.
type recordingStaleMarker struct {
	w            *Worker
	calls        int
	queuedBefore []int
	ctxErrs      []error
	err          error
}

func (m *recordingStaleMarker) MarkProfileStale(ctx context.Context, _ int, _ string) error {
	m.calls++
	m.ctxErrs = append(m.ctxErrs, ctx.Err())
	if m.w != nil {
		m.queuedBefore = append(m.queuedBefore, len(m.w.profileRefreshCh))
	}
	return m.err
}

func newRefreshTestWorker() (*Worker, *recordingStaleMarker) {
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = &Engine{}
	marker := &recordingStaleMarker{w: w}
	w.staleMarker = marker
	return w, marker
}

// takeQueued drains one queued refresh, failing when none is queued, and
// releases its pending key as the refresh loop would.
func takeQueued(t *testing.T, w *Worker) profileRefreshRequest {
	t.Helper()
	select {
	case req := <-w.profileRefreshCh:
		w.clearProfileRefreshPending(profileRefreshKey(req.userID, req.profileID))
		return req
	default:
		t.Fatal("no profile refresh was queued")
		return profileRefreshRequest{}
	}
}

func assertNothingQueued(t *testing.T, w *Worker, stage string) {
	t.Helper()
	select {
	case req := <-w.profileRefreshCh:
		t.Fatalf("%s: queued %+v, want nothing", stage, req)
	default:
	}
}

// NotifySignalsChanged writes the durable stale mark before it queues the
// in-memory refresh, so a server that dies with the request queued still
// leaves the mark for another server's sweep. A failed mark still queues.
func TestNotifySignalsChangedMarksStaleThenQueues(t *testing.T) {
	w, marker := newRefreshTestWorker()
	w.NotifySignalsChanged(t.Context(), 7, "p")
	if marker.calls != 1 || marker.queuedBefore[0] != 0 {
		t.Fatalf("marks = %d, refreshes queued before the mark = %v; want one mark before any refresh", marker.calls, marker.queuedBefore)
	}
	if req := takeQueued(t, w); req.userID != 7 || req.profileID != "p" {
		t.Fatalf("queued %+v", req)
	}

	marker.err = errors.New("database down")
	w.NotifySignalsChanged(t.Context(), 7, "p")
	if marker.calls != 2 {
		t.Fatalf("marks = %d, want 2", marker.calls)
	}
	takeQueued(t, w)

	for _, bad := range []struct {
		userID    int
		profileID string
	}{{0, "p"}, {7, ""}} {
		w.NotifySignalsChanged(t.Context(), bad.userID, bad.profileID)
	}
	if marker.calls != 2 {
		t.Fatalf("marks for invalid profiles = %d, want none", marker.calls-2)
	}
	assertNothingQueued(t, w, "invalid profiles")

	// Callers hold a nil *Worker when recommendations are disabled.
	var disabled *Worker
	disabled.NotifySignalsChanged(t.Context(), 7, "p")
	disabled.RequestReadRefresh(t.Context(), 7, "p")
}

// A read refresh the full queue dropped does not count against the
// profile's allowance: it left no stale mark, so the next read asks again.
func TestDroppedReadRefreshCanBeAskedAgain(t *testing.T) {
	w, _ := newRefreshTestWorker()
	ctx := t.Context()
	w.RequestProfileRefresh(ctx, 7, "other") // fills the one-slot queue
	w.RequestReadRefresh(ctx, 7, "p")
	if !w.ReadRefreshDue(7, "p") {
		t.Fatal("a dropped read refresh still counts against the profile")
	}
	takeQueued(t, w)
	w.RequestReadRefresh(ctx, 7, "p")
	if req := takeQueued(t, w); req.profileID != "p" {
		t.Fatalf("queued %+v, want profile p", req)
	}
}

// A refresh that fails marks the profile stale again, with a live context
// even when the refresh's own context ended, so the stale sweep retries it.
func TestRefreshProfileMarksStaleAgainWhenItFails(t *testing.T) {
	w, marker := newRefreshTestWorker()
	marker.w = nil
	w.engine = closedPoolEngine(t)
	if err := w.refreshProfile(t.Context(), nil, 7, "p"); err == nil {
		t.Fatal("refresh against a closed database succeeded")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := w.refreshProfile(ctx, nil, 7, "p"); err == nil {
		t.Fatal("refresh with an ended context succeeded")
	}
	if marker.calls != 2 || marker.ctxErrs[0] != nil || marker.ctxErrs[1] != nil {
		t.Fatalf("marks = %d with context errors %v; want two marks with live contexts", marker.calls, marker.ctxErrs)
	}
}

// The catch-up pass runs under the embeddings job's claim: it skips while an
// embedding run holds the job on this server or another.
func TestEmbedMissingSkipsWhileTheEmbeddingsJobIsClaimed(t *testing.T) {
	// The worker has no engine, so a pass that ran would panic.
	history := newFakeHistory()
	here := newJobTestWorker(&fakeLocker{}, history)
	if !here.tryStart(JobEmbeddings) {
		t.Fatal("could not claim the embeddings job")
	}
	here.runEmbedMissing()
	if !here.IsRunning(JobEmbeddings) {
		t.Fatal("the skipped pass released the running job's claim")
	}

	locker := &fakeLocker{held: true}
	elsewhere := newJobTestWorker(locker, history)
	elsewhere.runEmbedMissing()
	if keys, _ := locker.snapshot(); len(keys) != 1 || keys[0] != embeddingsJobLock {
		t.Fatalf("catch-up locks = %#x, want the embeddings job's lock", keys)
	}
	if elsewhere.IsRunning(JobEmbeddings) || history.count() != 0 {
		t.Fatalf("skipped pass: running=%v recorded=%d", elsewhere.IsRunning(JobEmbeddings), history.count())
	}
}

// A delete that fails leaves an empty global row serving its old titles, so
// it counts as a failure and the run is not recorded as clean.
func TestCacheGlobalRowsCountsFailedDeletes(t *testing.T) {
	w := newJobTestWorker(&fakeLocker{}, nil)
	store := &fakeGlobalRows{deleteErr: errors.New("db down")}
	// Every build succeeds; the delete of the genres that left the menu
	// fails.
	if written, failed := w.cacheGlobalRows(t.Context(), store, cacheExpiry(time.Now())); written != 4 || failed != 1 {
		t.Fatalf("written = %d, failed = %d; want 4 and the failed delete", written, failed)
	}
}

// A signal change already committed is recorded even when the request that
// made it has ended: the stale mark and the refresh do not use its context.
func TestNotifySignalsChangedOutlivesTheRequest(t *testing.T) {
	w, marker := newRefreshTestWorker()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w.NotifySignalsChanged(ctx, 7, "p")
	if marker.calls != 1 || marker.ctxErrs[0] != nil {
		t.Fatalf("marks = %d, context errors = %v; want one mark with a live context", marker.calls, marker.ctxErrs)
	}
	if req := takeQueued(t, w); req.profileID != "p" {
		t.Fatalf("queued %+v", req)
	}
}

// A profile another server is refreshing is not rebuilt here: the refresh
// reads nothing, marks the profile stale for the sweep, and releases its
// pending key. Concurrent refreshes could otherwise finish out of order and
// leave the older one's rows.
func TestRefreshProfileDefersToAnotherServersRefresh(t *testing.T) {
	w, marker := newRefreshTestWorker()
	marker.w = nil
	locker := &fakeLocker{held: true}
	w.locker = locker
	// The engine has no repo, so a refresh that went ahead would panic.

	if err := w.refreshProfile(t.Context(), nil, 7, "p"); !errors.Is(err, errProfileRefreshElsewhere) {
		t.Fatalf("refresh while another server holds the lock = %v, want errProfileRefreshElsewhere", err)
	}
	if marker.calls != 1 {
		t.Fatalf("stale marks = %d, want 1 so the sweep runs the profile again", marker.calls)
	}
	if !slices.Equal(locker.keys, []int64{profileRefreshLock(7, "p")}) {
		t.Fatalf("locks tried %#x, want only the profile's refresh lock", locker.keys)
	}

	key := profileRefreshKey(7, "p")
	if !w.claimProfileRefresh(key) {
		t.Fatal("claim failed")
	}
	_ = w.runProfileRefresh(t.Context(), nil, 7, "p", w.refreshProfile)
	if !w.claimProfileRefresh(key) {
		t.Fatal("a deferred refresh kept its pending key")
	}
	if profileRefreshLock(7, "p") == profileRefreshLock(7, "q") || profileRefreshLock(7, "p") == profileRefreshLock(8, "p") {
		t.Fatal("different profiles share a refresh lock")
	}
}
