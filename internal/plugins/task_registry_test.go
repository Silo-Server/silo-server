package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
	taskrepository "github.com/Silo-Server/silo-server/internal/taskmanager/repository"
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

type fakeTaskScheduler struct {
	tasks     map[string]taskmanager.Task
	registers int
	reloads   []string
	reloadErr error
}

func (s *fakeTaskScheduler) Register(task taskmanager.Task) {
	s.tasks[task.Key()] = task
	s.registers++
}
func (s *fakeTaskScheduler) Unregister(key string) { delete(s.tasks, key) }
func (s *fakeTaskScheduler) ReloadSchedule(_ context.Context, key string) error {
	s.reloads = append(s.reloads, key)
	return s.reloadErr
}

// Sync follows binding and installation changes without a restart. It
// registers new tasks and only reloads the saved schedule of registered ones:
// schedules are written by binding writes, never by a sync.
func TestTaskRegistrySyncAppliesChangesLive(t *testing.T) {
	ctx := context.Background()
	const key = "plugin:1:sync"
	installation := &Installation{ID: 1, PluginID: "example"}
	installations := &fakeInstallationStore{
		installations: []*Installation{installation, {ID: 2, PluginID: "builtin", Kind: KindBuiltin}},
		capabilities: map[int][]*Capability{
			1: {scheduledTaskCapability(t, "sync", "Sync", &pluginv1.ScheduledTaskTrigger{Type: "interval", IntervalMs: 300_000}), {Type: "metadata_provider.v1", ID: "meta"}},
			2: {scheduledTaskCapability(t, "builtin", "", nil)},
		},
	}
	bindings := &fakeTaskBindingStore{}
	scheduler := &fakeTaskScheduler{tasks: map[string]taskmanager.Task{}}
	registry := NewTaskRegistry(installations, bindings, nil, scheduler)
	resync := func() {
		t.Helper()
		if err := registry.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	setBinding := func(enabled bool, trigger map[string]any) {
		bindings.bindings = []*TaskBinding{{InstallationID: 1, CapabilityID: "sync", Enabled: enabled, Trigger: trigger}}
	}
	registered := func() []string { return slices.Sorted(maps.Keys(scheduler.tasks)) }

	resync()
	if got := registered(); !slices.Equal(got, []string{key}) || len(scheduler.reloads) != 0 {
		t.Fatalf("registered = %v and reloaded %v, want only the plugin's task, registered", got, scheduler.reloads)
	}
	task := scheduler.tasks[key]
	if got := task.DefaultTriggers(); !slices.Equal(got, []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 300_000}}) {
		t.Fatalf("unbound task defaults = %+v, want the manifest default", got)
	}

	setBinding(false, map[string]any{})
	resync()
	if got := registered(); len(got) != 0 {
		t.Fatalf("disabled binding left %v registered", got)
	}
	setBinding(true, map[string]any{"type": "daily", "time_of_day": "02:00"})
	resync()
	if got := scheduler.tasks[key].DefaultTriggers(); !slices.Equal(got, []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00"}}) {
		t.Fatalf("bound task defaults = %+v, want the binding trigger", got)
	}

	// A registered task reloads its saved schedule on every sync; a failed
	// reload fails the sync, which RunSyncs retries, and keeps the task.
	registers := scheduler.registers
	scheduler.reloadErr = errors.New("database unavailable")
	if err := registry.Sync(ctx); err == nil {
		t.Fatal("a failed schedule reload must fail the sync")
	}
	scheduler.reloadErr = nil
	if got := registered(); !slices.Equal(got, []string{key}) || !slices.Equal(scheduler.reloads, []string{key}) {
		t.Fatalf("after a failed reload: registered %v, reloaded %v", got, scheduler.reloads)
	}

	// An upgrade renames the task in place.
	task = scheduler.tasks[key]
	installations.capabilities[1] = []*Capability{scheduledTaskCapability(t, "sync", "Sync library", nil)}
	resync()
	if scheduler.registers != registers || task.Name() != "Sync library (example)" {
		t.Fatalf("after upgrade: %d registers, name %q", scheduler.registers-registers, task.Name())
	}

	bindings.err = errors.New("database unavailable")
	if err := registry.Sync(ctx); err == nil {
		t.Fatal("a failed read must fail the sync")
	}
	if got := registered(); !slices.Equal(got, []string{key}) {
		t.Fatalf("a failed read unregistered tasks: %v", got)
	}
	bindings.err = nil

	installations.installations = nil
	resync()
	if got := registered(); len(got) != 0 {
		t.Fatalf("disabled plugin left %v registered", got)
	}
}

