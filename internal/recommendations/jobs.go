package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/Silo-Server/silo-server/internal/workmetrics"
)

// Every API node runs a Worker, so each job also takes a cluster-wide
// advisory lock: one node runs it while the others skip. The keys spell their
// names in ASCII, like the repository's other advisory lock keys.
const (
	embeddingsJobLock    int64 = 0x53494C4F52454D42 // "SILOREMB"
	tasteProfilesJobLock int64 = 0x53494C4F52545354 // "SILORTST"
	cowatchJobLock       int64 = 0x53494C4F52435754 // "SILORCWT"
	cacheJobLock         int64 = 0x53494C4F52434143 // "SILORCAC"
	// staleSweepLock lets one node sweep stale profiles at a time.
	staleSweepLock int64 = 0x53494C4F5253544C // "SILORSTL"
)

var jobLockKeys = map[JobName]int64{
	JobEmbeddings:      embeddingsJobLock,
	JobTasteProfiles:   tasteProfilesJobLock,
	JobCowatch:         cowatchJobLock,
	JobRecommendations: cacheJobLock,
}

// Each job's runs are rows in the task manager's execution history under
// these keys. No task is registered under them, so the admin task list does
// not show them; history retention prunes them like any other key.
const (
	embeddingsTaskKey    = "recommendations.embeddings"
	tasteProfilesTaskKey = "recommendations.taste_profiles"
	cowatchTaskKey       = "recommendations.cowatch"
	cacheTaskKey         = "recommendations.cache"
)

var jobTaskKeys = map[JobName]string{
	JobEmbeddings:      embeddingsTaskKey,
	JobTasteProfiles:   tasteProfilesTaskKey,
	JobCowatch:         cowatchTaskKey,
	JobRecommendations: cacheTaskKey,
}

// Partial runs do part of a job under its claim between full runs: the
// embedding catch-up pass and the startup build of the global rows. They are
// recorded under keys of their own, so a frequent partial run never hides the
// outcome of the last full one.
const (
	embeddingsCatchUpTaskKey = "recommendations.embeddings.catch_up"
	globalRowsTaskKey        = "recommendations.cache.global_rows"
)

// staleSweepTaskKey records stale sweeps that refreshed or failed at
// least one profile.
const staleSweepTaskKey = "recommendations.stale_sweep"

var partialJobTaskKeys = map[JobName]string{
	JobEmbeddings:      embeddingsCatchUpTaskKey,
	JobRecommendations: globalRowsTaskKey,
}

// Run statuses, from the task manager's execution history vocabulary.
const (
	runStatusCompleted = "completed"
	runStatusFailed    = "failed"
)

const (
	// jobLockTimeout bounds taking a job's cluster lock, or checking its
	// session, not the job.
	jobLockTimeout = 10 * time.Second
	// jobHistoryTimeout bounds recording a run, which happens after the job's
	// own deadline may have passed.
	jobHistoryTimeout = 10 * time.Second
	// cacheTTL outlives the daily cache job by two hours, so the rows one run
	// writes stay readable until the next run has rebuilt them.
	cacheTTL = 26 * time.Hour
)

// ErrJobRunning reports a job that is already running on this server.
var ErrJobRunning = errors.New("job is already running")

// ErrJobRunningElsewhere reports a job whose cluster lock another server holds.
var ErrJobRunningElsewhere = errors.New("job is already running on another server")

// cacheExpiry is the expires_at for cache rows written by work that started at
// start.
func cacheExpiry(start time.Time) string {
	return start.Add(cacheTTL).Format(time.RFC3339)
}

// jobLocker takes a cluster-wide lock without waiting. acquired is false when
// another server holds it.
type jobLocker interface {
	TryLock(ctx context.Context, key int64) (lock clusterLock, acquired bool, err error)
}

// clusterLock is a cluster-wide lock this server holds. Each lock pins one
// database connection, so work guarded by a second lock while it holds one
// (a job taking each profile's lock, an embeddings reset taking several jobs')
// takes it with TryAlso, on the same connection.
type clusterLock interface {
	// TryAlso takes key on the lock's own session without waiting.
	// acquired is false when another server holds key. release unlocks key
	// alone; Release unlocks it too when release was not called.
	TryAlso(ctx context.Context, key int64) (release func(), acquired bool, err error)
	// Check returns errJobLockLost once the lock's session has closed, which
	// released every key the lock held.
	Check(ctx context.Context) error
	// Release unlocks every key the lock holds.
	Release()
}

type pgJobLocker struct{ pool *pgxpool.Pool }

func (l pgJobLocker) TryLock(ctx context.Context, key int64) (clusterLock, bool, error) {
	lock, acquired, err := pglock.TryAcquire(ctx, l.pool, key)
	if err != nil || !acquired {
		return nil, false, err
	}
	return pgClusterLock{lock: lock, key: key}, true, nil
}

type pgClusterLock struct {
	lock *pglock.Lock
	key  int64
}

// errJobLockLost reports that the session holding a job's cluster lock
// closed, which released the lock: another server may start the job, so the
// run stops.
var errJobLockLost = errors.New("recommendation job lost its cluster lock")

