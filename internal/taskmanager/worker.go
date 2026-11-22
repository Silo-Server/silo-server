package taskmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/Silo-Server/silo-server/internal/workmetrics"
)

// taskWorker wraps a Task with in-memory runtime state and trigger management.
type taskWorker struct {
	task            Task
	manager         *TaskManager
	state           TaskState
	progress        float64
	progressMessage string
	lastStarted     time.Time
	lastCompleted   time.Time
	lastResult      *ExecutionResult
	triggers        []Trigger
	cancel          context.CancelFunc
	// triggerUpdate wakes the trigger loop after setTriggers replaced the
	// triggers. triggerGen counts those replacements, so the loop can tell a
	// firing of the triggers it waits on from one of triggers since replaced.
	triggerUpdate chan struct{}
	triggerGen    uint64
	mu            sync.RWMutex
	scheduleMu    sync.Mutex
	// schedule is the normalized configs setTriggers last installed,
	// including any the trigger factory could not build, so ReloadSchedule
	// can tell whether the saved schedule differs from the one the worker
	// runs.
	schedule []TriggerConfig
	// retired is closed when the task is unregistered; it ends the trigger
	// loop, setTriggers no longer starts triggers and reserve refuses runs.
	retired chan struct{}
}

func newTaskWorker(task Task, manager *TaskManager) *taskWorker {
	return &taskWorker{
		task:          task,
		manager:       manager,
		state:         TaskStateIdle,
		triggerUpdate: make(chan struct{}, 1),
		retired:       make(chan struct{}),
	}
}

// isRetired reports whether retire ran.
func (w *taskWorker) isRetired() bool {
	select {
	case <-w.retired:
		return true
	default:
		return false
	}
}

// generation returns triggerGen.
func (w *taskWorker) generation() uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.triggerGen
}

// retire stops a worker its manager no longer holds: its triggers stop, its
// trigger loop ends, and a run in progress is canceled and recorded as such.
func (w *taskWorker) retire() {
	w.scheduleMu.Lock()
	w.mu.Lock()
	if w.isRetired() {
		w.mu.Unlock()
		w.scheduleMu.Unlock()
		return
	}
	close(w.retired)
	for _, tr := range w.triggers {
		tr.Stop()
	}
	w.triggers = nil
	cancel := w.cancel
	if w.state == TaskStateRunning {
		w.state = TaskStateCancelling
	}
	w.mu.Unlock()
	w.scheduleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Task lists refetch on task.updated, so they drop the task now
	// rather than at their next refresh.
	w.notify()
}

// info returns a snapshot of the worker's current state as a TaskInfo.
func (w *taskWorker) info() TaskInfo {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.infoLocked()
}

func (w *taskWorker) infoLocked() TaskInfo {
	info := TaskInfo{
		Key:             w.task.Key(),
		Name:            w.task.Name(),
		Description:     w.task.Description(),
		Category:        w.task.Category(),
		State:           w.state,
		Progress:        w.progress,
		ProgressMessage: w.progressMessage,
		LastExecution:   w.lastResult,
	}
	if w.state != TaskStateIdle && !w.lastStarted.IsZero() {
		started := w.lastStarted
		info.StartedAt = &started
	}
	info.ManualOnly = isManualOnly(w.task)

	var earliest time.Time
	for _, tr := range w.triggers {
		info.Triggers = append(info.Triggers, tr.Config())
		next := tr.NextRunTime()
		if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
			earliest = next
		}
	}
	if info.Triggers == nil {
		info.Triggers = []TriggerConfig{}
	}
	if !earliest.IsZero() {
		info.NextRunAt = &earliest
	}

	return info
}

// setTriggers replaces active triggers. A running task arms its new triggers
// when it finishes, so an edit cannot spend a startup firing mid-run.
// Pass lastResult to resume scheduling from the last execution, or nil to
// start the interval fresh from now (e.g. when the user edits the schedule).
func (w *taskWorker) setTriggers(configs []TriggerConfig, factory func(TriggerConfig) Trigger, lastResult *ExecutionResult, notify bool) {
	w.mu.Lock()

	for _, tr := range w.triggers {
		tr.Stop()
	}

	w.triggers = nil
	w.triggerGen++
	configs = normalizedTriggers(configs)
	w.schedule = configs
	if w.isRetired() {
		configs = nil
	}
	spread := startSpread(w.task)
	for _, cfg := range configs {
		tr := factory(cfg)
		if tr != nil {
			if spreadable, ok := tr.(SpreadableTrigger); ok && spread > 0 {
				spreadable.SetStartSpread(spread)
			}
			if w.state == TaskStateIdle && !w.manager.stopped.Load() {
				tr.Start(lastResult)
			}
			w.triggers = append(w.triggers, tr)
		}
	}

	w.mu.Unlock()

	if notify {
		select {
		case w.triggerUpdate <- struct{}{}:
		default:
		}
	}
}

// stopTriggers stops all active triggers.
func (w *taskWorker) stopTriggers() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, tr := range w.triggers {
		tr.Stop()
	}
}

// progressReporter implements ProgressReporter, writing into the worker's state.
type progressReporter struct {
	worker     *taskWorker
	resultData json.RawMessage
}

func (p *progressReporter) Report(percent float64, message string) {
	workmetrics.Progress(p.worker.task.Key())
	p.worker.mu.Lock()
	p.worker.progress = percent
	p.worker.progressMessage = message
	p.worker.mu.Unlock()
	p.worker.notify()
}

func (p *progressReporter) SetResultData(data json.RawMessage) {
	p.resultData = data
}

// maxRuntimeError is the cause of a run stopped at its trigger's max runtime.
// Its text is the run's recorded error_message, which admins see.
type maxRuntimeError struct{ limit time.Duration }

