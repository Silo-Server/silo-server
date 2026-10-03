package recommendations

import (
	"context"
	"encoding/json"
	"errors"
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

func (l *fakeLocker) TryLock(_ context.Context, key int64) (func(), bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = append(l.keys, key)
	if l.err != nil || l.held {
		return nil, false, l.err
	}
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.released++
	}, true, nil
}

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

func TestRunJobRefusesASecondRunInThisProcess(t *testing.T) {
	locker := &fakeLocker{}
	history := newFakeHistory()
	w := newJobTestWorker(locker, history)

	started := make(chan struct{})
	finish := make(chan struct{})
	blocking := job{name: JobCowatch, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		close(started)
		<-finish
		return countsResult{Done: 1}, nil
	}}
	// A synchronous run returns only after releasing its claim, so the test
	// can tell when the job is free again.
	firstDone := make(chan error, 1)
	go func() { firstDone <- w.runJob(blocking, false) }()
	select {
	case <-started:
	case err := <-firstDone:
		t.Fatalf("first run did not start: %v", err)
	}
	if !w.IsRunning(JobCowatch) {
		t.Fatal("running job not reported as running")
	}

	ran := false
	second := job{name: JobCowatch, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		ran = true
		return countsResult{}, nil
	}}
	err := w.runJob(second, true)
	if !errors.Is(err, ErrJobRunning) || errors.Is(err, ErrJobRunningElsewhere) {
		t.Fatalf("second run error = %v, want ErrJobRunning", err)
	}
	if err.Error() != "cowatch job is already running" {
		t.Fatalf("second run message = %q", err.Error())
	}
	if keys, _ := locker.snapshot(); len(keys) != 1 || keys[0] != cowatchJobLock {
		t.Fatalf("lock requests = %#x, want one for the co-watch lock", keys)
	}

	close(finish)
	if err := <-firstDone; err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := w.runJob(second, false); err != nil {
		t.Fatalf("run after the first finished: %v", err)
	}
	if !ran {
		t.Fatal("second job did not run after the first finished")
	}
	if _, released := locker.snapshot(); released != 2 {
		t.Fatalf("locks released = %d, want 2", released)
	}
}

func TestRunJobSkipsWhenAnotherServerHoldsTheLock(t *testing.T) {
	locker := &fakeLocker{held: true}
	history := newFakeHistory()
	w := newJobTestWorker(locker, history)

	ran := false
	err := w.runJob(job{name: JobRecommendations, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		ran = true
		return cacheResult{}, nil
	}}, true)
	if !errors.Is(err, ErrJobRunningElsewhere) {
		t.Fatalf("error = %v, want ErrJobRunningElsewhere", err)
	}
	if err.Error() != "recommendations job is already running on another server" {
		t.Fatalf("message = %q", err.Error())
	}
	if ran || w.IsRunning(JobRecommendations) || history.count() != 0 {
		t.Fatalf("skipped job ran=%v running=%v recorded=%d", ran, w.IsRunning(JobRecommendations), history.count())
	}
	if keys, _ := locker.snapshot(); len(keys) != 1 || keys[0] != cacheJobLock {
		t.Fatalf("lock requests = %#x, want one for the cache lock", keys)
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

func TestRunJobRecordsOutcome(t *testing.T) {
	history := newFakeHistory()
	w := newJobTestWorker(&fakeLocker{}, history)

	if err := w.runJob(job{name: JobRecommendations, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		return countsResult{Done: 3, Failed: 1}, nil
	}}, false); err != nil {
		t.Fatal(err)
	}
	run := waitRun(t, history)
	if run.TaskKey != cacheTaskKey || run.Status != runStatusCompleted || run.ErrorMessage != "" {
		t.Fatalf("completed run = %+v", run)
	}
	if string(run.ResultData) != `{"done":3,"failed":1}` || run.CompletedAt.Before(run.StartedAt) || run.DurationMs < 0 {
		t.Fatalf("completed run data = %s %+v", run.ResultData, run)
	}

	if err := w.runJob(job{name: JobEmbeddings, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		return countsResult{Done: 2}, errors.New("provider unavailable")
	}}, false); err != nil {
		t.Fatal(err)
	}
	run = waitRun(t, history)
	if run.TaskKey != embeddingsTaskKey || run.Status != runStatusFailed || run.ErrorMessage != "provider unavailable" || string(run.ResultData) != `{"done":2,"failed":0}` {
		t.Fatalf("failed run = %+v %s", run, run.ResultData)
	}

	// A background run (a manual trigger) is recorded the same way.
	if err := w.runJob(job{name: JobTasteProfiles, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		return countsResult{Done: 5}, nil
	}}, true); err != nil {
		t.Fatal(err)
	}
	run = waitRun(t, history)
	if run.TaskKey != tasteProfilesTaskKey || run.Status != runStatusCompleted || string(run.ResultData) != `{"done":5,"failed":0}` {
		t.Fatalf("background run = %+v %s", run, run.ResultData)
	}

	// Without history a run still completes.
	bare := newJobTestWorker(&fakeLocker{}, nil)
	ran := false
	if err := bare.runJob(job{name: JobCowatch, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		ran = true
		return countsResult{}, nil
	}}, false); err != nil || !ran {
		t.Fatalf("run without history: ran=%v err=%v", ran, err)
	}
}

