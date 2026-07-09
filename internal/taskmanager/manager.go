package taskmanager

import (
	"context"
	"log/slog"
	"sort"
	"sync"
)

// TriggerFactory is a function that creates a live Trigger from a TriggerConfig.
type TriggerFactory func(TriggerConfig) Trigger

// TaskManager is the central orchestrator for background tasks.
type TaskManager struct {
	tasks          map[string]*taskWorker
	mu             sync.RWMutex
	triggerRepo    TriggerRepository
	historyRepo    ExecutionRepository
	triggerFactory TriggerFactory
	logger         *slog.Logger
	observers      []Observer
}

// New creates a new TaskManager.
func New(triggerRepo TriggerRepository, historyRepo ExecutionRepository, triggerFactory TriggerFactory, logger *slog.Logger) *TaskManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &TaskManager{
		tasks:          make(map[string]*taskWorker),
		triggerRepo:    triggerRepo,
		historyRepo:    historyRepo,
		triggerFactory: triggerFactory,
		logger:         logger,
	}
}

func (m *TaskManager) AddObserver(observer Observer) {
	if m == nil || observer == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.observers = append(m.observers, observer)
}

// Register adds a task to the manager. Must be called before Start.
func (m *TaskManager) Register(task Task) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[task.Key()] = newTaskWorker(task, m)
}

// Start loads triggers from the repository and begins all scheduling loops.
func (m *TaskManager) Start(ctx context.Context) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for key, w := range m.tasks {
		if latest, err := m.historyRepo.GetLatest(ctx, key); err == nil && latest != nil {
			w.mu.Lock()
			w.lastResult = latest
			w.mu.Unlock()
		}

		configs, err := m.triggerRepo.GetTriggers(ctx, key)
		if err != nil {
			m.logger.ErrorContext(ctx, "failed to load triggers", "task", key, "error", err)
		}
		if len(configs) == 0 {
			configs = w.task.DefaultTriggers()
			if len(configs) > 0 {
				if err := m.triggerRepo.SetTriggers(ctx, key, configs); err != nil {
					m.logger.ErrorContext(ctx, "failed to persist default triggers", "task", key, "error", err)
				}
			}
		}

		w.setTriggers(configs, m.triggerFactory, w.lastResult, false)

		go m.triggerLoop(ctx, w)
	}

	m.logger.InfoContext(ctx, "task manager started", "tasks", len(m.tasks))
}

// triggerLoop listens on all trigger channels for a worker and runs the task
// when any trigger fires.
func (m *TaskManager) triggerLoop(ctx context.Context, w *taskWorker) {
	for {
		w.mu.RLock()
		trigs := w.triggers
		w.mu.RUnlock()

		if len(trigs) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-w.triggerUpdate:
				continue
			}
		}

		merged := make(chan struct{}, 1)
		done := make(chan struct{})

		for _, tr := range trigs {
			tr := tr
			go func() {
				select {
				case <-done:
					return
				case _, ok := <-tr.C():
					if ok {
						select {
						case merged <- struct{}{}:
						default:
						}
					}
				}
			}()
		}

		go func() {
			select {
			case <-done:
				return
			case <-w.triggerUpdate:
				select {
				case merged <- struct{}{}:
				default:
				}
			}
		}()

		select {
		case <-ctx.Done():
			close(done)
			return
		case <-merged:
			close(done)
		}

		if w.triggerChanged.CompareAndSwap(true, false) {
			continue
		}

		shouldRun, shouldRunErr := m.shouldRunScheduledTask(ctx, w)
		if shouldRunErr != nil {
			// Fail closed: a preflight that cannot answer must not launch the
			// task — for expensive conditional tasks a transient error would
			// otherwise trigger the exact work the gate exists to suppress.
			// Interval/daily triggers retry at their next firing; manual
			// RunTask always bypasses the gate.
			m.logger.WarnContext(ctx, "scheduled task preflight failed; skipping run",
				"task", w.task.Key(), "error", shouldRunErr)
			m.rearmTriggersFromNow(w)
			continue
		}
		if !shouldRun {
			m.rearmTriggersFromNow(w)
			continue
		}

		result, err := w.run(ctx)
		if err != nil {
			// If the task is already running (e.g. via manual RunTask), don't
			// rearm — the concurrent runner will rearm when it finishes.
			// For other errors, rearm so the trigger fires again later.
			if err != ErrTaskAlreadyRunning {
				m.rearmTriggers(w)
			}
			continue
		}

		if result != nil {
			if insertErr := m.historyRepo.Insert(ctx, *result); insertErr != nil {
				m.logger.ErrorContext(ctx, "failed to persist execution result",
					"task", w.task.Key(), "error", insertErr)
			}
		}

		m.rearmTriggers(w)
	}
}

