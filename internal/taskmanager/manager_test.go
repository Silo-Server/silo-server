package taskmanager_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
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

type libraryScopedStubTask struct {
	stubTask
	libraryType string
}

func (t libraryScopedStubTask) ServesLibrary(libraryType string) bool {
	return libraryType == t.libraryType
}

type hiddenStubTask struct{ stubTask }

func (hiddenStubTask) IsHidden() bool { return true }

func TestTaskManagerListRelevantTasksOmitsLibraryScopedTasksWithoutLibrary(t *testing.T) {
	newManager := func(lookup taskmanager.LibraryTypesFunc) *taskmanager.TaskManager {
		manager := taskmanager.New(
			&fakeTriggerRepository{triggers: map[string][]taskmanager.TriggerConfig{}},
			fakeExecutionRepository{},
			newFakeTrigger,
			slog.New(slog.DiscardHandler),
		)
		manager.Register(stubTask{key: "always"})
		manager.Register(hiddenStubTask{stubTask{key: "hidden"}})
		manager.Register(libraryScopedStubTask{stubTask: stubTask{key: "ebooks"}, libraryType: "ebooks"})
		if lookup != nil {
			manager.SetLibraryTypes(lookup)
		}
		return manager
	}
	keys := func(infos []taskmanager.TaskInfo) []string {
		out := make([]string, 0, len(infos))
		for _, info := range infos {
			out = append(out, info.Key)
		}
		return out
	}
	libraries := func(types ...string) taskmanager.LibraryTypesFunc {
		return func(context.Context) ([]string, error) { return types, nil }
	}
	failing := func(context.Context) ([]string, error) { return nil, errors.New("database unavailable") }
	relevant := func(m *taskmanager.TaskManager) []taskmanager.TaskInfo {
		return m.ListRelevantTasks(context.Background())
	}
	visible := func(m *taskmanager.TaskManager) []taskmanager.TaskInfo { return m.ListTasks(false) }
	all := func(m *taskmanager.TaskManager) []taskmanager.TaskInfo { return m.ListTasks(true) }

	tests := []struct {
		name   string
		lookup taskmanager.LibraryTypesFunc
		list   func(*taskmanager.TaskManager) []taskmanager.TaskInfo
		want   []string
	}{
		{name: "no matching library", lookup: libraries("movies", "series"), list: relevant, want: []string{"always"}},
		{name: "matching library", lookup: libraries("movies", "ebooks"), list: relevant, want: []string{"always", "ebooks"}},
		{name: "lookup fails open", lookup: failing, list: relevant, want: []string{"always", "ebooks"}},
		{name: "no lookup installed", list: relevant, want: []string{"always", "ebooks"}},
		// The v1 list keeps its behavior: hidden flags only, no library scoping.
		{name: "full list ignores library scoping", lookup: libraries("movies"), list: visible, want: []string{"always", "ebooks"}},
		{name: "hidden-inclusive list", lookup: libraries("movies"), list: all, want: []string{"always", "ebooks", "hidden"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keys(tt.list(newManager(tt.lookup)))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("listed keys = %v, want %v", got, tt.want)
			}
		})
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

func TestTaskManagerRejectsScheduledTriggersForManualOnlyTask(t *testing.T) {
	const taskKey = "manual-backfill"
	triggerRepo := &fakeTriggerRepository{triggers: map[string][]taskmanager.TriggerConfig{}}
	manager := taskmanager.New(
		triggerRepo,
		fakeExecutionRepository{},
		newFakeTrigger,
		slog.New(slog.DiscardHandler),
	)
	manager.Register(manualOnlyStubTask{stubTask{key: taskKey}})

	if info := manager.GetTaskInfo(taskKey); !info.ManualOnly {
		t.Fatal("TaskInfo.ManualOnly = false, want true")
	}
	err := manager.UpdateTriggers(taskKey, []taskmanager.TriggerConfig{{
		Type:       taskmanager.TriggerTypeInterval,
		IntervalMs: int64(time.Hour / time.Millisecond),
	}})
	if !errors.Is(err, taskmanager.ErrTaskManualOnly) {
		t.Fatalf("UpdateTriggers() error = %v, want ErrTaskManualOnly", err)
	}
	if _, wrote := triggerRepo.setCalls[taskKey]; wrote {
		t.Fatal("manual-only trigger rejection wrote to the trigger repository")
	}
	if err := manager.UpdateTriggers(taskKey, nil); err != nil {
		t.Fatalf("clearing manual-only triggers error = %v", err)
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
			cfg:     cfg,
			ch:      make(chan struct{}, 1),
			stopCh:  make(chan struct{}),
			started: make(chan struct{}, 2),
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
	initialNextRun := triggers[0].NextRunTime()
	select {
	case <-triggers[0].started:
	default:
	}
	triggers[0].ch <- struct{}{}

	select {
	case <-task.shouldRunCalled:
	case <-time.After(time.Second):
		t.Fatal("scheduled preflight was not called")
	}

	select {
	case <-triggers[0].started:
	case <-time.After(time.Second):
		t.Fatal("trigger was not rearmed after skipped preflight error")
	}
	if got := task.executeCount(); got != 0 {
		t.Fatalf("Execute calls = %d, want 0", got)
	}
	if got := historyRepo.insertCount(); got != 0 {
		t.Fatalf("history inserts = %d, want 0", got)
	}
	if !triggers[0].NextRunTime().After(initialNextRun) {
		t.Fatalf("next run = %s, want later than original run %s",
			triggers[0].NextRunTime().Format(time.RFC3339Nano),
			initialNextRun.Format(time.RFC3339Nano))
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
			cfg:     cfg,
			ch:      make(chan struct{}, 1),
			stopCh:  make(chan struct{}),
			started: make(chan struct{}, 2),
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
	initialNextRun := triggers[0].NextRunTime()
	select {
	case <-triggers[0].started:
	default:
	}
	triggers[0].ch <- struct{}{}

	select {
	case <-task.shouldRunCalled:
	case <-time.After(time.Second):
		t.Fatal("scheduled preflight was not called")
	}

	select {
	case <-triggers[0].started:
	case <-time.After(time.Second):
		t.Fatal("trigger was not rearmed after skipped preflight error")
	}
	if got := task.executeCount(); got != 0 {
		t.Fatalf("Execute calls = %d, want 0 (preflight errors must fail closed)", got)
	}
	if !triggers[0].NextRunTime().After(initialNextRun) {
		t.Fatalf("next run = %s, want rearmed after skipped preflight error",
			triggers[0].NextRunTime().Format(time.RFC3339Nano))
	}
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
	if err != nil || info.State != taskmanager.TaskStateRunning {
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
