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

// Run statuses, from the task manager's execution history vocabulary.
const (
	runStatusCompleted = "completed"
	runStatusFailed    = "failed"
)

const (
	// jobLockTimeout bounds taking a job's cluster lock, not the job.
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
	TryLock(ctx context.Context, key int64) (release func(), acquired bool, err error)
}

type pgJobLocker struct{ pool *pgxpool.Pool }

func (l pgJobLocker) TryLock(ctx context.Context, key int64) (func(), bool, error) {
	lock, acquired, err := pglock.TryAcquire(ctx, l.pool, key)
	if err != nil || !acquired {
		return nil, false, err
	}
	return func() {
		if err := lock.Release(context.Background()); err != nil {
			slog.WarnContext(ctx, "releasing recommendation job lock failed", "component", "recommendations", "lock", key, "error", err)
		}
	}, true, nil
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

// LastRuns returns the newest recorded run of each job. Jobs with no recorded
// run are absent, and the map is empty when no history is wired.
func (w *Worker) LastRuns(ctx context.Context) (map[JobName]taskmanager.ExecutionResult, error) {
	runs := make(map[JobName]taskmanager.ExecutionResult, len(jobTaskKeys))
	if w == nil || w.history == nil {
		return runs, nil
	}
	for name, key := range jobTaskKeys {
		latest, err := w.history.GetLatest(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("read last %s run: %w", jobLabel(name), err)
		}
		if latest != nil {
			runs[name] = *latest
		}
	}
	return runs, nil
}

// jobResult is the summary a job body returns. It is logged and stored as the
// run's result data.
type jobResult interface {
	// failures counts the parts of the run that failed without stopping it.
	failures() int
}

type job struct {
	name    JobName
	timeout time.Duration
	run     func(ctx context.Context) (jobResult, error)
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
	release, err := w.claimJob(j.name)
	if err != nil {
		return err
	}
	if background {
		go func() {
			defer release()
			w.executeJob(j)
		}()
		return nil
	}
	defer release()
	w.executeJob(j)
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

func (w *Worker) claimJob(name JobName) (release func(), err error) {
	if !w.tryStart(name) {
		return nil, fmt.Errorf("%s %w", jobLabel(name), ErrJobRunning)
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobLockTimeout)
	defer cancel()
	unlock, acquired, err := w.locker.TryLock(ctx, jobLockKeys[name])
	if err != nil {
		w.setRunning(name, false)
		return nil, fmt.Errorf("take %s job lock: %w", jobLabel(name), err)
	}
	if !acquired {
		w.setRunning(name, false)
		return nil, fmt.Errorf("%s %w", jobLabel(name), ErrJobRunningElsewhere)
	}
	return func() {
		unlock()
		w.setRunning(name, false)
	}, nil
}

func (w *Worker) executeJob(j job) {
	ctx, cancel := context.WithTimeout(context.Background(), j.timeout)
	defer cancel()
	ctx, observation := workmetrics.Start(ctx, "recommendations", time.Time{})
	defer workmetrics.Profile(ctx)()

	slog.InfoContext(ctx, "recommendation job started", "component", "recommendations", "job", j.name, "timeout", j.timeout)
	started := time.Now()
	result, err := j.run(ctx)
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
	case err != nil:
		slog.ErrorContext(ctx, "recommendation job failed", append(attrs, "error", err)...)
	case result != nil && result.failures() > 0:
		slog.WarnContext(ctx, "recommendation job completed with errors", attrs...)
	default:
		slog.InfoContext(ctx, "recommendation job completed", attrs...)
	}

	w.recordJob(j.name, started, completed, resultData, err)
}

func (w *Worker) recordJob(name JobName, started, completed time.Time, resultData json.RawMessage, runErr error) {
	if w.history == nil {
		return
	}
	run := taskmanager.ExecutionResult{
		TaskKey:     jobTaskKeys[name],
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
		slog.WarnContext(ctx, "recording recommendation job run failed", "component", "recommendations", "job", name, "error", err)
	}
}