func TestRunJobAppliesTheJobTimeout(t *testing.T) {
	history := newFakeHistory()
	w := newJobTestWorker(&fakeLocker{}, history)
	if err := w.runJob(job{name: JobCowatch, timeout: time.Millisecond, run: func(ctx context.Context) (jobResult, error) {
		<-ctx.Done()
		return countsResult{}, ctx.Err()
	}}, false); err != nil {
		t.Fatal(err)
	}
	if run := waitRun(t, history); run.Status != runStatusFailed || !strings.Contains(run.ErrorMessage, "deadline") {
		t.Fatalf("timed-out run = %+v", run)
	}
}

func TestLastRunsReadsEachJob(t *testing.T) {
	history := newFakeHistory()
	w := newJobTestWorker(&fakeLocker{}, history)
	for _, name := range []JobName{JobCowatch, JobRecommendations, JobRecommendations} {
		if err := w.runJob(job{name: name, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
			return countsResult{Done: history.count()}, nil
		}}, false); err != nil {
			t.Fatal(err)
		}
		waitRun(t, history)
	}

	runs, err := w.LastRuns(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[JobCowatch].TaskKey != cowatchTaskKey || string(runs[JobRecommendations].ResultData) != `{"done":2,"failed":0}` {
		t.Fatalf("last runs = %+v", runs)
	}

	history.err = errors.New("db down")
	if _, err := w.LastRuns(t.Context()); err == nil {
		t.Fatal("history error was dropped")
	}
	var nilWorker *Worker
	if runs, err := nilWorker.LastRuns(t.Context()); err != nil || len(runs) != 0 {
		t.Fatalf("nil worker last runs = %v, %v", runs, err)
	}
	if runs, err := newJobTestWorker(&fakeLocker{}, nil).LastRuns(t.Context()); err != nil || len(runs) != 0 {
		t.Fatalf("no-history last runs = %v, %v", runs, err)
	}
}

func TestCacheExpiryOutlivesTheDailyJob(t *testing.T) {
	start := time.Date(2026, 10, 3, 5, 0, 0, 0, time.UTC)
	got, err := time.Parse(time.RFC3339, cacheExpiry(start))
	if err != nil {
		t.Fatal(err)
	}
	if want := start.Add(26 * time.Hour); !got.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got, want)
	}
	if !got.After(start.Add(24 * time.Hour)) {
		t.Fatal("rows expire before the next daily run")
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
}

