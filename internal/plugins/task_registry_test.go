package plugins

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// scheduledTaskCapability stores a capability the way an install does, so the
// default trigger reaches the registry in its protojson metadata shape.
func scheduledTaskCapability(t *testing.T, id, displayName string, trigger *pluginv1.ScheduledTaskTrigger) *Capability {
	t.Helper()
	descriptor := &pluginv1.CapabilityDescriptor{Type: "scheduled_task.v1", Id: id, DisplayName: displayName}
	if trigger != nil {
		descriptor.ScheduledTask = &pluginv1.ScheduledTaskDescriptor{DefaultTrigger: trigger}
	}
	records, err := CapabilityRecordsFromManifest(&pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{descriptor}})
	if err != nil {
		t.Fatal(err)
	}
	return &records[0]
}

func TestPluginTaskTriggers(t *testing.T) {
	startup := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeStartup}
	fiveMinutes := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeInterval, IntervalMs: 300_000}
	manifestFiveMinutes := &pluginv1.ScheduledTaskTrigger{Type: "interval", IntervalMs: 300_000}
	for _, tc := range []struct {
		name     string
		binding  *TaskBinding
		declared *pluginv1.ScheduledTaskTrigger
		want     taskmanager.TriggerConfig
	}{
		{name: "no binding and no default", want: startup},
		{name: "no binding uses the interval default", declared: manifestFiveMinutes, want: fiveMinutes},
		{name: "daily default", declared: &pluginv1.ScheduledTaskTrigger{Type: "daily", TimeOfDay: "03:30"},
			want: taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "03:30"}},
		{name: "weekly default on Sunday", declared: &pluginv1.ScheduledTaskTrigger{Type: "weekly", TimeOfDay: "04:00", DayOfWeek: 0},
			want: taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeWeekly, TimeOfDay: "04:00"}},
		{name: "startup default", declared: &pluginv1.ScheduledTaskTrigger{Type: "startup"}, want: startup},
		{name: "interval default below the minimum", declared: &pluginv1.ScheduledTaskTrigger{Type: "interval", IntervalMs: 59_999}, want: startup},
		{name: "default of an unknown type", declared: &pluginv1.ScheduledTaskTrigger{Type: "cron"}, want: startup},
		{name: "daily default without a valid time", declared: &pluginv1.ScheduledTaskTrigger{Type: "daily", TimeOfDay: "25:00"}, want: startup},
		{name: "weekly default on no weekday", declared: &pluginv1.ScheduledTaskTrigger{Type: "weekly", TimeOfDay: "04:00", DayOfWeek: 7}, want: startup},
		{name: "binding overrides the default",
			binding:  &TaskBinding{Enabled: true, Trigger: map[string]any{"type": "daily", "time_of_day": "02:00"}},
			declared: manifestFiveMinutes,
			want:     taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00"}},
		{name: "binding interval may be shorter than the manifest minimum",
			binding: &TaskBinding{Enabled: true, Trigger: map[string]any{"type": "interval", "interval_ms": 1000}},
			want:    taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeInterval, IntervalMs: 1000}},
		{name: "empty binding trigger uses the default", binding: &TaskBinding{Enabled: true, Trigger: map[string]any{}}, declared: manifestFiveMinutes, want: fiveMinutes},
		{name: "malformed binding trigger uses the default",
			binding:  &TaskBinding{Enabled: true, Trigger: map[string]any{"type": "interval", "interval_ms": "soon"}},
			declared: manifestFiveMinutes, want: fiveMinutes},
		{name: "binding trigger keeps only the fields its type reads",
			binding: &TaskBinding{Enabled: true, Trigger: map[string]any{"type": "daily", "time_of_day": "02:00", "day_of_week": 3, "interval_ms": 5}},
			want:    taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00"}},
		{name: "binding trigger of an unknown type uses the default",
			binding:  &TaskBinding{Enabled: true, Trigger: map[string]any{"type": "cron", "expression": "0 * * * *"}},
			declared: manifestFiveMinutes, want: fiveMinutes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pluginTaskTriggers(tc.binding, scheduledTaskCapability(t, "sync", "", tc.declared))
			if !slices.Equal(got, []taskmanager.TriggerConfig{tc.want}) {
				t.Fatalf("triggers = %+v, want %+v", got, tc.want)
			}
		})
	}
}

type fakeTaskBindingStore struct {
	bindings []*TaskBinding
	err      error
}

func (s *fakeTaskBindingStore) ListTaskBindings(context.Context) ([]*TaskBinding, error) {
	return s.bindings, s.err
}

