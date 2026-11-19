package taskmanager_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
	taskdefs "github.com/Silo-Server/silo-server/internal/taskmanager/tasks"
)

type fakeTriggerRepository struct {
	mu       sync.Mutex
	triggers map[string][]taskmanager.TriggerConfig
	setCalls map[string][]taskmanager.TriggerConfig
}

func (r *fakeTriggerRepository) GetTriggers(_ context.Context, taskKey string) ([]taskmanager.TriggerConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.triggers[taskKey]), nil
}

func (r *fakeTriggerRepository) GetOrCreateTriggers(_ context.Context, taskKey string, defaults []taskmanager.TriggerConfig) ([]taskmanager.TriggerConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.triggers[taskKey]; !exists {
		r.setTriggers(taskKey, defaults)
	}
	return slices.Clone(r.triggers[taskKey]), nil
}

func (r *fakeTriggerRepository) SetTriggers(_ context.Context, taskKey string, triggers []taskmanager.TriggerConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setTriggers(taskKey, triggers)
	return nil
}

func (r *fakeTriggerRepository) setTriggers(taskKey string, triggers []taskmanager.TriggerConfig) {
	if r.triggers == nil {
		r.triggers = map[string][]taskmanager.TriggerConfig{}
	}
	if r.setCalls == nil {
		r.setCalls = map[string][]taskmanager.TriggerConfig{}
	}
	copied := append([]taskmanager.TriggerConfig{}, triggers...)
	r.triggers[taskKey] = copied
	r.setCalls[taskKey] = copied
}

type fakeExecutionRepository struct{}

func (fakeExecutionRepository) Insert(context.Context, taskmanager.ExecutionResult) error { return nil }
func (fakeExecutionRepository) GetLatest(context.Context, string) (*taskmanager.ExecutionResult, error) {
	return nil, nil
}
func (fakeExecutionRepository) List(context.Context, string, int) ([]taskmanager.ExecutionResult, error) {
	return nil, nil
}

type recordingExecutionRepository struct {
	mu      sync.Mutex
	inserts []taskmanager.ExecutionResult
	latest  *taskmanager.ExecutionResult
}

func (r *recordingExecutionRepository) Insert(_ context.Context, result taskmanager.ExecutionResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inserts = append(r.inserts, result)
	return nil
}

func (r *recordingExecutionRepository) GetLatest(context.Context, string) (*taskmanager.ExecutionResult, error) {
	return r.latest, nil
}

func (r *recordingExecutionRepository) List(context.Context, string, int) ([]taskmanager.ExecutionResult, error) {
	return nil, nil
}

func (r *recordingExecutionRepository) insertCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inserts)
}

type fakeTrigger struct {
	mu      sync.Mutex
	cfg     taskmanager.TriggerConfig
	ch      chan struct{}
	next    time.Time
	stopCh  chan struct{}
	started chan struct{}
}

func (t *fakeTrigger) Start(lastResult *taskmanager.ExecutionResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	interval := time.Minute
	if t.cfg.IntervalMs > 0 {
		interval = time.Duration(t.cfg.IntervalMs) * time.Millisecond
	}
	base := time.Now()
	if lastResult != nil && !lastResult.CompletedAt.IsZero() {
		base = lastResult.CompletedAt
	}
	t.next = base.Add(interval)
	if t.started != nil {
		select {
		case t.started <- struct{}{}:
		default:
		}
	}
}

func (t *fakeTrigger) Stop() {
	if t.stopCh != nil {
		select {
		case <-t.stopCh:
		default:
			close(t.stopCh)
		}
	}
}

func (t *fakeTrigger) NextRunTime() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.next
}
func (t *fakeTrigger) Config() taskmanager.TriggerConfig { return t.cfg }
func (t *fakeTrigger) C() <-chan struct{}                { return t.ch }

type stubTask struct {
	key      string
	triggers []taskmanager.TriggerConfig
}

