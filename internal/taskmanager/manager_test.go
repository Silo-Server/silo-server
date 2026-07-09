package taskmanager_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
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
	return append([]taskmanager.TriggerConfig(nil), r.triggers[taskKey]...), nil
}

func (r *fakeTriggerRepository) SetTriggers(_ context.Context, taskKey string, triggers []taskmanager.TriggerConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.triggers == nil {
		r.triggers = map[string][]taskmanager.TriggerConfig{}
	}
	if r.setCalls == nil {
		r.setCalls = map[string][]taskmanager.TriggerConfig{}
	}
	copied := append([]taskmanager.TriggerConfig(nil), triggers...)
	r.triggers[taskKey] = copied
	r.setCalls[taskKey] = copied
	return nil
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
	cfg    taskmanager.TriggerConfig
	ch     chan struct{}
	next   time.Time
	stopCh chan struct{}
}

func (t *fakeTrigger) Start(lastResult *taskmanager.ExecutionResult) {
	interval := time.Minute
	if t.cfg.IntervalMs > 0 {
		interval = time.Duration(t.cfg.IntervalMs) * time.Millisecond
	}
	base := time.Now()
	if lastResult != nil && !lastResult.CompletedAt.IsZero() {
		base = lastResult.CompletedAt
	}
	t.next = base.Add(interval)
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

func (t *fakeTrigger) NextRunTime() time.Time            { return t.next }
func (t *fakeTrigger) Config() taskmanager.TriggerConfig { return t.cfg }
func (t *fakeTrigger) C() <-chan struct{}                { return t.ch }

type fakeServerSettings struct {
	values map[string]string
}

func (s *fakeServerSettings) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *fakeServerSettings) Set(_ context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

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

func TestTaskManagerStartSeedsCleanupTaskDefaults(t *testing.T) {
	triggerRepo := &fakeTriggerRepository{triggers: map[string][]taskmanager.TriggerConfig{}}
	settings := &fakeServerSettings{
		values: map[string]string{"opslog.cleanup_interval_minutes": "42"},
	}
	manager := taskmanager.New(
		triggerRepo,
		fakeExecutionRepository{},
		newFakeTrigger,
		slog.New(slog.DiscardHandler),
	)

	manager.Register(taskdefs.NewActivityLogCleanupTask(nil, settings, nil))
	manager.Register(taskdefs.NewOperationalLogCleanupTask(nil, settings, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer manager.Stop()
	defer cancel()
	manager.Start(ctx)

	wantActivity := []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
	if got := triggerRepo.setCalls["cleanup_activity_log"]; !reflect.DeepEqual(got, wantActivity) {
		t.Fatalf("cleanup_activity_log triggers = %#v, want %#v", got, wantActivity)
	}

	wantOps := []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((42 * time.Minute) / time.Millisecond)},
	}
	if got := triggerRepo.setCalls["cleanup_operational_log"]; !reflect.DeepEqual(got, wantOps) {
		t.Fatalf("cleanup_operational_log triggers = %#v, want %#v", got, wantOps)
	}
}

func TestTaskManagerStartPreservesExistingTriggers(t *testing.T) {
	existing := []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "03:15"},
	}
	triggerRepo := &fakeTriggerRepository{
		triggers: map[string][]taskmanager.TriggerConfig{
			"cleanup_activity_log": existing,
		},
	}
	manager := taskmanager.New(
		triggerRepo,
		fakeExecutionRepository{},
		newFakeTrigger,
		slog.New(slog.DiscardHandler),
	)

	manager.Register(stubTask{
		key: "cleanup_activity_log",
		triggers: []taskmanager.TriggerConfig{
			{Type: taskmanager.TriggerTypeStartup},
			{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer manager.Stop()
	defer cancel()
	manager.Start(ctx)

	if _, ok := triggerRepo.setCalls["cleanup_activity_log"]; ok {
		t.Fatalf("expected existing triggers to be preserved without SetTriggers call")
	}

	if got := manager.GetTaskInfo("cleanup_activity_log").Triggers; !reflect.DeepEqual(got, existing) {
		t.Fatalf("worker triggers = %#v, want %#v", got, existing)
	}
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

func TestTaskManagerTriggerSkipsConditionalTaskWithoutHistory(t *testing.T) {
	triggerRepo := &fakeTriggerRepository{
		triggers: map[string][]taskmanager.TriggerConfig{
			"conditional": {
				{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Hour / time.Millisecond)},
			},
		},
	}
	historyRepo := &recordingExecutionRepository{
		latest: &taskmanager.ExecutionResult{
			TaskKey:     "conditional",
			CompletedAt: time.Now().Add(-24 * time.Hour),
		},
	}
	var triggers []*fakeTrigger
	factory := func(cfg taskmanager.TriggerConfig) taskmanager.Trigger {
		tr := &fakeTrigger{
			cfg:    cfg,
			ch:     make(chan struct{}, 1),
			stopCh: make(chan struct{}),
		}
		triggers = append(triggers, tr)
		return tr
	}
	manager := taskmanager.New(
		triggerRepo,
		historyRepo,
		factory,
		slog.New(slog.DiscardHandler),
	)
	task := &conditionalStubTask{
		stubTask:        stubTask{key: "conditional"},
		shouldRunCalled: make(chan struct{}, 1),
	}
	manager.Register(task)

	ctx, cancel := context.WithCancel(context.Background())
	defer manager.Stop()
	defer cancel()
	manager.Start(ctx)

	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(triggers))
	}
	beforeTrigger := time.Now()
	triggers[0].ch <- struct{}{}

	select {
	case <-task.shouldRunCalled:
	case <-time.After(time.Second):
		t.Fatal("scheduled preflight was not called")
	}

	time.Sleep(25 * time.Millisecond)
	if got := task.executeCount(); got != 0 {
		t.Fatalf("Execute calls = %d, want 0", got)
	}
	if got := historyRepo.insertCount(); got != 0 {
		t.Fatalf("history inserts = %d, want 0", got)
	}
	if !triggers[0].next.After(beforeTrigger) {
		t.Fatalf("next run = %s, want rearmed after skip time %s",
			triggers[0].next.Format(time.RFC3339Nano),
			beforeTrigger.Format(time.RFC3339Nano))
	}
}

// A preflight that cannot answer must fail closed: skipping the run is
// recoverable at the next trigger, while running an expensive conditional
// task on a transient error is exactly what the gate exists to prevent.
func TestTaskManagerTriggerSkipsConditionalTaskOnPreflightError(t *testing.T) {
	triggerRepo := &fakeTriggerRepository{
		triggers: map[string][]taskmanager.TriggerConfig{
			"conditional": {
				{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Hour / time.Millisecond)},
			},
		},
	}
	historyRepo := &recordingExecutionRepository{}
	var triggers []*fakeTrigger
	factory := func(cfg taskmanager.TriggerConfig) taskmanager.Trigger {
		tr := &fakeTrigger{
			cfg:    cfg,
			ch:     make(chan struct{}, 1),
			stopCh: make(chan struct{}),
		}
		triggers = append(triggers, tr)
		return tr
	}
	manager := taskmanager.New(
		triggerRepo,
		historyRepo,
		factory,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	task := &conditionalStubTask{
		stubTask:        stubTask{key: "conditional"},
		shouldRunCalled: make(chan struct{}, 1),
		shouldRunErr:    errors.New("settings unavailable"),
	}
	manager.Register(task)

	ctx, cancel := context.WithCancel(context.Background())
	defer manager.Stop()
	defer cancel()
	manager.Start(ctx)

	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(triggers))
	}
	beforeTrigger := time.Now()
	triggers[0].ch <- struct{}{}

	select {
	case <-task.shouldRunCalled:
	case <-time.After(time.Second):
		t.Fatal("scheduled preflight was not called")
	}

	time.Sleep(25 * time.Millisecond)
	if got := task.executeCount(); got != 0 {
		t.Fatalf("Execute calls = %d, want 0 (preflight errors must fail closed)", got)
	}
	if !triggers[0].next.After(beforeTrigger) {
		t.Fatalf("next run = %s, want rearmed after skipped preflight error",
			triggers[0].next.Format(time.RFC3339Nano))
	}
}