// idleTrigger never fires; it records the schedule a task manager installed.
type idleTrigger struct{ cfg taskmanager.TriggerConfig }

func (idleTrigger) Start(*taskmanager.ExecutionResult)  {}
func (idleTrigger) Stop()                               {}
func (idleTrigger) NextRunTime() time.Time              { return time.Time{} }
func (t idleTrigger) Config() taskmanager.TriggerConfig { return t.cfg }
func (idleTrigger) C() <-chan struct{}                  { return nil }

type noExecutions struct{}

func (noExecutions) Insert(context.Context, taskmanager.ExecutionResult) error { return nil }
func (noExecutions) GetLatest(context.Context, string) (*taskmanager.ExecutionResult, error) {
	return nil, nil
}
func (noExecutions) List(context.Context, string, int) ([]taskmanager.ExecutionResult, error) {
	return nil, nil
}

// bindingScheduleHarness runs API servers against one database, each a task
// manager and plugin task registry started as main starts them, for one
// seeded plugin task whose manifest default is hourly.
type bindingScheduleHarness struct {
	t           *testing.T
	pool        *pgxpool.Pool
	id          int
	key         string
	configs     *RuntimeConfigStore
	triggerRepo *taskrepository.PgTriggerRepository
}

// bindingScheduleServer is one API server; built lists every trigger its
// task manager created.
type bindingScheduleServer struct {
	manager  *taskmanager.TaskManager
	registry *TaskRegistry
	mu       sync.Mutex
	built    []taskmanager.TriggerConfig
}

func newBindingScheduleHarness(t *testing.T) *bindingScheduleHarness {
	t.Helper()
	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	id := seedResidentQueryInstallation(t, pool, true)
	capability := scheduledTaskCapability(t, "sync", "Sync", &pluginv1.ScheduledTaskTrigger{Type: "interval", IntervalMs: 3_600_000})
	metadata, err := json.Marshal(capability.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'scheduled_task.v1', 'sync', $2)`, id, metadata); err != nil {
		t.Fatal(err)
	}
	key := pluginTaskKey(id, "sync")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM task_triggers WHERE task_key = $1`, key)
		_, _ = pool.Exec(context.Background(), `DELETE FROM task_schedules WHERE task_key = $1`, key)
	})
	return &bindingScheduleHarness{
		t: t, pool: pool, id: id, key: key,
		configs:     NewRuntimeConfigStore(pool),
		triggerRepo: taskrepository.NewPgTriggerRepository(pool),
	}
}

func (h *bindingScheduleHarness) start() *bindingScheduleServer {
	h.t.Helper()
	s := &bindingScheduleServer{}
	s.manager = taskmanager.New(h.triggerRepo, noExecutions{}, func(cfg taskmanager.TriggerConfig) taskmanager.Trigger {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.built = append(s.built, cfg)
		return idleTrigger{cfg: cfg}
	}, slog.New(slog.DiscardHandler))
	s.registry = NewTaskRegistry(NewInstallationStore(h.pool), h.configs, nil, s.manager)
	serverCtx, cancel := context.WithCancel(h.t.Context())
	h.t.Cleanup(cancel)
	h.t.Cleanup(s.manager.Stop)
	if err := s.registry.Sync(serverCtx); err != nil {
		h.t.Fatal(err)
	}
	s.manager.Start(serverCtx)
	return s
}

// takeBuilt returns the triggers the server built since the last call.
func (s *bindingScheduleServer) takeBuilt() []taskmanager.TriggerConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	built := s.built
	s.built = nil
	return built
}