func (f *fakeGlobalRows) result(name string, items []ScoredItem) ([]ScoredItem, error) {
	if f.fail[name] {
		return nil, errors.New(name + " query failed")
	}
	return items, nil
}

func (f *fakeGlobalRows) GetPopularItems(context.Context, int, int) ([]ScoredItem, error) {
	return f.result("popular", []ScoredItem{{MediaItemID: "p"}})
}

func (f *fakeGlobalRows) GetRecentlyAddedItems(context.Context, int, int) ([]ScoredItem, error) {
	return f.result("recent", []ScoredItem{{MediaItemID: "r"}})
}

func (f *fakeGlobalRows) GetTopRatedItems(context.Context, int, int) ([]ScoredItem, error) {
	return f.result("top", nil) // no title has enough ratings
}

func (f *fakeGlobalRows) GetTopGenres(context.Context, int) ([]string, error) {
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

func (f *fakeGlobalRows) HasGlobalRecommendationCache(context.Context) (bool, error) {
	return f.cached, f.cacheErr
}

func writtenTypes(f *fakeGlobalRows) string {
	var types []string
	for _, recType := range []string{RecTypePopular, RecTypeRecentlyAdded, RecTypeTopRated, RecTypeGenreSamplerPrefix + "Drama", RecTypeGenreSamplerPrefix + "Comedy"} {
		if _, ok := f.written[recType]; ok {
			types = append(types, recType)
		}
	}
	return strings.Join(types, ",")
}

func TestCacheGlobalRowsCountsFailuresAndKeepsFailedRows(t *testing.T) {
	w := newJobTestWorker(&fakeLocker{}, nil)

	store := &fakeGlobalRows{fail: map[string]bool{"popular": true, "Comedy": true, "write:" + RecTypeGenreSamplerPrefix + "Drama": true}}
	written, failed := w.cacheGlobalRows(t.Context(), store, "expiry")
	// Popular's query, Comedy's query, and Drama's write fail, so their cached
	// rows are kept until the new expiry. Top rated is empty, which is not a
	// failure: its row is neither rewritten nor kept.
	if written != 1 || failed != 3 || writtenTypes(store) != RecTypeRecentlyAdded {
		t.Fatalf("written=%d failed=%d rows=%s", written, failed, writtenTypes(store))
	}
	wantKept := []string{RecTypePopular + "@expiry", RecTypeGenreSamplerPrefix + "Drama@expiry", RecTypeGenreSamplerPrefix + "Comedy@expiry"}
	if strings.Join(store.extended, ",") != strings.Join(wantKept, ",") {
		t.Fatalf("kept rows = %v, want %v", store.extended, wantKept)
	}

	// Without the top genres, every cached genre sampler is kept.
	store = &fakeGlobalRows{fail: map[string]bool{"genres": true}}
	written, failed = w.cacheGlobalRows(t.Context(), store, "expiry")
	if written != 2 || failed != 1 || writtenTypes(store) != RecTypePopular+","+RecTypeRecentlyAdded {
		t.Fatalf("genre failure: written=%d failed=%d rows=%s", written, failed, writtenTypes(store))
	}
	if strings.Join(store.extended, ",") != RecTypeGenreSamplerPrefix+"*@expiry" {
		t.Fatalf("genre failure kept %v, want every genre sampler", store.extended)
	}

	// A failed extend is only logged; the run goes on.
	store = &fakeGlobalRows{fail: map[string]bool{"popular": true, "recent": true}, extendErr: errors.New("db down")}
	written, failed = w.cacheGlobalRows(t.Context(), store, "expiry")
	if written != 2 || failed != 2 || len(store.extended) != 2 {
		t.Fatalf("failed extend: written=%d failed=%d extended=%v", written, failed, store.extended)
	}
}

func TestEnsureGlobalRowsBuildsOnlyWhenNoneIsCached(t *testing.T) {
	locker := &fakeLocker{}
	history := newFakeHistory()
	w := newJobTestWorker(locker, history)

	store := &fakeGlobalRows{}
	before := time.Now()
	w.ensureGlobalRows(t.Context(), store)
	if writtenTypes(store) != strings.Join([]string{RecTypePopular, RecTypeRecentlyAdded, RecTypeGenreSamplerPrefix + "Drama", RecTypeGenreSamplerPrefix + "Comedy"}, ",") {
		t.Fatalf("startup build wrote %s", writtenTypes(store))
	}
	if expires, err := time.Parse(time.RFC3339, store.expires); err != nil || expires.Before(before.Add(cacheTTL).Truncate(time.Second)) {
		t.Fatalf("startup rows expire at %q (%v)", store.expires, err)
	}
	if keys, released := locker.snapshot(); len(keys) != 1 || keys[0] != cacheJobLock || released != 1 {
		t.Fatalf("startup build locks = %#x released %d, want the cache job's lock", keys, released)
	}
	run := waitRun(t, history)
	var result cacheResult
	if err := json.Unmarshal(run.ResultData, &result); err != nil {
		t.Fatal(err)
	}
	if run.TaskKey != cacheTaskKey || run.Status != runStatusCompleted || !result.GlobalOnly || result.GlobalRows != 4 {
		t.Fatalf("startup build recorded %+v %s", run, run.ResultData)
	}

	for name, skip := range map[string]*fakeGlobalRows{
		"rows cached":  {cached: true},
		"check failed": {cacheErr: errors.New("db down")},
	} {
		w.ensureGlobalRows(t.Context(), skip)
		if len(skip.written) != 0 {
			t.Fatalf("%s: startup build wrote %s", name, writtenTypes(skip))
		}
	}

	// Another server already holds the cache job: it builds the rows, not us.
	busy := newJobTestWorker(&fakeLocker{held: true}, history)
	elsewhere := &fakeGlobalRows{}
	busy.ensureGlobalRows(t.Context(), elsewhere)
	if len(elsewhere.written) != 0 || history.count() != 1 {
		t.Fatalf("startup build ran beside another server: wrote %s, runs %d", writtenTypes(elsewhere), history.count())
	}
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

func TestCacheUserRowsCountsFailedSteps(t *testing.T) {
	engine := closedPoolEngine(t)
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = engine

	built := w.cacheUserRows(t.Context(), engine.repo, 7, "profile", cacheExpiry(time.Now()))
	if built.cached != 0 || built.failed == 0 {
		t.Fatalf("build against a failing database = %+v, want failures and nothing cached", built)
	}
}

func TestTasteProfileJobFailsWhenSubjectsCannotBeListed(t *testing.T) {
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = closedPoolEngine(t)
	res, err := w.doTasteProfiles(t.Context())
	if err == nil || res.Profiles != 0 {
		t.Fatalf("taste job = %+v, %v; want a failure", res, err)
	}
}

func TestCacheJobFailsWhenProfilesCannotBeListed(t *testing.T) {
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = closedPoolEngine(t)
	res, err := w.doRecommendations(t.Context())
	if err == nil || !res.CleanFailed || res.GlobalFailures == 0 {
		t.Fatalf("cache job = %+v, %v; want failures", res, err)
	}
	if res.failures() == 0 {
		t.Fatal("cache job failures not counted")
	}
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

func TestClaimProfileRefreshDedupesUntilCleared(t *testing.T) {
	w := newJobTestWorker(&fakeLocker{}, nil)
	key := profileRefreshKey(1, "p")
	if !w.claimProfileRefresh(key) || w.claimProfileRefresh(key) {
		t.Fatal("a pending profile refresh was claimed twice")
	}
	w.clearProfileRefreshPending(key)
	if !w.claimProfileRefresh(key) {
		t.Fatal("a cleared profile refresh could not be claimed")
	}
}
