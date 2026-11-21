package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
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
	firingClaims   FiringClaims
	// startCtx is the context Start ran under, nil before Start. A task
	// registered later runs its trigger loop under it.
	startCtx  context.Context //nolint:containedctx // Start's context outlives the call: tasks registered after Start run under it
	stopLoops context.CancelFunc
	stopped   atomic.Bool
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
	if err := m.register(context.Background(), task, false); err != nil {
		m.logger.Error("failed to load triggers", "task", task.Key(), "error", err)
	}
}

// RegisterLive is Register for a caller that retries, such as the plugin
// task registry: when Start has run and the task's schedule cannot be read,
// it leaves no task under the key and returns the error.
func (m *TaskManager) RegisterLive(ctx context.Context, task Task) error {
	return m.register(ctx, task, true)
}

// register initializes a live worker before exposing it to callers.
func (m *TaskManager) register(loadCtx context.Context, task Task, requireSchedule bool) error {
	w := newTaskWorker(task, m)
	key := task.Key()
	m.mu.Lock()
	ctx := m.startCtx
	if m.stopped.Load() {
		m.mu.Unlock()
		return context.Canceled
	}
	if ctx == nil {
		replaced := m.tasks[key]
		m.tasks[key] = w
		m.mu.Unlock()
		if replaced != nil {
			replaced.retire()
		}
		return nil
	}
	m.mu.Unlock()

	if !requireSchedule {
		loadCtx = ctx
	}
	err := m.loadWorker(loadCtx, w)
	if err != nil && requireSchedule {
		return err
	}
	m.mu.Lock()
	replaced := m.tasks[key]
	if m.stopped.Load() || ctx.Err() != nil {
		m.mu.Unlock()
		w.stopTriggers()
		return context.Canceled
	}
	if replaced != nil && replaced.isRetired() {
		m.mu.Unlock()
		w.stopTriggers()
		return ErrTaskAlreadyRunning
	}
	m.tasks[key] = w
	m.mu.Unlock()
	if replaced != nil {
		replaced.retire()
	}
	go m.triggerLoop(ctx, w)
	w.notify()
	return err
}

// Unregister removes a task: its triggers stop, its trigger loop ends and a
// run in progress is canceled. Its saved schedule and history stay, so
// registering the key again resumes that schedule. An unknown key is ignored.
func (m *TaskManager) Unregister(key string) {
	w, err := m.getWorker(key)
	if err != nil {
		return
	}
	w.retire()
	m.removeRetiredWorker(w)
}

// A retired worker is hidden immediately. Keep it under its key until
// Execute returns, so a rapid disable and enable cannot start a second run.
func (m *TaskManager) removeRetiredWorker(w *taskWorker) {
	if !w.isRetired() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	w.mu.RLock()
	defer w.mu.RUnlock()
	key := w.task.Key()
	if m.tasks[key] == w && w.state == TaskStateIdle {
		delete(m.tasks, key)
	}
}

// Start loads triggers from the repository and begins all scheduling loops.
func (m *TaskManager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.stopped.Load() {
		m.mu.Unlock()
		return
	}
	ctx, m.stopLoops = context.WithCancel(ctx)
	m.startCtx = ctx //nolint:fatcontext // set once, by Start
	workers := make([]*taskWorker, 0, len(m.tasks))
	for _, w := range m.tasks {
		workers = append(workers, w)
	}
	m.mu.Unlock()

	for _, w := range workers {
		if err := m.loadWorker(ctx, w); err != nil {
			m.logger.ErrorContext(ctx, "failed to load triggers", "task", w.task.Key(), "error", err)
		}
		go m.triggerLoop(ctx, w)
		w.notify()
	}
	m.logger.InfoContext(ctx, "task manager started", "tasks", len(workers))
}

