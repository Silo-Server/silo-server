package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// TriggerFactory is a function that creates a live Trigger from a TriggerConfig.
type TriggerFactory func(TriggerConfig) Trigger

// LibraryTypesFunc lists the media_folders.type value of every library.
type LibraryTypesFunc func(ctx context.Context) ([]string, error)

// TaskManager is the central orchestrator for background tasks.
type TaskManager struct {
	tasks          map[string]*taskWorker
	mu             sync.RWMutex
	triggerRepo    TriggerRepository
	historyRepo    ExecutionRepository
	triggerFactory TriggerFactory
	logger         *slog.Logger
	observers      []Observer
	libraryTypes   LibraryTypesFunc
	// startCtx is the context Start ran under, nil before Start. A task
	// registered later runs its trigger loop under it.
	startCtx context.Context //nolint:containedctx // Start's context outlives the call: tasks registered after Start run under it
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

// SetLibraryTypes installs the lookup ListRelevantTasks uses to omit
// LibraryScopedTask tasks for library kinds this server does not have. Without
// it, every non-hidden task is listed.
func (m *TaskManager) SetLibraryTypes(fn LibraryTypesFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.libraryTypes = fn
}

// Register adds a task, replacing one registered under the same key as
// Unregister does. A task registered before Start starts with the others; one
// registered after Start loads its schedule and starts its trigger loop
// before Register returns. If that schedule cannot be read, the task is
// registered with no automatic runs until ReloadSchedule or an edit installs
// one; RegisterLive reports the failure instead.
func (m *TaskManager) Register(task Task) {
	w, ctx := m.add(task)
	if ctx == nil {
		return
	}
	if err := m.startWorker(ctx, w); err != nil {
		m.logger.ErrorContext(ctx, "failed to load triggers", "task", task.Key(), "error", err)
	}
}

// RegisterLive is Register for a caller that retries, such as the plugin
// task registry: when Start has run and the task's schedule cannot be read,
// it leaves no task under the key and returns the error.
func (m *TaskManager) RegisterLive(task Task) error {
	w, ctx := m.add(task)
	if ctx == nil {
		return nil
	}
	err := m.startWorker(ctx, w)
	if err != nil {
		m.mu.Lock()
		if m.tasks[task.Key()] == w {
			delete(m.tasks, task.Key())
		}
		m.mu.Unlock()
		w.retire()
	}
	return err
}

// add installs a worker for task, retiring the one it replaces, and returns
// Start's context, nil before Start.
func (m *TaskManager) add(task Task) (*taskWorker, context.Context) {
	w := newTaskWorker(task, m)
	m.mu.Lock()
	replaced := m.tasks[task.Key()]
	m.tasks[task.Key()] = w
	ctx := m.startCtx
	m.mu.Unlock()
	if replaced != nil {
		replaced.retire()
	}
	return w, ctx
}

// Unregister removes a task: its triggers stop, its trigger loop ends and a
// run in progress is canceled. Its saved schedule and history stay, so
// registering the key again resumes that schedule. An unknown key is ignored.
func (m *TaskManager) Unregister(key string) {
	m.mu.Lock()
	w := m.tasks[key]
	delete(m.tasks, key)
	m.mu.Unlock()
	if w != nil {
		w.retire()
	}
}

// Start loads triggers from the repository and begins all scheduling loops.
func (m *TaskManager) Start(ctx context.Context) {
	m.mu.Lock()
	m.startCtx = ctx //nolint:fatcontext // set once, by Start
	workers := make([]*taskWorker, 0, len(m.tasks))
	for _, w := range m.tasks {
		workers = append(workers, w)
	}
	m.mu.Unlock()

	for _, w := range workers {
		if err := m.startWorker(ctx, w); err != nil {
			m.logger.ErrorContext(ctx, "failed to load triggers", "task", w.task.Key(), "error", err)
		}
	}
	m.logger.InfoContext(ctx, "task manager started", "tasks", len(workers))
}

// startWorker loads a task's last result and schedule (savedSchedule) and
// starts its trigger loop. It returns a schedule read failure.
func (m *TaskManager) startWorker(ctx context.Context, w *taskWorker) error {
	key := w.task.Key()
	latest, err := m.historyRepo.GetLatest(ctx, key)
	if err == nil && latest != nil {
		w.mu.Lock()
		w.lastResult = latest
		w.mu.Unlock()
	} else {
		latest = nil
	}

	// An administrator edit made while the task registers waits, so the
	// schedule read here cannot replace it.
	w.scheduleMu.Lock()
	configs, err := m.savedSchedule(ctx, w)
	// On a storage failure configs is nil, so automatic runs stay idle. The
	// trigger loop still starts so a later administrator edit or
	// ReloadSchedule can recover the task.
	w.setTriggers(configs, m.triggerFactory, latest, false)
	w.scheduleMu.Unlock()

	go m.triggerLoop(ctx, w)
	return err
}

// savedSchedule reads a task's saved schedule. With none, it is the task's
// DefaultTriggers, saved first unless the task is a DefaultFollowingTask.
// Callers hold w.scheduleMu.
func (m *TaskManager) savedSchedule(ctx context.Context, w *taskWorker) ([]TriggerConfig, error) {
	key := w.task.Key()
	configs, err := m.triggerRepo.GetTriggers(ctx, key)
	if err == nil && configs == nil {
		if followsDefaultTriggers(w.task) {
			configs = w.task.DefaultTriggers()
		} else {
			// Default providers may read settings. Resolve them only for new
			// schedules, outside the repository transaction. Initialization
			// rechecks saved state so a concurrent edit still takes precedence.
			configs, err = m.triggerRepo.GetOrCreateTriggers(ctx, key, w.task.DefaultTriggers())
		}
	}
	if err != nil {
		return nil, err
	}
	if isManualOnly(w.task) {
		// A schedule saved before the task became manual-only must not run it.
		return nil, nil
	}
	return configs, nil
}

// ReloadSchedule installs a task's schedule (savedSchedule) when it differs
// from the one the task runs, so a schedule another process saved, or a
// DefaultFollowingTask's changed defaults, apply without a restart. Like an
// edit, a changed schedule's interval starts from now. It never writes a
// saved schedule, except to seed DefaultTriggers for a task that has none and
// does not follow them. Before Start it does nothing: Start loads every
// schedule.
func (m *TaskManager) ReloadSchedule(ctx context.Context, key string) error {
	w, err := m.getWorker(key)
	if err != nil {
		return err
	}
	m.mu.RLock()
	started := m.startCtx != nil
	m.mu.RUnlock()
	if !started {
		return nil
	}
	w.scheduleMu.Lock()
	defer w.scheduleMu.Unlock()
	configs, err := m.savedSchedule(ctx, w)
	if err != nil {
		return err
	}
	if !w.runsSchedule(configs) {
		w.setTriggers(configs, m.triggerFactory, nil, true)
	}
	return nil
}

// triggerLoop listens on all trigger channels for a worker and runs the task
// when any trigger fires.
func (m *TaskManager) triggerLoop(ctx context.Context, w *taskWorker) {
	for {
		w.mu.RLock()
		trigs := w.triggers
		gen := w.triggerGen
		w.mu.RUnlock()

		if len(trigs) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-w.retired:
				return
			case <-w.triggerUpdate:
				continue
			}
		}

		fired := make(chan struct{}, 1)
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
						case fired <- struct{}{}:
						default:
						}
					}
				}
			}()
		}

		select {
		case <-ctx.Done():
			close(done)
			return
		case <-w.retired:
			close(done)
			return
		case <-w.triggerUpdate:
			// Wait on the new triggers. An update never runs the task.
			close(done)
			continue
		case <-fired:
			close(done)
		}

		// A trigger replaced since the loop read them fired before it
		// stopped; the update that replaced it wakes the loop for the new
		// ones.
		w.mu.RLock()
		replaced := w.triggerGen != gen
		w.mu.RUnlock()
		if replaced {
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
		if errors.Is(err, ErrTaskNotFound) {
			// Unregistered while this firing waited: the worker is retired.
			return
		}
		if err != nil {
			// If the task is already running (e.g. via manual RunTask), don't
			// rearm — the concurrent runner will rearm when it finishes.
			// For other errors, rearm so the trigger fires again later.
			if !errors.Is(err, ErrTaskAlreadyRunning) {
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

// manualStartKey marks the execution context of a run started by StartTask.
type manualStartKey struct{}

// StartedManually reports whether ctx belongs to a task run started through
// StartTask (the v2 admin run and trigger operations). Runs through RunTask,
// including the trigger loop, internal kicks and the v1 admin task run route,
// do not report it. Tasks use it to skip their scheduling floors when asked to
// run now.
func StartedManually(ctx context.Context) bool {
	manual, _ := ctx.Value(manualStartKey{}).(bool)
	return manual
}

// StartTask starts work on this process after synchronously reserving its worker.
// It does not persist work intent or guarantee execution after a process failure.
// The run's context reports StartedManually.
func (m *TaskManager) StartTask(key string) (TaskInfo, error) {
	w, err := m.getWorker(key)
	if err != nil {
		return TaskInfo{}, err
	}
	ctx, cancel, err := w.reserve(context.WithValue(context.Background(), manualStartKey{}, true))
	if err != nil {
		return TaskInfo{}, err
	}
	info := w.info()
	go func() {
		result := w.executeReserved(ctx, cancel)
		if err := m.historyRepo.Insert(context.Background(), *result); err != nil {
			m.logger.Error("failed to persist execution result", "task", key, "error", err)
		}
		m.rearmTriggers(w)
	}()
	return info, nil
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
	return m.listTasks(func(w *taskWorker) bool { return includeHidden || !w.task.IsHidden() })
}

// ListRelevantTasks returns the non-hidden tasks an administrator can act on:
// it also omits library-scoped tasks that no existing library needs. If the
// library lookup fails, those tasks are listed.
func (m *TaskManager) ListRelevantTasks(ctx context.Context) []TaskInfo {
	m.mu.RLock()
	lookup := m.libraryTypes
	m.mu.RUnlock()

	scoped := lookup != nil
	var libraryTypes []string
	if scoped {
		types, err := lookup(ctx)
		if err != nil {
			m.logger.WarnContext(ctx, "listing library types for task list", "error", err)
			scoped = false
		}
		libraryTypes = types
	}
	return m.listTasks(func(w *taskWorker) bool {
		return !w.task.IsHidden() && (!scoped || servesAnyLibrary(w.task, libraryTypes))
	})
}

func (m *TaskManager) listTasks(include func(*taskWorker) bool) []TaskInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var infos []TaskInfo
	for _, w := range m.tasks {
		if include(w) {
			infos = append(infos, w.info())
		}
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Key < infos[j].Key })
	return infos
}

func isManualOnly(task Task) bool {
	manual, ok := task.(ManualOnlyTask)
	return ok && manual.ManualOnly()
}

func followsDefaultTriggers(task Task) bool {
	follower, ok := task.(DefaultFollowingTask)
	return ok && follower.FollowsDefaultTriggers()
}

func startSpread(task Task) time.Duration {
	spreader, ok := task.(StartSpreader)
	if !ok {
		return 0
	}
	return spreader.StartSpread()
}

func servesAnyLibrary(task Task, libraryTypes []string) bool {
	scopedTask, ok := task.(LibraryScopedTask)
	if !ok {
		return true
	}
	for _, libraryType := range libraryTypes {
		if scopedTask.ServesLibrary(libraryType) {
			return true
		}
	}
	return false
}

// UpdateTriggers replaces the triggers for a task.
func (m *TaskManager) UpdateTriggers(key string, triggerConfigs []TriggerConfig) error {
	w, err := m.getWorker(key)
	if err != nil {
		return err
	}
	w.scheduleMu.Lock()
	defer w.scheduleMu.Unlock()
	if isManualOnly(w.task) && len(triggerConfigs) > 0 {
		return ErrTaskManualOnly
	}

	if err := m.triggerRepo.SetTriggers(context.Background(), key, triggerConfigs); err != nil {
		return err
	}

	w.setTriggers(triggerConfigs, m.triggerFactory, nil, true)
	m.notifyTaskUpdated(w.info())
	return nil
}

// GetSchedule reads the durable editor state rather than a live trigger snapshot.
// A DefaultFollowingTask with no saved schedule reports its DefaultTriggers at
// revision zero, the schedule it runs until one is saved.
func (m *TaskManager) GetSchedule(ctx context.Context, key string) (Schedule, error) {
	w, err := m.getWorker(key)
	if err != nil {
		return Schedule{}, err
	}
	repo, ok := m.triggerRepo.(GuardedTriggerRepository)
	if !ok {
		return Schedule{}, fmt.Errorf("guarded task schedules unavailable")
	}
	schedule, err := repo.GetSchedule(ctx, key)
	if err == nil && schedule.Revision == 0 && followsDefaultTriggers(w.task) {
		schedule.Triggers = w.task.DefaultTriggers()
	}
	return schedule, err
}

// UpdateSchedule persists under the original revision and installs that schedule
// on this process. Other running processes do not automatically reload it.
func (m *TaskManager) UpdateSchedule(ctx context.Context, key string, expected int64, configs []TriggerConfig) (Schedule, error) {
	w, err := m.getWorker(key)
	if err != nil {
		return Schedule{}, err
	}
	if isManualOnly(w.task) && len(configs) > 0 {
		return Schedule{}, ErrTaskManualOnly
	}
	repo, ok := m.triggerRepo.(GuardedTriggerRepository)
	if !ok {
		return Schedule{}, fmt.Errorf("guarded task schedules unavailable")
	}
	w.scheduleMu.Lock()
	defer w.scheduleMu.Unlock()
	saved, err := repo.ReplaceSchedule(ctx, key, expected, configs)
	if err != nil {
		return Schedule{}, err
	}
	w.setTriggers(saved.Triggers, m.triggerFactory, nil, true)
	return saved, nil
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
