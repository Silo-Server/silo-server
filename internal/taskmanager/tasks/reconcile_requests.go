package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/jackc/pgx/v5/pgxpool"
)

// requestReconcileAdvisoryLock spells "SILORQRC". It lets one server run each
// reconcile pass.
const requestReconcileAdvisoryLock int64 = 0x53494C4F52515243

// requestTargetWriteAdvisoryLock spells "SILORQTW". It keeps the reconcile pass
// and the download refresh pass from writing request targets at the same time:
// reconcile waits for it, and the refresh skips while it is held.
const requestTargetWriteAdvisoryLock int64 = 0x53494C4F52515457

// requestTargetWriteWait bounds how long a reconcile pass waits for a download
// refresh pass to finish. A refresh pass asks about at most 200 requests and
// normally ends well inside a minute; requestDownloadRefreshBudget cuts it off
// before this wait runs out, even while a download server stops answering.
const requestTargetWriteWait = 2 * time.Minute

// requestDownloadRefreshBudget bounds a download refresh pass. A plugin call
// may take the router's full 60-second deadline, and one server that stops
// answering can hang every call on it, so without a bound a pass could hold
// the target write lock past requestTargetWriteWait and fail the reconcile
// pass waiting for it. The margin covers the writes that apply the last
// answer after the budget cuts the call in flight.
const requestDownloadRefreshBudget = 90 * time.Second

type RequestReconciler interface {
	ReconcileRequests(ctx context.Context, limit int) (requests.ReconcileResult, error)
}

// ReconcileRequestsTask moves in-flight media requests forward. Every API
// process runs the task manager, so an advisory lock lets one server run each
// pass; the others skip. The per-request submission claim already prevents a
// double submission, so the lock only saves the duplicate router status calls
// and presence lookups. The pass then waits for the request target write lock,
// so it runs after a download refresh pass in progress instead of skipping.
type ReconcileRequestsTask struct {
	reconciler RequestReconciler
	limit      int
	locks      reconcileLocker
}

// reconcileLockOutcome is how taking the reconcile pass's locks went.
type reconcileLockOutcome int

const (
	reconcileLocksHeld reconcileLockOutcome = iota
	// reconcileLocksBusy: another server's reconcile pass is running.
	reconcileLocksBusy
	// reconcileLocksWaitedOut: a download refresh pass held the request target
	// write lock past requestTargetWriteWait.
	reconcileLocksWaitedOut
)

// reconcileLocker takes both of the reconcile pass's locks. release is set
// only when outcome is reconcileLocksHeld.
type reconcileLocker interface {
	Acquire(ctx context.Context) (release func(), outcome reconcileLockOutcome, err error)
}

// requestReconcileLocks takes requestReconcileAdvisoryLock without waiting,
// then requestTargetWriteAdvisoryLock with a wait of requestTargetWriteWait,
// both on one database session. One session leaves the rest of the pool to
// the pass itself: a session per lock would take both connections of a
// two-connection pool and leave the pass none.
type requestReconcileLocks struct {
	pool *pgxpool.Pool
}

func (l requestReconcileLocks) Acquire(ctx context.Context) (func(), reconcileLockOutcome, error) {
	lock, acquired, err := pglock.TryAcquire(ctx, l.pool, requestReconcileAdvisoryLock)
	if err != nil {
		return nil, reconcileLocksBusy, fmt.Errorf("acquiring request reconcile lock: %w", err)
	}
	if !acquired {
		return nil, reconcileLocksBusy, nil
	}
	release := func() {
		if err := lock.Release(ctx); err != nil {
			slog.WarnContext(ctx, "releasing request reconcile locks failed", "component", "taskmanager", "error", err)
		}
	}
	held, err := lock.AcquireAlso(ctx, requestTargetWriteAdvisoryLock, requestTargetWriteWait)
	if err != nil || !held {
		release()
		if err != nil {
			return nil, reconcileLocksWaitedOut, fmt.Errorf("acquiring request target write lock: %w", err)
		}
		return nil, reconcileLocksWaitedOut, nil
	}
	return release, reconcileLocksHeld, nil
}

// NewReconcileRequestsTask constructs the task. A nil pool runs without the
// cluster lock.
func NewReconcileRequestsTask(reconciler RequestReconciler, limit int, pool *pgxpool.Pool) *ReconcileRequestsTask {
	if limit <= 0 {
		limit = 100
	}
	t := &ReconcileRequestsTask{reconciler: reconciler, limit: limit}
	if pool != nil {
		t.locks = requestReconcileLocks{pool: pool}
	}
	return t
}

func (t *ReconcileRequestsTask) Key() string  { return "reconcile_requests" }
func (t *ReconcileRequestsTask) Name() string { return "Reconcile Requests" }
func (t *ReconcileRequestsTask) Description() string {
	return "Checks approved and active media requests against Radarr, Sonarr, and the Silo catalog"
}
func (t *ReconcileRequestsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *ReconcileRequestsTask) IsHidden() bool { return true }

func (t *ReconcileRequestsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 5 * 60 * 1000},
	}
}

func (t *ReconcileRequestsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Reconciling media requests")
	if t.reconciler == nil {
		progress.Report(100, "Request reconciliation unavailable")
		return nil
	}
	if t.locks != nil {
		release, outcome, err := t.locks.Acquire(ctx)
		if err != nil {
			return err
		}
		switch outcome {
		case reconcileLocksBusy:
			progress.Report(100, "Another server is reconciling media requests")
			return nil
		case reconcileLocksWaitedOut:
			return fmt.Errorf("request download refresh held the request target write lock for over %s", requestTargetWriteWait)
		}
		defer release()
	}
	result, err := t.reconciler.ReconcileRequests(ctx, t.limit)
	if err != nil {
		return fmt.Errorf("reconcile media requests: %w", err)
	}
	if data, err := json.Marshal(result); err == nil {
		progress.SetResultData(data)
	}
	progress.Report(100, "Request reconciliation complete")
	return nil
}
