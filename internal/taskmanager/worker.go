package taskmanager

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
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
	triggerUpdate   chan struct{}
	triggerChanged  atomic.Bool
	mu              sync.RWMutex
}

func newTaskWorker(task Task, manager *TaskManager) *taskWorker {
	return &taskWorker{
		task:          task,
		manager:       manager,
		state:         TaskStateIdle,
		triggerUpdate: make(chan struct{}, 1),
	}
}

// info returns a snapshot of the worker's current state as a TaskInfo.
func (w *taskWorker) info() TaskInfo {
	w.mu.RLock()
	defer w.mu.RUnlock()

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

// setTriggers replaces active triggers. Stops old triggers, starts new ones.
// Pass lastResult to resume scheduling from the last execution, or nil to
// start the interval fresh from now (e.g. when the user edits the schedule).
func (w *taskWorker) setTriggers(configs []TriggerConfig, factory func(TriggerConfig) Trigger, lastResult *ExecutionResult, notify bool) {
	w.mu.Lock()

	for _, tr := range w.triggers {
		tr.Stop()
	}

	w.triggers = nil
	for _, cfg := range configs {
		tr := factory(cfg)
		if tr != nil {
			tr.Start(lastResult)
			w.triggers = append(w.triggers, tr)
		}
	}

	w.mu.Unlock()

	if !notify {
		w.notify()
		return
	}

	w.triggerChanged.Store(true)
	select {
	case w.triggerUpdate <- struct{}{}:
	default:
	}

	w.notify()
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
	p.worker.mu.Lock()
	p.worker.progress = percent
	p.worker.progressMessage = message
	p.worker.mu.Unlock()
	p.worker.notify()
}

func (p *progressReporter) SetResultData(data json.RawMessage) {
	p.resultData = data
}

// run executes the task. Returns ErrTaskAlreadyRunning if already running.
func (w *taskWorker) run(ctx context.Context) (*ExecutionResult, error) {
	w.mu.Lock()
	if w.state == TaskStateRunning || w.state == TaskStateCancelling {
		w.mu.Unlock()
		return nil, ErrTaskAlreadyRunning
	}
	w.state = TaskStateRunning
	w.progress = 0
	w.progressMessage = ""
	w.lastStarted = time.Now()

	execCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.mu.Unlock()
	w.notify()

	defer func() {
		cancel()
		w.mu.Lock()
		w.cancel = nil
		w.mu.Unlock()
	}()

	reporter := &progressReporter{worker: w}
	startedAt := time.Now()
	err := w.task.Execute(execCtx, reporter)
	completedAt := time.Now()

	result := &ExecutionResult{
		TaskKey:     w.task.Key(),
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		DurationMs:  completedAt.Sub(startedAt).Milliseconds(),
		ResultData:  reporter.resultData,
	}

	w.mu.Lock()
	switch {
	case w.state == TaskStateCancelling:
		result.Status = "cancelled"
	case err != nil:
		result.Status = "failed"
		result.ErrorMessage = err.Error()
	default:
		result.Status = "completed"
	}

	w.state = TaskStateIdle
	w.lastCompleted = completedAt
	w.lastResult = result
	w.progress = 0
	w.progressMessage = ""
	w.mu.Unlock()
	w.notify()

	return result, nil
}

// requestCancel sets state to Cancelling and calls the cancel func.
func (w *taskWorker) requestCancel() error {
	w.mu.Lock()
	if w.state != TaskStateRunning {
		w.mu.Unlock()
		return ErrTaskNotRunning
	}
	w.state = TaskStateCancelling
	cancel := w.cancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	slog.Info("task cancel requested", "task", w.task.Key())
	w.notify()
	return nil
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