func (l pgClusterLock) TryAlso(ctx context.Context, key int64) (func(), bool, error) {
	acquired, err := l.lock.AcquireAlso(ctx, key, 0)
	if err != nil && !l.lock.Held() {
		return nil, false, fmt.Errorf("%w: %w", errJobLockLost, err)
	}
	if err != nil || !acquired {
		return nil, false, err
	}
	return func() {
		releaseCtx := context.WithoutCancel(ctx)
		if err := l.lock.ReleaseAlso(releaseCtx, key); err != nil {
			slog.WarnContext(releaseCtx, "releasing recommendation lock failed", "component", "recommendations", "lock", key, "error", err)
		}
	}, true, nil
}

// Check gives the probe jobLockTimeout, not the job's deadline: a session
// whose connection stopped answering would otherwise hold the job, and its
// claim, until the deadline.
func (l pgClusterLock) Check(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, jobLockTimeout)
	defer cancel()
	if err := l.lock.Check(checkCtx); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: %w", errJobLockLost, err)
	}
	return nil
}

func (l pgClusterLock) Release() {
	if err := l.lock.Release(context.Background()); err != nil {
		slog.Warn("releasing recommendation job lock failed", "component", "recommendations", "lock", l.key, "error", err)
	}
}

// JobHistory stores one row per finished job run. The task manager's
// execution repository satisfies it.
type JobHistory interface {
	Insert(ctx context.Context, result taskmanager.ExecutionResult) error
	GetLatest(ctx context.Context, taskKey string) (*taskmanager.ExecutionResult, error)
}

// WithJobHistory records every finished job run in history and returns the
// worker. Without it runs are only logged.
func (w *Worker) WithJobHistory(history JobHistory) *Worker {
	if w != nil {
		w.history = history
	}
	return w
}

// JobRuns holds a job's newest recorded full and partial runs. Each is nil
// until one is recorded.
type JobRuns struct {
	Full    *taskmanager.ExecutionResult
	Partial *taskmanager.ExecutionResult
}

// LastRuns returns the newest recorded runs of each job. Jobs with no
// recorded run are absent, and the map is empty when no history is wired.
func (w *Worker) LastRuns(ctx context.Context) (map[JobName]JobRuns, error) {
	runs := make(map[JobName]JobRuns, len(jobTaskKeys))
	if w == nil || w.history == nil {
		return runs, nil
	}
	for name, key := range jobTaskKeys {
		var r JobRuns
		var err error
		if r.Full, err = w.history.GetLatest(ctx, key); err != nil {
			return nil, fmt.Errorf("read last %s run: %w", jobLabel(name), err)
		}
		if partialKey, ok := partialJobTaskKeys[name]; ok {
			if r.Partial, err = w.history.GetLatest(ctx, partialKey); err != nil {
				return nil, fmt.Errorf("read last partial %s run: %w", jobLabel(name), err)
			}
		}
		if r.Full != nil || r.Partial != nil {
			runs[name] = r
		}
	}
	return runs, nil
}

// StaleSweepStatus is the stale sweep's backlog and its newest recorded run.
type StaleSweepStatus struct {
	// Pending counts the profiles marked stale and not yet refreshed.
	Pending int
	// LastRun is nil until a sweep refreshes or fails a profile.
	LastRun *taskmanager.ExecutionResult
}

// StaleSweep reports the stale sweep's backlog and its newest recorded run.
func (w *Worker) StaleSweep(ctx context.Context) (StaleSweepStatus, error) {
	var status StaleSweepStatus
	var err error
	if status.Pending, err = w.engine.repo.StaleProfileCount(ctx); err != nil {
		return status, err
	}
	if w.history != nil {
		if status.LastRun, err = w.history.GetLatest(ctx, staleSweepTaskKey); err != nil {
			return status, fmt.Errorf("read last stale sweep: %w", err)
		}
	}
	return status, nil
}

// jobResult is the summary a job body returns. It is logged and stored as the
// run's result data.
type jobResult interface {
	// failures counts the parts of the run that failed without stopping it.
	failures() int
}

// idleResult is a jobResult that can tell when its run found nothing to do.
type idleResult interface {
	jobResult
	idle() bool
}

type job struct {
	name    JobName
	timeout time.Duration
	// run does the work. held is the job's cluster lock, through which
	// the work takes any further lock.
	run func(ctx context.Context, held clusterLock) (jobResult, error)
	// partial marks a run that does part of the job between full runs; it
	// is recorded under the job's partial task key.
	partial bool
	// quietWhenIdle logs the job at Debug and leaves a run out of history
	// when it succeeds and its result reports nothing done. Frequent
	// catch-up runs use it so history holds only runs that did something.
	quietWhenIdle bool
}

// isIdleRun reports a successful run whose result says it did nothing.
func isIdleRun(result jobResult, err error) bool {
	idle, ok := result.(idleResult)
	return err == nil && ok && idle.idle()
}

func jobLabel(name JobName) string {
	return strings.ReplaceAll(string(name), "_", " ")
}

