package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/requests"
)

type downloadRefresherStub struct {
	result requests.DownloadRefreshResult
	err    error
	runs   atomic.Int32
	limit  int
	budget time.Duration

	hasWork    bool
	hasWorkErr error
}

func (s *downloadRefresherStub) RefreshDownloads(_ context.Context, limit int, budget time.Duration) (requests.DownloadRefreshResult, error) {
	s.runs.Add(1)
	s.limit, s.budget = limit, budget
	return s.result, s.err
}

func (s *downloadRefresherStub) HasDownloadsToRefresh(context.Context) (bool, error) {
	return s.hasWork, s.hasWorkErr
}

// reconcilerStub counts reconcile passes. With proceed set, a pass reports
// on entered and then blocks until proceed closes.
type reconcilerStub struct {
	runs    atomic.Int32
	entered chan struct{}
	proceed chan struct{}
}

func (s *reconcilerStub) ReconcileRequests(ctx context.Context, _ int) (requests.ReconcileResult, error) {
	s.runs.Add(1)
	if s.proceed != nil {
		s.entered <- struct{}{}
		select {
		case <-s.proceed:
		case <-ctx.Done():
			return requests.ReconcileResult{}, ctx.Err()
		}
	}
	return requests.ReconcileResult{}, nil
}

// fakeWaitingLock stands in for a lock taken with a bounded wait.
type fakeWaitingLock struct {
	acquired bool
	err      error
	waits    int
	released int
}

func (f *fakeWaitingLock) Acquire(context.Context) (func(), bool, error) {
	f.waits++
	if f.err != nil || !f.acquired {
		return nil, false, f.err
	}
	return func() { f.released++ }, true, nil
}