func (e maxRuntimeError) Error() string {
	return fmt.Sprintf("Stopped after reaching the max runtime of %s", e.limit)
}

// runsSchedule reports whether configs is the schedule the worker runs.
func (w *taskWorker) runsSchedule(configs []TriggerConfig) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return slices.Equal(w.schedule, normalizedTriggers(configs))
}

// normalizedTriggers copies configs with each one Normalized, so a schedule
// read back from the store, which keeps day_of_week only on weekly triggers,
// compares equal to the one installed from a binding or an edit.
func normalizedTriggers(configs []TriggerConfig) []TriggerConfig {
	out := make([]TriggerConfig, len(configs))
	for i, cfg := range configs {
		out[i] = cfg.Normalized()
	}
	return out
}

// reserve claims the local worker before an asynchronous caller acknowledges it.
// The caller holds scheduleMu, fencing schedule changes and retirement.
// A positive maxRuntime stops the run when it elapses, the way a cancel does.
// A retired worker answers ErrTaskNotFound: a caller that looked the worker up,
// or a trigger firing consumed, before Unregister must not start a run after it.
func (w *taskWorker) reserve(ctx context.Context, maxRuntime time.Duration) (context.Context, context.CancelFunc, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.isRetired() || w.manager.stopped.Load() {
		return nil, nil, ErrTaskNotFound
	}
	if w.state == TaskStateRunning || w.state == TaskStateCancelling {
		return nil, nil, ErrTaskAlreadyRunning
	}
	w.state = TaskStateRunning
	w.progress = 0
	w.progressMessage = ""
	w.lastStarted = time.Now()
	var execCtx context.Context
	var cancel context.CancelFunc
	if maxRuntime > 0 {
		execCtx, cancel = context.WithTimeoutCause(ctx, maxRuntime, maxRuntimeError{limit: maxRuntime})
	} else {
		execCtx, cancel = context.WithCancel(ctx)
	}
	w.cancel = cancel
	return execCtx, cancel, nil
}

// run executes the task. Returns ErrTaskAlreadyRunning if already running.
func (w *taskWorker) run(ctx context.Context, maxRuntime time.Duration) (*ExecutionResult, error) {
	w.scheduleMu.Lock()
	execCtx, cancel, err := w.reserve(ctx, maxRuntime)
	w.scheduleMu.Unlock()
	if err != nil {
		return nil, err
	}
	return w.executeReserved(execCtx, cancel), nil
}

func (w *taskWorker) executeReserved(execCtx context.Context, cancel context.CancelFunc) *ExecutionResult {
	w.notify()
	defer cancel()

	execCtx, observation := workmetrics.Start(execCtx, w.task.Key(), time.Time{})
	defer observation.Finish("unknown")
	reporter := &progressReporter{worker: w}
	startedAt := time.Now()
	var err error
	workmetrics.Do(execCtx, func(ctx context.Context) { err = w.task.Execute(ctx, reporter) })
	completedAt := time.Now()
	// Stop the max runtime timer now, so it can't relabel a run that already
	// returned while this goroutine waits for the worker lock.
	cancel()
	var limitErr maxRuntimeError
	limitReached := errors.As(context.Cause(execCtx), &limitErr)

	result := &ExecutionResult{
		TaskKey:     w.task.Key(),
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		DurationMs:  completedAt.Sub(startedAt).Milliseconds(),
		ResultData:  reporter.resultData,
	}

	metricOutcome := telemetry.Outcome(err)
	if limitReached && err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		// The run is recorded as stopped at its limit; keep the task's own error.
		slog.WarnContext(execCtx, "task returned an error after reaching its max runtime", "task", w.task.Key(), "error", err)
	}
	w.mu.Lock()
	switch {
	case limitReached:
		// Checked before TaskStateCancelling: an admin who cancels a run already past
		// its limit still sees why it stopped.
		result.Status = "cancelled" //nolint:misspell // Existing task state vocabulary.
		result.ErrorMessage = limitErr.Error()
		metricOutcome = "canceled"
	case w.state == TaskStateCancelling:
		result.Status = "cancelled"
		metricOutcome = "canceled"
	case err != nil:
		result.Status = "failed"
		result.ErrorMessage = err.Error()
	default:
		result.Status = "completed"
	}

	w.cancel = nil
	w.state = TaskStateIdle
	w.lastCompleted = completedAt
	w.lastResult = result
	w.progress = 0
	w.progressMessage = ""
	w.rearmTriggersLocked(result)
	w.mu.Unlock()
	w.notify()
	w.manager.removeRetiredWorker(w)
	observation.Finish(metricOutcome)

	return result
}

func (w *taskWorker) rearmTriggersLocked(lastResult *ExecutionResult) {
	if w.manager.stopped.Load() {
		return
	}
	for _, tr := range w.triggers {
		tr.Stop()
		tr.Start(lastResult)
	}
}

// requestCancel marks the worker as canceling and returns its snapshot.
func (w *taskWorker) requestCancel() (TaskInfo, error) {
	w.mu.Lock()
	if w.state != TaskStateRunning {
		w.mu.Unlock()
		return TaskInfo{}, ErrTaskNotRunning
	}
	w.state = TaskStateCancelling
	cancel := w.cancel
	info := w.infoLocked()
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	slog.Info("task cancel requested", "task", w.task.Key())
	w.notify()
	return info, nil
}

func (w *taskWorker) notify() {
	if w == nil {
		return
	}
	w.notifyInfo(w.info())
}

func (w *taskWorker) notifyInfo(info TaskInfo) {
	if w == nil || w.manager == nil {
		return
	}
	w.manager.notifyTaskUpdated(info)
}