func (h *bindingScheduleHarness) sync(servers ...*bindingScheduleServer) {
	h.t.Helper()
	for _, s := range servers {
		if err := s.registry.Sync(h.t.Context()); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *bindingScheduleHarness) setBinding(enabled bool, trigger map[string]any) {
	h.t.Helper()
	if err := h.configs.UpsertTaskBinding(h.t.Context(), TaskBinding{InstallationID: h.id, CapabilityID: "sync", Enabled: enabled, Trigger: trigger}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *bindingScheduleHarness) saved() taskmanager.Schedule {
	h.t.Helper()
	schedule, err := h.triggerRepo.GetSchedule(h.t.Context(), h.key)
	if err != nil {
		h.t.Fatal(err)
	}
	return schedule
}

// expect checks the saved schedule and that each server runs it.
func (h *bindingScheduleHarness) expect(step string, want []taskmanager.TriggerConfig, servers ...*bindingScheduleServer) {
	h.t.Helper()
	if got := h.saved().Triggers; !slices.Equal(got, want) {
		h.t.Fatalf("%s: saved schedule = %+v, want %+v", step, got, want)
	}
	for i, s := range servers {
		if got := s.manager.GetTaskInfo(h.key).Triggers; !slices.Equal(got, want) {
			h.t.Fatalf("%s: server %d runs %+v, want %+v", step, i, got, want)
		}
	}
}

// A binding write saves the schedule its trigger implies in its own
// transaction, so the change survives a restart and a server that registers
// the task later, every server only loads it, and an edit on the task page
// made after the write stays until the next binding change.
func TestTaskBindingScheduleAppliesOnceOnEveryServerDB(t *testing.T) {
	h := newBindingScheduleHarness(t)
	hourly := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 3_600_000}}
	daily := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00"}}
	weekly := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeWeekly, TimeOfDay: "04:00", DayOfWeek: 3}}
	startup := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
	dailyBinding := map[string]any{"type": "daily", "time_of_day": "02:00"}

	a := h.start()
	h.expect("unbound", hourly, a)

	// Finding 1: a server that restarts while the binding is disabled, then
	// sees it enabled with a new trigger, runs the new trigger.
	h.setBinding(false, map[string]any{})
	h.sync(a)
	if a.manager.GetTaskInfo(h.key).Key != "" {
		t.Fatal("a disabled binding left the task registered")
	}
	a.manager.Stop()
	a = h.start()
	h.setBinding(true, dailyBinding)
	h.sync(a)
	h.expect("enabled after a restart with a new trigger", daily, a)

	// Finding 2: a binding change applies on A; an edit on the task page
	// follows before B has seen the change. The edit wins on every server,
	// and their syncs write nothing.
	b := h.start()
	h.expect("second server", daily, a, b)
	everyTwoHours := map[string]any{"type": "interval", "interval_ms": 7_200_000}
	h.setBinding(true, everyTwoHours)
	h.sync(a)
	edited, err := a.manager.UpdateSchedule(t.Context(), h.key, h.saved().Revision, weekly)
	if err != nil {
		t.Fatal(err)
	}
	h.sync(b, a)
	h.expect("task page edit", weekly, a, b)
	if got := h.saved().Revision; got != edited.Revision {
		t.Fatalf("syncs rewrote the schedule: revision %d, want %d", got, edited.Revision)
	}
	// The plugin page's switch resends the stored trigger: the edit stays.
	h.setBinding(false, everyTwoHours)
	h.sync(a, b)
	h.setBinding(true, everyTwoHours)
	h.sync(a, b)
	h.expect("toggled", weekly, a, b)

	// Finding 4: re-enabling with a new trigger registers the task on it; the
	// startup trigger it had before is never started.
	h.setBinding(true, map[string]any{"type": "startup"})
	h.sync(a, b)
	h.expect("startup binding", startup, a, b)
	h.setBinding(false, map[string]any{"type": "startup"})
	h.sync(a, b)
	a.takeBuilt()
	b.takeBuilt()
	h.setBinding(true, dailyBinding)
	h.sync(a, b)
	h.expect("re-enabled with a new trigger", daily, a, b)
	for i, s := range []*bindingScheduleServer{a, b} {
		if built := s.takeBuilt(); !slices.Equal(built, daily) {
			t.Fatalf("server %d built triggers %+v on re-enable, want only the new one", i, built)
		}
	}

	// Clearing the binding's trigger returns the task to the manifest default.
	h.setBinding(true, map[string]any{})
	h.sync(a, b)
	h.expect("trigger cleared", hourly, a, b)
}