func TestRefreshRequestDownloadsTaskRunsUnderTheLock(t *testing.T) {
	refresher := &downloadRefresherStub{result: requests.DownloadRefreshResult{Checked: 3, Updated: 1}}
	lock := &fakeClusterLock{acquired: true}
	task := NewRefreshRequestDownloadsTask(refresher, 200, nil)
	task.lock = lock
	progress := &bulkEnrichmentTaskProgress{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if refresher.runs.Load() != 1 || refresher.limit != 200 || lock.released != 1 {
		t.Fatalf("runs = %d, limit = %d, lock releases = %d; want 1, 200 and 1", refresher.runs.Load(), refresher.limit, lock.released)
	}
	if refresher.budget != requestDownloadRefreshBudget {
		t.Fatalf("budget = %s, want %s", refresher.budget, requestDownloadRefreshBudget)
	}
	var result requests.DownloadRefreshResult
	if err := json.Unmarshal(progress.result, &result); err != nil || result.Checked != 3 || result.Updated != 1 {
		t.Fatalf("result data %s: %v", progress.result, err)
	}
}

func TestRefreshRequestDownloadsTaskSkipsWhileTheWriteLockIsHeld(t *testing.T) {
	refresher := &downloadRefresherStub{}
	task := NewRefreshRequestDownloadsTask(refresher, 200, nil)
	task.lock = &fakeClusterLock{acquired: false}

	if err := task.Execute(context.Background(), &bulkEnrichmentTaskProgress{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if refresher.runs.Load() != 0 {
		t.Fatalf("runs = %d, want 0 while the lock is held", refresher.runs.Load())
	}
}

func TestRefreshRequestDownloadsTaskReportsFailure(t *testing.T) {
	task := NewRefreshRequestDownloadsTask(&downloadRefresherStub{err: errors.New("database unavailable")}, 0, nil)
	if err := task.Execute(context.Background(), &bulkEnrichmentTaskProgress{}); err == nil {
		t.Fatal("Execute() error = nil, want the pass's error")
	}
}

// Scheduled runs happen only while some downloading target has progress, so
// an idle server records no run every minute.
func TestRefreshRequestDownloadsTaskShouldRun(t *testing.T) {
	ctx := context.Background()
	if run, err := NewRefreshRequestDownloadsTask(&downloadRefresherStub{}, 0, nil).ShouldRun(ctx); err != nil || run {
		t.Fatalf("ShouldRun() without work = %v, %v; want false", run, err)
	}
	if run, err := NewRefreshRequestDownloadsTask(&downloadRefresherStub{hasWork: true}, 0, nil).ShouldRun(ctx); err != nil || !run {
		t.Fatalf("ShouldRun() with work = %v, %v; want true", run, err)
	}
	failure := errors.New("database unavailable")
	if _, err := NewRefreshRequestDownloadsTask(&downloadRefresherStub{hasWorkErr: failure}, 0, nil).ShouldRun(ctx); !errors.Is(err, failure) {
		t.Fatalf("ShouldRun() err = %v, want the check's", err)
	}
	if run, err := NewRefreshRequestDownloadsTask(nil, 0, nil).ShouldRun(ctx); err != nil || run {
		t.Fatalf("ShouldRun() without a refresher = %v, %v; want false", run, err)
	}
}

// Reconcile keeps its own lock as the one-pass-at-a-time guard and waits for
// the target write lock; the refresh tries only the write lock. The pool never
// connects here.
func TestRequestTaskLockKeys(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://silo@127.0.0.1:1/silo?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	refresh, _ := NewRefreshRequestDownloadsTask(&downloadRefresherStub{}, 0, pool).lock.(advisoryClusterLock)
	reconcileTask := NewReconcileRequestsTask(nil, 0, pool)
	reconcile, _ := reconcileTask.lock.(advisoryClusterLock)
	writes, _ := reconcileTask.writeLock.(advisoryClusterLock)
	if refresh.key != requestTargetWriteAdvisoryLock {
		t.Fatalf("refresh lock key = %#x, want the target write lock %#x", refresh.key, requestTargetWriteAdvisoryLock)
	}
	if reconcile.key != requestReconcileAdvisoryLock {
		t.Fatalf("reconcile lock key = %#x, want %#x", reconcile.key, requestReconcileAdvisoryLock)
	}
	if writes.key != requestTargetWriteAdvisoryLock || writes.wait != requestTargetWriteWait {
		t.Fatalf("reconcile write lock = %#x waiting %s, want %#x waiting %s", writes.key, writes.wait, requestTargetWriteAdvisoryLock, requestTargetWriteWait)
	}
}

// A refresh pass must end inside the reconcile pass's wait for the write lock
// even when a download server stops answering, or a reconcile tick that lands
// during it fails. The budget cuts the call in flight, so the pass ends at the
// budget plus the writes that apply the last answer; the margin covers those.
func TestRequestDownloadRefreshBudgetEndsInsideTheReconcileWait(t *testing.T) {
	const writeMargin = 30 * time.Second
	if requestDownloadRefreshBudget <= 0 || requestDownloadRefreshBudget+writeMargin > requestTargetWriteWait {
		t.Fatalf("refresh budget %s leaves under %s of the reconcile's %s wait", requestDownloadRefreshBudget, writeMargin, requestTargetWriteWait)
	}
}

func TestReconcileRequestsTaskWaitsForTheWriteLock(t *testing.T) {
	reconciler := &reconcilerStub{}
	task := NewReconcileRequestsTask(reconciler, 0, nil)
	lock := &fakeClusterLock{acquired: true}
	writes := &fakeWaitingLock{acquired: true}
	task.lock, task.writeLock = lock, writes

	if err := task.Execute(context.Background(), &bulkEnrichmentTaskProgress{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if reconciler.runs.Load() != 1 || writes.waits != 1 || writes.released != 1 || lock.released != 1 {
		t.Fatalf("runs = %d, write lock waits = %d and releases = %d, reconcile lock releases = %d; want 1 each",
			reconciler.runs.Load(), writes.waits, writes.released, lock.released)
	}
}

func TestReconcileRequestsTaskSkipsWhileAnotherReconcileRuns(t *testing.T) {
	reconciler := &reconcilerStub{}
	task := NewReconcileRequestsTask(reconciler, 0, nil)
	writes := &fakeWaitingLock{acquired: true}
	task.lock, task.writeLock = &fakeClusterLock{acquired: false}, writes

	if err := task.Execute(context.Background(), &bulkEnrichmentTaskProgress{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if reconciler.runs.Load() != 0 || writes.waits != 0 {
		t.Fatalf("runs = %d, write lock waits = %d; want neither while another reconcile runs", reconciler.runs.Load(), writes.waits)
	}
}

// A refresh that holds the write lock past the wait fails the pass loudly
// instead of skipping it quietly.
func TestReconcileRequestsTaskFailsWhenTheWaitRunsOut(t *testing.T) {
	reconciler := &reconcilerStub{}
	task := NewReconcileRequestsTask(reconciler, 0, nil)
	lock := &fakeClusterLock{acquired: true}
	task.lock, task.writeLock = lock, &fakeWaitingLock{acquired: false}

	if err := task.Execute(context.Background(), &bulkEnrichmentTaskProgress{}); err == nil {
		t.Fatal("Execute() error = nil, want the wait to fail the pass")
	}
	if reconciler.runs.Load() != 0 || lock.released != 1 {
		t.Fatalf("runs = %d, reconcile lock releases = %d; want 0 and 1", reconciler.runs.Load(), lock.released)
	}
}

func requestLockTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// waitForLockWaiter returns once a session is queued for advisory lock key,
// failing the test if done fires first.
func waitForLockWaiter(t *testing.T, pool *pgxpool.Pool, key int64, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_locks
				WHERE locktype = 'advisory' AND NOT granted
					AND ((classid::bigint << 32) | objid::bigint) = $1
			)`, key).Scan(&waiting); err != nil {
			t.Fatalf("inspect pg_locks: %v", err)
		}
		if waiting {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("Execute() returned %v before queueing for the lock", err)
		case <-ctx.Done():
			t.Fatal("nothing queued for the lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A reconcile tick that lands while a download refresh holds the target write
// lock waits for it and then runs.
func TestReconcileWaitsForADownloadRefreshPostgres(t *testing.T) {
	pool := requestLockTestPool(t)
	reconciler := &reconcilerStub{}
	task := NewReconcileRequestsTask(reconciler, 0, pool)

	refresh, acquired, err := pglock.TryAcquire(t.Context(), pool, requestTargetWriteAdvisoryLock)
	if err != nil || !acquired {
		t.Fatalf("take the target write lock: acquired = %v, err = %v", acquired, err)
	}
	done := make(chan error, 1)
	go func() { done <- task.Execute(t.Context(), &bulkEnrichmentTaskProgress{}) }()
	waitForLockWaiter(t, pool, requestTargetWriteAdvisoryLock, done)
	if runs := reconciler.runs.Load(); runs != 0 {
		t.Fatalf("runs = %d while the refresh held the write lock, want 0", runs)
	}
	if err := refresh.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Execute() after the refresh released the lock: %v", err)
	}
	if runs := reconciler.runs.Load(); runs != 1 {
		t.Fatalf("runs = %d, want 1 once the refresh finished", runs)
	}
}

// Reconcile stays one pass at a time across the cluster.
func TestReconcileSkipsWhileAnotherReconcileRunsPostgres(t *testing.T) {
	pool := requestLockTestPool(t)
	reconciler := &reconcilerStub{}
	task := NewReconcileRequestsTask(reconciler, 0, pool)

	other, acquired, err := pglock.TryAcquire(t.Context(), pool, requestReconcileAdvisoryLock)
	if err != nil || !acquired {
		t.Fatalf("take the reconcile lock: acquired = %v, err = %v", acquired, err)
	}
	defer func() { _ = other.Release(t.Context()) }()
	if err := task.Execute(t.Context(), &bulkEnrichmentTaskProgress{}); err != nil {
		t.Fatalf("Execute() while another reconcile runs: %v", err)
	}
	if runs := reconciler.runs.Load(); runs != 0 {
		t.Fatalf("runs = %d while another reconcile ran, want 0", runs)
	}
}

// A refresh tick skips while a reconcile pass runs, and runs once it ends.
func TestRefreshRequestDownloadsSkipsDuringReconcilePostgres(t *testing.T) {
	pool := requestLockTestPool(t)
	reconciler := &reconcilerStub{entered: make(chan struct{}, 1), proceed: make(chan struct{})}
	reconcile := NewReconcileRequestsTask(reconciler, 0, pool)
	refresher := &downloadRefresherStub{}
	refresh := NewRefreshRequestDownloadsTask(refresher, 200, pool)

	done := make(chan error, 1)
	go func() { done <- reconcile.Execute(t.Context(), &bulkEnrichmentTaskProgress{}) }()
	select {
	case <-reconciler.entered:
	case err := <-done:
		t.Fatalf("reconcile returned %v before its pass started", err)
	}
	if err := refresh.Execute(t.Context(), &bulkEnrichmentTaskProgress{}); err != nil {
		t.Fatalf("Execute() while reconcile runs: %v", err)
	}
	if runs := refresher.runs.Load(); runs != 0 {
		t.Fatalf("refresh runs = %d while reconcile ran, want 0", runs)
	}
	close(reconciler.proceed)
	if err := <-done; err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if err := refresh.Execute(t.Context(), &bulkEnrichmentTaskProgress{}); err != nil {
		t.Fatalf("Execute() after reconcile: %v", err)
	}
	if runs := refresher.runs.Load(); runs != 1 {
		t.Fatalf("refresh runs = %d after reconcile ended, want 1", runs)
	}
}