func (t stubTask) Key() string                        { return t.key }
func (t stubTask) Name() string                       { return t.key }
func (t stubTask) Description() string                { return t.key }
func (t stubTask) Category() taskmanager.TaskCategory { return taskmanager.TaskCategorySystem }
func (t stubTask) IsHidden() bool                     { return false }
func (t stubTask) Execute(context.Context, taskmanager.ProgressReporter) error {
	return nil
}

func (t stubTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return append([]taskmanager.TriggerConfig(nil), t.triggers...)
}

type conditionalStubTask struct {
	stubTask
	shouldRunCalled chan struct{}
	shouldRunErr    error
	mu              sync.Mutex
	executeCalls    int
}

type manualOnlyStubTask struct{ stubTask }

func (manualOnlyStubTask) ManualOnly() bool { return true }

func (t *conditionalStubTask) ShouldRun(context.Context) (bool, error) {
	select {
	case t.shouldRunCalled <- struct{}{}:
	default:
	}
	return false, t.shouldRunErr
}

func (t *conditionalStubTask) Execute(context.Context, taskmanager.ProgressReporter) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.executeCalls++
	return nil
}

func (t *conditionalStubTask) executeCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.executeCalls
}

func newFakeTrigger(cfg taskmanager.TriggerConfig) taskmanager.Trigger {
	return &fakeTrigger{
		cfg:    cfg,
		ch:     make(chan struct{}),
		stopCh: make(chan struct{}),
	}
}

type recordingObserver struct {
	mu      sync.Mutex
	updates []taskmanager.TaskInfo
}

func (o *recordingObserver) TaskUpdated(info taskmanager.TaskInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.updates = append(o.updates, info)
}

func (o *recordingObserver) last() taskmanager.TaskInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.updates) == 0 {
		return taskmanager.TaskInfo{}
	}
	return o.updates[len(o.updates)-1]
}

func TestTaskManagerRunTaskNotifiesAfterTriggerRearm(t *testing.T) {
	const taskKey = "refresh_metadata"
	triggerRepo := &fakeTriggerRepository{
		triggers: map[string][]taskmanager.TriggerConfig{
			taskKey: {
				{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Hour / time.Millisecond)},
			},
		},
	}
	observer := &recordingObserver{}
	manager := taskmanager.New(
		triggerRepo,
		fakeExecutionRepository{},
		newFakeTrigger,
		slog.New(slog.DiscardHandler),
	)
	manager.AddObserver(observer)
	manager.Register(stubTask{key: taskKey})

	ctx, cancel := context.WithCancel(context.Background())
	defer manager.Stop()
	defer cancel()
	manager.Start(ctx)

	if err := manager.RunTask(ctx, taskKey); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}

	last := observer.last()
	if last.LastExecution == nil {
		t.Fatal("last notification missing execution result")
	}
	if last.NextRunAt == nil {
		t.Fatal("last notification missing next run time")
	}
	if !last.NextRunAt.After(last.LastExecution.CompletedAt) {
		t.Fatalf("next run = %s, want after completed_at %s",
			last.NextRunAt.Format(time.RFC3339Nano),
			last.LastExecution.CompletedAt.Format(time.RFC3339Nano))
	}
	if last.NextRunAt.Sub(last.LastExecution.CompletedAt) < 59*time.Minute {
		t.Fatalf("next run was not rearmed from the latest completion: got %s after completion",
			last.NextRunAt.Sub(last.LastExecution.CompletedAt))
	}
}