// A plugin update moves a task whose saved schedule is still the old
// manifest default to the new default, and keeps an edited schedule and a
// binding's own trigger.
func TestPluginUpdateMovesUneditedTaskScheduleDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	store := NewInstallationStore(pool)
	configs := NewRuntimeConfigStore(pool)
	triggerRepo := taskrepository.NewPgTriggerRepository(pool)
	manifestHourly := &pluginv1.ScheduledTaskTrigger{Type: "interval", IntervalMs: 3_600_000}
	manifestDaily := &pluginv1.ScheduledTaskTrigger{Type: "daily", TimeOfDay: "02:00"}
	hourly := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 3_600_000}}
	startup := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
	weekly := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeWeekly, TimeOfDay: "04:00", DayOfWeek: 3}}
	for _, tc := range []struct {
		name          string
		before, after *pluginv1.ScheduledTaskTrigger
		binding       map[string]any
		unsaved       bool // the task has no saved schedule yet
		edit          []taskmanager.TriggerConfig
		want          []taskmanager.TriggerConfig
	}{
		{name: "startup moves to an added default", after: manifestHourly, want: hourly},
		{name: "unsaved task gets the changed default", before: manifestDaily, after: manifestHourly, unsaved: true, want: hourly},
		{name: "edited schedule stays", after: manifestHourly, edit: weekly, want: weekly},
		// The binding's trigger is the old default, so only the binding check keeps it.
		{name: "binding trigger stays", after: manifestHourly, binding: map[string]any{"type": "startup"}, want: startup},
		{name: "old default moves to the changed default", before: manifestDaily, after: manifestHourly, want: hourly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			id := seedResidentQueryInstallation(t, pool, true)
			key := pluginTaskKey(id, "sync")
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM task_triggers WHERE task_key = $1`, key)
				_, _ = pool.Exec(context.Background(), `DELETE FROM task_schedules WHERE task_key = $1`, key)
			})
			release := func(version string, trigger *pluginv1.ScheduledTaskTrigger) *Capability {
				capability := scheduledTaskCapability(t, "sync", "Sync", trigger)
				if err := store.Update(ctx, id, UpdateInstallationInput{Version: &version, Capabilities: []Capability{*capability}}); err != nil {
					t.Fatal(err)
				}
				return capability
			}

			installed := release("1", tc.before)
			binding := &TaskBinding{InstallationID: id, CapabilityID: "sync", Enabled: true, Trigger: tc.binding}
			if tc.binding != nil {
				if err := configs.UpsertTaskBinding(ctx, *binding); err != nil {
					t.Fatal(err)
				}
			}
			// The task manager saves the default when the task first registers.
			seed := func() {
				if _, err := triggerRepo.GetOrCreateTriggers(ctx, key, pluginTaskTriggers(binding, installed)); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.unsaved {
				seed()
			}
			if tc.edit != nil {
				if err := triggerRepo.SetTriggers(ctx, key, tc.edit); err != nil {
					t.Fatal(err)
				}
			}

			release("2", tc.after)
			// A server whose registry scan read the old manifest seeds its default late.
			seed()
			if got, err := triggerRepo.GetTriggers(ctx, key); err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("saved schedule after the update = %+v (%v), want %+v", got, err, tc.want)
			}
		})
	}
}

// A task's first binding, written while a plugin update moves its schedule
// to a changed default, waits for the update and then saves its own trigger,
// so the update cannot overwrite a schedule the binding set.
func TestFirstTaskBindingWaitsForPluginUpdateDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	store := NewInstallationStore(pool)
	triggerRepo := taskrepository.NewPgTriggerRepository(pool)
	id := seedResidentQueryInstallation(t, pool, true)
	key := pluginTaskKey(id, "sync")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM task_triggers WHERE task_key = $1`, key)
		_, _ = pool.Exec(context.Background(), `DELETE FROM task_schedules WHERE task_key = $1`, key)
	})
	version := "1"
	if err := store.Update(ctx, id, UpdateInstallationInput{Version: &version, Capabilities: []Capability{*scheduledTaskCapability(t, "sync", "Sync", nil)}}); err != nil {
		t.Fatal(err)
	}
	startup := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
	if _, err := triggerRepo.GetOrCreateTriggers(ctx, key, startup); err != nil {
		t.Fatal(err)
	}

	// The update's transaction, as InstallationStore.Update runs it.
	update, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = update.Rollback(context.WithoutCancel(ctx)) }()
	var holder int
	if err := update.QueryRow(ctx, `SELECT pg_backend_pid() FROM plugin_installations WHERE id = $1 FOR NO KEY UPDATE`, id).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- NewRuntimeConfigStore(pool).UpsertTaskBinding(ctx, TaskBinding{InstallationID: id, CapabilityID: "sync", Enabled: true, Trigger: map[string]any{"type": "startup"}})
	}()
	waitUntilBlockedBy(t, pool, holder, done, "binding write")
	previous, err := store.WithTx(update).ListCapabilities(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	hourly := []Capability{*scheduledTaskCapability(t, "sync", "Sync", &pluginv1.ScheduledTaskTrigger{Type: "interval", IntervalMs: 3_600_000})}
	if err := store.replaceCapabilities(ctx, update, id, hourly); err != nil {
		t.Fatal(err)
	}
	if err := followManifestDefaultSchedules(ctx, update, id, previous, hourly); err != nil {
		t.Fatal(err)
	}
	if err := update.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, err := triggerRepo.GetTriggers(ctx, key); err != nil || !slices.Equal(got, startup) {
		t.Fatalf("saved schedule = %+v (%v), want the binding's %+v", got, err, startup)
	}
}