// memoryTriggerStore is task_schedules in memory; GetTriggers is nil for a
// task with no saved schedule.
type memoryTriggerStore struct {
	mu    sync.Mutex
	saved map[string][]taskmanager.TriggerConfig
	err   error
}

func (s *memoryTriggerStore) GetTriggers(_ context.Context, key string) ([]taskmanager.TriggerConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return slices.Clone(s.saved[key]), nil
}

func (s *memoryTriggerStore) GetOrCreateTriggers(ctx context.Context, key string, defaults []taskmanager.TriggerConfig) ([]taskmanager.TriggerConfig, error) {
	panic("a plugin task's defaults must not be saved")
}

func (s *memoryTriggerStore) SetTriggers(_ context.Context, key string, configs []taskmanager.TriggerConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved[key] = append([]taskmanager.TriggerConfig{}, configs...)
	return nil
}

type noExecutions struct{}

func (noExecutions) Insert(context.Context, taskmanager.ExecutionResult) error { return nil }
func (noExecutions) GetLatest(context.Context, string) (*taskmanager.ExecutionResult, error) {
	return nil, nil
}
func (noExecutions) List(context.Context, string, int) ([]taskmanager.ExecutionResult, error) {
	return nil, nil
}

// idleTrigger never fires; it records the schedule a task manager installed.
type idleTrigger struct{ cfg taskmanager.TriggerConfig }

func (idleTrigger) Start(*taskmanager.ExecutionResult)  {}
func (idleTrigger) Stop()                               {}
func (idleTrigger) NextRunTime() time.Time              { return time.Time{} }
func (t idleTrigger) Config() taskmanager.TriggerConfig { return t.cfg }
func (idleTrigger) C() <-chan struct{}                  { return nil }

// Sync applies installation and binding changes to a running task manager
// without a restart. A task with no saved schedule follows its binding and
// manifest default; a task-page edit wins over both; a sync never saves a
// schedule; and an unchanged schedule is not reinstalled, so its timer keeps
// running, even when the binding carries fields its type ignores.
func TestTaskRegistrySyncAppliesChangesLive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const key = "plugin:1:sync"
	installations := &fakeInstallationStore{
		installations: []*Installation{{ID: 1, PluginID: "example"}, {ID: 2, PluginID: "builtin", Kind: KindBuiltin}},
		capabilities: map[int][]*Capability{
			1: {scheduledTaskCapability(t, "sync", "Sync", &pluginv1.ScheduledTaskTrigger{Type: "interval", IntervalMs: 300_000}), {Type: "metadata_provider.v1", ID: "meta"}},
			2: {scheduledTaskCapability(t, "builtin", "", nil)},
		},
	}
	bindings := &fakeTaskBindingStore{}
	store := &memoryTriggerStore{saved: map[string][]taskmanager.TriggerConfig{}}
	var builds atomic.Int32
	manager := taskmanager.New(store, noExecutions{}, func(cfg taskmanager.TriggerConfig) taskmanager.Trigger {
		builds.Add(1)
		return idleTrigger{cfg: cfg}
	}, slog.New(slog.DiscardHandler))
	defer manager.Stop()
	registry := NewTaskRegistry(installations, bindings, nil, manager)
	resync := func() {
		t.Helper()
		if err := registry.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runs := func(want ...taskmanager.TriggerConfig) {
		t.Helper()
		if got := manager.GetTaskInfo(key).Triggers; !slices.Equal(got, want) {
			t.Fatalf("task runs %+v, want %+v", got, want)
		}
	}
	registered := func() []string {
		var keys []string
		for _, info := range manager.ListTasks(true) {
			keys = append(keys, info.Key)
		}
		return keys
	}
	setBinding := func(enabled bool, trigger map[string]any) {
		bindings.bindings = []*TaskBinding{{InstallationID: 1, CapabilityID: "sync", Enabled: enabled, Trigger: trigger}}
	}

	resync()
	manager.Start(ctx)
	if got := registered(); !slices.Equal(got, []string{key}) {
		t.Fatalf("registered %v, want only the plugin's task", got)
	}
	runs(taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeInterval, IntervalMs: 300_000})

	setBinding(true, map[string]any{"type": "daily", "time_of_day": "02:00", "day_of_week": 3})
	resync()
	daily := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00"}
	runs(daily)
	built := builds.Load()
	resync()
	if builds.Load() != built {
		t.Fatal("a sync with nothing changed reinstalled the schedule")
	}

	edited := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 600_000}}
	if err := manager.UpdateTriggers(key, edited); err != nil {
		t.Fatal(err)
	}
	setBinding(true, map[string]any{"type": "weekly", "time_of_day": "05:00", "day_of_week": 1})
	resync()
	runs(edited...)

	// An upgrade renames the task in place; a failed read changes nothing.
	installations.capabilities[1] = []*Capability{scheduledTaskCapability(t, "sync", "Sync library", nil)}
	resync()
	if got := manager.GetTaskInfo(key).Name; got != "Sync library (example)" {
		t.Fatalf("name after upgrade = %q", got)
	}
	bindings.err = errors.New("database unavailable")
	if err := registry.Sync(ctx); err == nil {
		t.Fatal("a failed read must fail the sync")
	}
	if got := registered(); !slices.Equal(got, []string{key}) {
		t.Fatalf("a failed read unregistered tasks: %v", got)
	}
	bindings.err = nil

	setBinding(false, map[string]any{})
	resync()
	if got := registered(); len(got) != 0 {
		t.Fatalf("disabled binding left %v registered", got)
	}
	setBinding(true, map[string]any{})
	resync()
	runs(edited...)
	installations.installations = nil
	resync()
	if got := registered(); len(got) != 0 {
		t.Fatalf("disabled plugin left %v registered", got)
	}
}

