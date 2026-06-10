// Package jobrunner owns the lifecycle mechanics shared by Silo's AI job
// services (subtitle translation/ASR, metadata translation): bounded dispatch
// off a semaphore shared across services, a heartbeat loop that keeps a row
// alive even while the job is queued, a stale-job reaper for rows orphaned by
// a crashed worker, and a per-job cancel registry. The services keep their own
// job tables, validation, and run logic; this package keeps them honest about
// concurrency and crash recovery without copy-pasting the trickiest code.
package jobrunner

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// A running job refreshes its heartbeat every HeartbeatInterval; one whose
	// heartbeat has not advanced for StaleJobThreshold is treated as orphaned
	// by a crashed worker and reaped. The margin over HeartbeatInterval avoids
	// reaping a job that is merely mid–LLM-call.
	HeartbeatInterval = 30 * time.Second
	StaleJobThreshold = 2 * time.Minute
	// How often the background reaper scans for orphaned jobs.
	ReaperInterval = time.Minute
)

// Store is the minimal persistence surface the runner needs. Both AI job
// repositories satisfy it.
type Store interface {
	Heartbeat(ctx context.Context, id int64) error
	// ResetStaleJobs marks pending/running jobs whose heartbeat predates
	// `before` as failed with the given message. Returns rows reset.
	ResetStaleJobs(ctx context.Context, before time.Time, message string) (int64, error)
}

// NewSemaphore builds the dispatch semaphore shared across runners, so the
// configured endpoint sees one global bound regardless of job mix. size <= 0
// falls back to 2.
func NewSemaphore(size int) chan struct{} {
	if size <= 0 {
		size = 2
	}
	return make(chan struct{}, size)
}

// Runner executes jobs with bounded concurrency, heartbeats, cancellation,
// and crash recovery. One Runner per job table; the semaphore may be shared
// across Runners.
type Runner struct {
	// baseCtx is the application context; dispatched jobs and the reaper
	// derive from it so they stop when the server shuts down.
	baseCtx context.Context
	sem     chan struct{}
	store   Store
	// label names the job family in log lines ("subtitle ai", "metadata translation").
	label  string
	logger *slog.Logger

	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
	wg      sync.WaitGroup
}

// New wires a runner. A nil appCtx falls back to context.Background(); a nil
// sem gets a private default-size semaphore (used by tests; production passes
// the shared one).
func New(appCtx context.Context, sem chan struct{}, store Store, label string, logger *slog.Logger) *Runner {
	if appCtx == nil {
		appCtx = context.Background()
	}
	if sem == nil {
		sem = NewSemaphore(0)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		baseCtx: appCtx,
		sem:     sem,
		store:   store,
		label:   label,
		logger:  logger,
		cancels: make(map[int64]context.CancelFunc),
	}
}

// Recover clears jobs orphaned by a crashed worker and starts a background
// reaper that keeps doing so. Reaping is heartbeat-based (not "every active
// job"), so it is safe when multiple instances share one database: a job
// still being heartbeat-updated by a live worker is never reset. Call once at
// startup; jobs and the reaper derive from the application context, so they
// stop on shutdown.
func (r *Runner) Recover() {
	r.reapStaleJobs()
	go r.reaperLoop()
}

func (r *Runner) reaperLoop() {
	ticker := time.NewTicker(ReaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.baseCtx.Done():
			return
		case <-ticker.C:
			r.reapStaleJobs()
		}
	}
}

func (r *Runner) reapStaleJobs() {
	before := time.Now().Add(-StaleJobThreshold)
	n, err := r.store.ResetStaleJobs(context.WithoutCancel(r.baseCtx), before, "interrupted by server restart")
	if err != nil {
		r.logger.Warn("failed to reset stale jobs", "jobs", r.label, "error", err)
		return
	}
	if n > 0 {
		r.logger.Info("reset stale jobs", "jobs", r.label, "count", n)
	}
}

// Dispatch launches a bounded background goroutine for job id. run executes
// once a semaphore slot is acquired; onAbort runs instead if the job is
// cancelled (user cancel or shutdown) while still waiting for a slot. Both
// receive a context derived from the application context that is cancelled by
// Cancel(id) or server shutdown.
func (r *Runner) Dispatch(id int64, run func(ctx context.Context), onAbort func(ctx context.Context)) {
	runCtx, cancel := context.WithCancel(r.baseCtx)
	r.mu.Lock()
	r.cancels[id] = cancel
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.cancels, id)
			r.mu.Unlock()
			cancel()
		}()

		// Heartbeat for the whole lifetime — crucially including while queued
		// behind the semaphore — so the stale-job reaper never reaps a job that
		// is alive but merely waiting for a slot (which would otherwise mark it
		// failed and let it resurrect itself on acquire, or admit a duplicate).
		stopHeartbeat := make(chan struct{})
		defer close(stopHeartbeat)
		go r.heartbeatLoop(runCtx, id, stopHeartbeat)

		select {
		case r.sem <- struct{}{}:
		case <-runCtx.Done():
			if onAbort != nil {
				onAbort(context.WithoutCancel(runCtx))
			}
			return
		}
		defer func() { <-r.sem }()

		run(runCtx)
	}()
}

// Cancel cancels the in-flight goroutine for id, returning false when none is
// registered (job on another node, or never dispatched) so the caller can
// fall back to a best-effort terminal transition in the database.
func (r *Runner) Cancel(id int64) bool {
	r.mu.Lock()
	cancel := r.cancels[id]
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// heartbeatLoop keeps a job's heartbeat_at fresh until the job ends or the
// context is cancelled, so the stale-job reaper only ever reaps jobs orphaned
// by a crashed worker.
func (r *Runner) heartbeatLoop(ctx context.Context, jobID int64, stop <-chan struct{}) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.store.Heartbeat(context.WithoutCancel(ctx), jobID)
		}
	}
}