// loadWorker loads a task's last result and schedule before its loop starts.
func (m *TaskManager) loadWorker(ctx context.Context, w *taskWorker) error {
	key := w.task.Key()
	latest, err := m.historyRepo.GetLatest(ctx, key)
	if err == nil && latest != nil {
		w.mu.Lock()
		w.lastResult = latest
		w.mu.Unlock()
	} else {
		latest = nil
	}

	w.scheduleMu.Lock()
	configs, err := m.savedSchedule(ctx, w)
	w.setTriggers(configs, m.triggerFactory, latest, false)
	w.scheduleMu.Unlock()
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
func (m *TaskManager) ReloadSchedule(ctx context.Context, key string, presentationChanged bool) error {
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
	if w.isRetired() {
		w.scheduleMu.Unlock()
		return ErrTaskNotFound
	}
	configs, err := m.savedSchedule(ctx, w)
	if err != nil {
		w.scheduleMu.Unlock()
		if presentationChanged {
			w.notify()
		}
		return err
	}
	scheduleChanged := !w.runsSchedule(configs)
	if scheduleChanged {
		w.setTriggers(configs, m.triggerFactory, nil, true)
	}
	w.scheduleMu.Unlock()
	if scheduleChanged || presentationChanged {
		w.notify()
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

		// fired carries the trigger that fired: its config gives the run's
		// max runtime, and its due time names the firing to claim.
		fired := make(chan Trigger, 1)
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
						case fired <- tr:
						default:
						}
					}
				}
			}()
		}

		firedTrigger, wait := m.awaitFiring(ctx, w, gen, fired)
		close(done)
		if wait == firingStop {
			return
		}
		if wait == firingReplaced {
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
			m.rearmTriggersFromNow(w, gen)
			continue
		}
		if !shouldRun {
			m.rearmTriggersFromNow(w, gen)
			continue
		}
		execCtx, cancel, err := m.reserveFiring(ctx, w, gen, firedTrigger)
		switch {
		case errors.Is(err, ErrTaskNotFound):
			return
		case errors.Is(err, errFiringNotClaimed):
			// Another server runs this firing. Rearm from now, as after a
			// skipped preflight: this server's last result is older than that
			// run, so an interval would otherwise fire again at once.
			m.reloadLastResult(ctx, w)
			m.rearmTriggersFromNow(w, gen)
			continue
		case err != nil:
			continue
		}

		result := w.executeReserved(execCtx, cancel)
		if err := m.historyRepo.Insert(ctx, *result); err != nil {
			m.logger.ErrorContext(ctx, "failed to persist execution result", "task", w.task.Key(), "error", err)
		}
	}
}

var (
	errFiringNotClaimed = errors.New("scheduled firing not claimed")
	errFiringReplaced   = errors.New("scheduled firing was replaced")
)

// reserveFiring serializes the claim and local reservation with schedule
// edits, retirement, and manual admission. Any retirement after admission
// cancels a reserved run, which still records an execution result.
func (m *TaskManager) reserveFiring(ctx context.Context, w *taskWorker, gen uint64, trigger Trigger) (context.Context, context.CancelFunc, error) {
	w.scheduleMu.Lock()
	defer w.scheduleMu.Unlock()
	if w.isRetired() {
		return nil, nil, ErrTaskNotFound
	}
	w.mu.RLock()
	current, idle := w.triggerGen, w.state == TaskStateIdle
	w.mu.RUnlock()
	if current != gen {
		return nil, nil, errFiringReplaced
	}
	if !idle {
		return nil, nil, ErrTaskAlreadyRunning
	}
	if !m.claimFiring(ctx, w, trigger) {
		return nil, nil, errFiringNotClaimed
	}
	return w.reserve(ctx, trigger.Config().MaxRuntime())
}

func (m *TaskManager) shouldRunScheduledTask(ctx context.Context, w *taskWorker) (bool, error) {
	task, ok := w.task.(ScheduledConditionalTask)
	if !ok {
		return true, nil
	}
	return task.ShouldRun(ctx)
}

// firingWait is how awaitFiring ended.
type firingWait int

const (
	firingRun      firingWait = iota // a current trigger fired
	firingReplaced                   // the triggers changed: wait on the new ones
	firingStop                       // the manager stopped or the task was retired
)