// waitUntilBlockedBy returns once a session waits on a lock the backend
// holder holds, failing if what, whose result arrives on done, finishes
// first or nothing waits within five seconds.
func waitUntilBlockedBy(t *testing.T, pool *pgxpool.Pool, holder int, done <-chan error, what string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for waiting := false; !waiting; {
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, holder).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			t.Fatalf("%s finished without waiting: %v", what, err)
		case <-deadline.C:
			t.Fatalf("%s never waited", what)
		case <-tick.C:
		}
	}
}

// A schedule another transaction saves while a plugin update finds none,
// such as a server seeding the old default or an edit, is waited for: the
// update then moves the old default and keeps the edit.
func TestPluginUpdateWaitsForAScheduleBeingSavedDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	store := NewInstallationStore(pool)
	triggerRepo := taskrepository.NewPgTriggerRepository(pool)
	daily := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00"}}
	hourly := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 3_600_000}}
	weekly := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeWeekly, TimeOfDay: "04:00", DayOfWeek: 3}}
	for _, tc := range []struct {
		name         string
		saving, want []taskmanager.TriggerConfig
	}{
		{name: "old default moves", saving: daily, want: hourly},
		{name: "edit stays", saving: weekly, want: weekly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			id := seedResidentQueryInstallation(t, pool, true)
			key := pluginTaskKey(id, "sync")
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM task_triggers WHERE task_key = $1`, key)
				_, _ = pool.Exec(context.Background(), `DELETE FROM task_schedules WHERE task_key = $1`, key)
			})
			release := func(version string, trigger *pluginv1.ScheduledTaskTrigger) error {
				return store.Update(ctx, id, UpdateInstallationInput{Version: &version, Capabilities: []Capability{*scheduledTaskCapability(t, "sync", "Sync", trigger)}})
			}
			if err := release("1", &pluginv1.ScheduledTaskTrigger{Type: "daily", TimeOfDay: "02:00"}); err != nil {
				t.Fatal(err)
			}

			saving, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = saving.Rollback(context.WithoutCancel(ctx)) }()
			var holder int
			if err := saving.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
				t.Fatal(err)
			}
			if err := taskrepository.ReplaceScheduleInTx(ctx, saving, key, tc.saving); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- release("2", &pluginv1.ScheduledTaskTrigger{Type: "interval", IntervalMs: 3_600_000})
			}()
			waitUntilBlockedBy(t, pool, holder, done, "plugin update")
			if err := saving.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if got, err := triggerRepo.GetTriggers(ctx, key); err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("saved schedule = %+v (%v), want %+v", got, err, tc.want)
			}
		})
	}
}