func (m *TaskManager) shouldRunScheduledTask(ctx context.Context, w *taskWorker) (bool, error) {
	task, ok := w.task.(ScheduledConditionalTask)
	if !ok {
		return true, nil
	}
	return task.ShouldRun(ctx)
}

// rearmTriggers stops and restarts all triggers for a worker.
func (m *TaskManager) rearmTriggers(w *taskWorker) {
	w.mu.Lock()
	lastResult := w.lastResult
	for _, tr := range w.triggers {
		tr.Stop()
		tr.Start(lastResult)
	}
	w.mu.Unlock()
	w.notify()
}

// rearmTriggersFromNow restarts triggers after a skipped conditional task. This
// intentionally ignores lastResult; otherwise an old completed_at can make an
// interval trigger fire immediately in a loop while there is no work.
func (m *TaskManager) rearmTriggersFromNow(w *taskWorker) {
	w.mu.Lock()
	for _, tr := range w.triggers {
		tr.Stop()
		tr.Start(nil)
	}
	w.mu.Unlock()
	w.notify()
}

// Stop stops all triggers and cancels running tasks.
func (m *TaskManager) Stop() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, w := range m.tasks {
		w.stopTriggers()
		w.mu.RLock()
		if w.cancel != nil {
			w.cancel()
		}
		w.mu.RUnlock()
	}
	m.logger.Info("task manager stopped")
}

// RunTask triggers immediate execution of a task.
func (m *TaskManager) RunTask(ctx context.Context, key string) error {
	w, err := m.getWorker(key)
	if err != nil {
		return err
	}

	result, err := w.run(ctx)
	if err != nil {
		return err
	}

	if result != nil {
		if insertErr := m.historyRepo.Insert(ctx, *result); insertErr != nil {
			m.logger.ErrorContext(ctx, "failed to persist execution result", "task", key, "error", insertErr)
		}
	}

	m.rearmTriggers(w)
	return nil
}

// CancelTask requests cancellation of a running task.
func (m *TaskManager) CancelTask(key string) error {
	w, err := m.getWorker(key)
	if err != nil {
		return err
	}
	return w.requestCancel()
}

// GetTaskInfo returns the current state of a task.
func (m *TaskManager) GetTaskInfo(key string) TaskInfo {
	w, err := m.getWorker(key)
	if err != nil {
		return TaskInfo{}
	}
	return w.info()
}

// ListTasks returns info for all registered tasks, optionally including hidden ones.
func (m *TaskManager) ListTasks(includeHidden bool) []TaskInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var infos []TaskInfo
	for _, w := range m.tasks {
		if !includeHidden && w.task.IsHidden() {
			continue
		}
		infos = append(infos, w.info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Key < infos[j].Key })
	return infos
}

// UpdateTriggers replaces the triggers for a task.
func (m *TaskManager) UpdateTriggers(key string, triggerConfigs []TriggerConfig) error {
	w, err := m.getWorker(key)
	if err != nil {
		return err
	}

	if err := m.triggerRepo.SetTriggers(context.Background(), key, triggerConfigs); err != nil {
		return err
	}

	w.setTriggers(triggerConfigs, m.triggerFactory, nil, true)
	m.notifyTaskUpdated(w.info())
	return nil
}

func (m *TaskManager) notifyTaskUpdated(info TaskInfo) {
	if m == nil {
		return
	}

	m.mu.RLock()
	observers := append([]Observer(nil), m.observers...)
	m.mu.RUnlock()
	for _, observer := range observers {
		observer.TaskUpdated(info)
	}
}

func (m *TaskManager) getWorker(key string) (*taskWorker, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	w, ok := m.tasks[key]
	if !ok {
		return nil, ErrTaskNotFound
	}
	return w, nil
}