// runJob claims j for this process and for the cluster, then runs it: in a new
// goroutine when background is set (manual triggers), otherwise in the
// caller's goroutine (cron). The claim happens before runJob returns either
// way, so a caller learns about a busy job through ErrJobRunning or
// ErrJobRunningElsewhere. Runs skipped that way are not recorded.
func (w *Worker) runJob(j job, background bool) error {
	held, release, err := w.claimJobs(j.name)
	if err != nil {
		return err
	}
	if background {
		go func() {
			defer release()
			w.executeJob(j, held)
		}()
		return nil
	}
	defer release()
	w.executeJob(j, held)
	return nil
}

// runScheduled runs a cron job, logging instead of returning a busy job.
func (w *Worker) runScheduled(name JobName) {
	err := w.runJob(w.jobFor(name), false)
	switch {
	case err == nil:
	case errors.Is(err, ErrJobRunningElsewhere):
		slog.Info("recommendation job is running on another server; skipping scheduled run", "component", "recommendations", "job", name)
	case errors.Is(err, ErrJobRunning):
		slog.Warn("recommendation job already running; skipping scheduled run", "component", "recommendations", "job", name)
	default:
		slog.Error("recommendation job could not start", "component", "recommendations", "job", name, "error", err)
	}
}

// claimJobs claims the named jobs for this process and for the cluster and
// returns the first job's cluster lock, which holds the others' too, and one
// function that releases them all. When a claim fails it releases the ones it
// took.
func (w *Worker) claimJobs(names ...JobName) (held clusterLock, release func(), err error) {
	var releases []func()
	release = func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobLockTimeout)
	defer cancel()
	for _, name := range names {
		if !w.tryStart(name) {
			release()
			return nil, nil, fmt.Errorf("%s %w", jobLabel(name), ErrJobRunning)
		}
		releases = append(releases, func() { w.setRunning(name, false) })
		var unlock func()
		var acquired bool
		if held == nil {
			held, acquired, err = w.locker.TryLock(ctx, jobLockKeys[name])
			if acquired {
				unlock = held.Release
			}
		} else {
			unlock, acquired, err = held.TryAlso(ctx, jobLockKeys[name])
		}
		if err != nil {
			release()
			return nil, nil, fmt.Errorf("take %s job lock: %w", jobLabel(name), err)
		}
		if !acquired {
			release()
			return nil, nil, fmt.Errorf("%s %w", jobLabel(name), ErrJobRunningElsewhere)
		}
		releases = append(releases, unlock)
	}
	return held, release, nil
}

func (w *Worker) executeJob(j job, held clusterLock) {
	ctx, cancel := context.WithTimeout(context.Background(), j.timeout)
	defer cancel()
	ctx, observation := workmetrics.Start(ctx, "recommendations", time.Time{})
	defer workmetrics.Profile(ctx)()

	startLevel := slog.LevelInfo
	if j.quietWhenIdle {
		startLevel = slog.LevelDebug
	}
	slog.Log(ctx, startLevel, "recommendation job started", "component", "recommendations", "job", j.name, "timeout", j.timeout)
	started := time.Now()
	result, err := j.run(ctx, held)
	completed := time.Now()
	observation.Finish(telemetry.Outcome(err))

	var resultData json.RawMessage
	if result != nil {
		data, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			slog.WarnContext(ctx, "encoding recommendation job result failed", "component", "recommendations", "job", j.name, "error", marshalErr)
		} else {
			resultData = data
		}
	}

	attrs := []any{"component", "recommendations", "job", j.name, "duration", completed.Sub(started), "result", string(resultData)}
	switch {
	case j.quietWhenIdle && isIdleRun(result, err):
		slog.DebugContext(ctx, "recommendation job found nothing to do", attrs...)
		return
	case err != nil:
		slog.ErrorContext(ctx, "recommendation job failed", append(attrs, "error", err)...)
	case result != nil && result.failures() > 0:
		slog.WarnContext(ctx, "recommendation job completed with errors", attrs...)
	default:
		slog.InfoContext(ctx, "recommendation job completed", attrs...)
	}

	taskKey := jobTaskKeys[j.name]
	if j.partial {
		taskKey = partialJobTaskKeys[j.name]
	}
	w.recordRun(taskKey, started, completed, resultData, err)
}

// recordRun stores one finished run under taskKey.
func (w *Worker) recordRun(taskKey string, started, completed time.Time, resultData json.RawMessage, runErr error) {
	if w.history == nil {
		return
	}
	run := taskmanager.ExecutionResult{
		TaskKey:     taskKey,
		StartedAt:   started,
		CompletedAt: completed,
		Status:      runStatusCompleted,
		ResultData:  resultData,
		DurationMs:  completed.Sub(started).Milliseconds(),
	}
	if runErr != nil {
		run.Status = runStatusFailed
		run.ErrorMessage = runErr.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobHistoryTimeout)
	defer cancel()
	if err := w.history.Insert(ctx, run); err != nil {
		slog.WarnContext(ctx, "recording recommendation job run failed", "component", "recommendations", "task_key", taskKey, "error", err)
	}
}