func TestTaskManagerStartIgnoresSavedTriggersForManualOnlyTask(t *testing.T) {
	const taskKey = "manual-repair"
	saved := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
	manager := taskmanager.New(
		&fakeTriggerRepository{triggers: map[string][]taskmanager.TriggerConfig{taskKey: saved}},
		fakeExecutionRepository{},
		newFakeTrigger,
		slog.New(slog.DiscardHandler),
	)
	manager.Register(manualOnlyStubTask{stubTask{key: taskKey}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	defer manager.Stop()

	info := manager.GetTaskInfo(taskKey)
	if len(info.Triggers) != 0 || info.NextRunAt != nil {
		t.Fatalf("manual-only task loaded triggers %#v (next run %v), want none", info.Triggers, info.NextRunAt)
	}
}

// cleanStopTask stops when its context ends and reports success, as tasks that
// keep partial work do.
type cleanStopTask struct{ stubTask }

func (cleanStopTask) Execute(ctx context.Context, _ taskmanager.ProgressReporter) error {
	<-ctx.Done()
	return nil
}

type reservedTask struct {
	stubTask
	entered chan struct{}
	done    chan struct{}
}

func (t reservedTask) Execute(ctx context.Context, _ taskmanager.ProgressReporter) error {
	close(t.entered)
	<-ctx.Done()
	close(t.done)
	return ctx.Err()
}
func TestStartTaskReservesBeforeAcknowledgment(t *testing.T) {
	task := reservedTask{stubTask: stubTask{key: "reserved"}, entered: make(chan struct{}), done: make(chan struct{})}
	manager := taskmanager.New(&fakeTriggerRepository{}, fakeExecutionRepository{}, nil, nil)
	manager.Register(task)
	info, err := manager.StartTask(task.Key())
	if err != nil || info.State != taskmanager.TaskStateRunning || info.StartedAt == nil {
		t.Fatalf("start: %+v, %v", info, err)
	}
	t.Cleanup(manager.Stop)
	if _, err := manager.StartTask(task.Key()); !errors.Is(err, taskmanager.ErrTaskAlreadyRunning) {
		t.Fatalf("second start: %v", err)
	}
	if err := manager.CancelTask(task.Key()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-task.done:
	case <-time.After(time.Second):
		t.Fatal("reserved work did not receive cancellation")
	}
}

type manualMarkerTask struct {
	stubTask
	seen chan bool
}

func (t manualMarkerTask) Execute(ctx context.Context, _ taskmanager.ProgressReporter) error {
	t.seen <- taskmanager.StartedManually(ctx)
	return nil
}

type recordingAutoscanPoller struct {
	calls chan string
}

func (p recordingAutoscanPoller) PollOnce(context.Context) error {
	p.calls <- "PollOnce"
	return nil
}

func (p recordingAutoscanPoller) PollNow(context.Context) error {
	p.calls <- "PollNow"
	return nil
}

func TestAutoscanPollTaskPollsNowOnlyWhenStartedManually(t *testing.T) {
	poller := recordingAutoscanPoller{calls: make(chan string, 1)}
	task := taskdefs.NewAutoscanPollTask(poller, 0)
	manager := taskmanager.New(&fakeTriggerRepository{}, fakeExecutionRepository{}, nil, nil)
	manager.Register(task)
	t.Cleanup(manager.Stop)

	if _, err := manager.StartTask(task.Key()); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	select {
	case call := <-poller.calls:
		if call != "PollNow" {
			t.Fatalf("manual start called %s, want PollNow", call)
		}
	case <-time.After(time.Second):
		t.Fatal("StartTask did not run the autoscan poll")
	}
	waitIdle(t, manager, task.Key())

	if err := manager.RunTask(context.Background(), task.Key()); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	select {
	case call := <-poller.calls:
		if call != "PollOnce" {
			t.Fatalf("non-manual run called %s, want PollOnce", call)
		}
	case <-time.After(time.Second):
		t.Fatal("RunTask did not run the autoscan poll")
	}
}

// waitIdle waits until a StartTask goroutine has released the worker.
func waitIdle(t *testing.T, manager *taskmanager.TaskManager, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for manager.GetTaskInfo(key).State != taskmanager.TaskStateIdle {
		if time.Now().After(deadline) {
			t.Fatalf("task %s did not return to idle", key)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