// A new task whose schedule cannot be read fails the sync and stays
// unregistered, so the retry after the failed sync registers it.
func TestTaskRegistryRetriesTaskWhoseScheduleFailedToLoad(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const key = "plugin:1:sync"
	installations := &fakeInstallationStore{
		installations: []*Installation{{ID: 1, PluginID: "example"}},
		capabilities:  map[int][]*Capability{1: {scheduledTaskCapability(t, "sync", "Sync", nil)}},
	}
	hourly := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 3_600_000}}
	store := &memoryTriggerStore{saved: map[string][]taskmanager.TriggerConfig{key: hourly}, err: errors.New("database unavailable")}
	manager := taskmanager.New(store, noExecutions{}, func(cfg taskmanager.TriggerConfig) taskmanager.Trigger {
		return idleTrigger{cfg: cfg}
	}, slog.New(slog.DiscardHandler))
	defer manager.Stop()
	manager.Start(ctx)
	registry := NewTaskRegistry(installations, &fakeTaskBindingStore{}, nil, manager)

	if err := registry.Sync(ctx); err == nil {
		t.Fatal("a schedule that could not be read must fail the sync")
	}
	if got := manager.ListTasks(true); len(got) != 0 {
		t.Fatalf("a task with an unread schedule is registered: %+v", got)
	}
	store.mu.Lock()
	store.err = nil
	store.mu.Unlock()
	if err := registry.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := manager.GetTaskInfo(key).Triggers; !slices.Equal(got, hourly) {
		t.Fatalf("retried task runs %+v, want the saved schedule", got)
	}
}

type countingBindingStore struct {
	calls atomic.Int32
	err   error
}

func (s *countingBindingStore) ListTaskBindings(context.Context) ([]*TaskBinding, error) {
	s.calls.Add(1)
	return nil, s.err
}

// A sync that fails on a local change is retried after the retry wait, not
// at once, so an outage doesn't turn every change into a tight retry loop.
func TestTaskRegistryRetriesFailedSyncNowAfterTheWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		bindings := &countingBindingStore{err: errors.New("database unavailable")}
		manager := taskmanager.New(&memoryTriggerStore{saved: map[string][]taskmanager.TriggerConfig{}}, noExecutions{},
			func(cfg taskmanager.TriggerConfig) taskmanager.Trigger { return idleTrigger{cfg: cfg} }, slog.New(slog.DiscardHandler))
		defer manager.Stop()
		registry := NewTaskRegistry(&fakeInstallationStore{}, bindings, nil, manager)
		go registry.RunSyncs(ctx, time.Hour, TaskSyncRetryWait)
		synctest.Wait()

		registry.SyncNow(ctx)
		synctest.Wait()
		if got := bindings.calls.Load(); got != 1 {
			t.Fatalf("syncs right after a failed SyncNow = %d, want 1", got)
		}
		time.Sleep(TaskSyncRetryWait - time.Second) //nolint:forbidigo // synctest: advances the bubble's fake clock, no real wait
		synctest.Wait()
		if got := bindings.calls.Load(); got != 1 {
			t.Fatalf("syncs before the retry wait = %d, want 1", got)
		}
		time.Sleep(2 * time.Second) //nolint:forbidigo // synctest: advances the bubble's fake clock, no real wait
		synctest.Wait()
		if got := bindings.calls.Load(); got != 2 {
			t.Fatalf("syncs after the retry wait = %d, want 2", got)
		}
	})
}