// awaitFiring waits for one of the triggers read at generation gen to fire.
// An update signal for a generation the loop already read is stale (it was
// sent while the loop ran the task or handled an earlier update) and is
// ignored, so it can't discard a firing a forwarder already took from a
// one-shot trigger. A firing from triggers replaced since gen is dropped:
// the update that replaced them wakes the loop for the new ones.
func (m *TaskManager) awaitFiring(ctx context.Context, w *taskWorker, gen uint64, fired <-chan Trigger) (Trigger, firingWait) {
	for {
		select {
		case <-ctx.Done():
			return nil, firingStop
		case <-w.retired:
			return nil, firingStop
		case <-w.triggerUpdate:
			if w.generation() != gen {
				return nil, firingReplaced
			}
		case tr := <-fired:
			if w.generation() != gen {
				return nil, firingReplaced
			}
			return tr, firingRun
		}
	}
}

// rearmTriggersFromNow restarts triggers after a skipped conditional task. This
// intentionally ignores lastResult; otherwise an old completed_at can make an
// interval trigger fire immediately in a loop while there is no work.
func (m *TaskManager) rearmTriggersFromNow(w *taskWorker, gen uint64) {
	w.mu.Lock()
	if w.triggerGen != gen || w.state != TaskStateIdle {
		w.mu.Unlock()
		return
	}
	w.rearmTriggersLocked(nil)
	w.mu.Unlock()
	w.notify()
}

// Stop stops all triggers and cancels running tasks.
func (m *TaskManager) Stop() {
	m.mu.Lock()
	m.stopped.Store(true)
	if m.stopLoops != nil {
		m.stopLoops()
	}
	workers := make([]*taskWorker, 0, len(m.tasks))
	for _, w := range m.tasks {
		workers = append(workers, w)
	}
	m.mu.Unlock()
	for _, w := range workers {
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

	result, err := w.run(ctx, 0)
	if err != nil {
		return err
	}

	if result != nil {
		if insertErr := m.historyRepo.Insert(ctx, *result); insertErr != nil {
			m.logger.ErrorContext(ctx, "failed to persist execution result", "task", key, "error", insertErr)
		}
	}

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
	w.scheduleMu.Lock()
	ctx, cancel, err := w.reserve(context.WithValue(context.Background(), manualStartKey{}, true), 0)
	w.scheduleMu.Unlock()
	if err != nil {
		return TaskInfo{}, err
	}
	info := w.info()
	go func() {
		result := w.executeReserved(ctx, cancel)
		if err := m.historyRepo.Insert(context.Background(), *result); err != nil {
			m.logger.Error("failed to persist execution result", "task", key, "error", err)
		}
	}()
	return info, nil
}

// CancelTask requests cancellation of a running task.
func (m *TaskManager) CancelTask(key string) (TaskInfo, error) {
	w, err := m.getWorker(key)
	if err != nil {
		return TaskInfo{}, err
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
		if !w.isRetired() && include(w) {
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
	if w.isRetired() {
		return ErrTaskNotFound
	}
	if isManualOnly(w.task) && len(triggerConfigs) > 0 {
		return ErrTaskManualOnly
	}

	if err := m.triggerRepo.SetTriggers(context.Background(), key, triggerConfigs); err != nil {
		return err
	}

	w.setTriggers(triggerConfigs, m.triggerFactory, nil, true)
	w.notify()
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
// on this process. Other running processes reload it only through
// ReloadSchedule, which the plugin task registry calls for plugin tasks.
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
	if w.isRetired() {
		return Schedule{}, ErrTaskNotFound
	}
	saved, err := repo.ReplaceSchedule(ctx, key, expected, configs)
	if err != nil {
		return Schedule{}, err
	}
	w.setTriggers(saved.Triggers, m.triggerFactory, nil, true)
	w.notify()
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
	if !ok || w.isRetired() {
		return nil, ErrTaskNotFound
	}
	return w, nil
}
