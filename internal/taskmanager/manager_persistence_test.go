package taskmanager_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// This store retains an explicit empty schedule, just as durable storage must.
type scheduleStore struct {
	saved     map[string][]taskmanager.TriggerConfig
	loadErr   error
	saveErr   error
	saveCalls int
}

func (s *scheduleStore) GetTriggers(_ context.Context, key string) ([]taskmanager.TriggerConfig, bool, error) {
	configs, exists := s.saved[key]
	return slices.Clone(configs), exists, s.loadErr
}

func (s *scheduleStore) GetOrCreateTriggers(ctx context.Context, key string, defaults []taskmanager.TriggerConfig) ([]taskmanager.TriggerConfig, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	if _, exists := s.saved[key]; !exists {
		if err := s.SetTriggers(ctx, key, defaults); err != nil {
			return nil, err
		}
	}
	return slices.Clone(s.saved[key]), nil
}

func (s *scheduleStore) SetTriggers(_ context.Context, key string, configs []taskmanager.TriggerConfig) error {
	s.saveCalls++
	if s.saveErr != nil {
		return s.saveErr
	}
	if s.saved == nil {
		s.saved = make(map[string][]taskmanager.TriggerConfig)
	}
	s.saved[key] = slices.Clone(configs)
	return nil
}

type scheduleTask struct{ defaults []taskmanager.TriggerConfig }

func (scheduleTask) Key() string                                                 { return "schedule_test" }
func (scheduleTask) Name() string                                                { return "Schedule test" }
func (scheduleTask) Description() string                                         { return "" }
func (scheduleTask) Category() taskmanager.TaskCategory                          { return taskmanager.TaskCategorySystem }
func (scheduleTask) IsHidden() bool                                              { return false }
func (s scheduleTask) DefaultTriggers() []taskmanager.TriggerConfig              { return s.defaults }
func (scheduleTask) Execute(context.Context, taskmanager.ProgressReporter) error { return nil }

type observedDefaultsTask struct {
	scheduleTask
	onDefaults func()
}

func (t observedDefaultsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	t.onDefaults()
	return t.scheduleTask.DefaultTriggers()
}

func TestSavedTaskScheduleDoesNotLoadDefaults(t *testing.T) {
	for _, saved := range [][]taskmanager.TriggerConfig{
		{}, {{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "09:45"}},
	} {
		store := &scheduleStore{saved: map[string][]taskmanager.TriggerConfig{"schedule_test": saved}}
		consulted := false
		m := taskmanager.New(store, scheduleHistory{}, func(c taskmanager.TriggerConfig) taskmanager.Trigger {
			return scheduleTrigger{config: c}
		}, slog.New(slog.DiscardHandler))
		m.Register(observedDefaultsTask{onDefaults: func() { consulted = true }})
		ctx, cancel := context.WithCancel(context.Background())
		m.Start(ctx)
		cancel()
		m.Stop()
		if consulted {
			t.Errorf("loaded defaults for saved schedule %+v", saved)
		}
	}
}

func TestDefaultResolutionPreservesConcurrentScheduleEdit(t *testing.T) {
	for _, saved := range [][]taskmanager.TriggerConfig{
		{}, {{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "09:45"}},
	} {
		store := &scheduleStore{}
		m := taskmanager.New(store, scheduleHistory{}, func(c taskmanager.TriggerConfig) taskmanager.Trigger {
			return scheduleTrigger{config: c}
		}, slog.New(slog.DiscardHandler))
		m.Register(observedDefaultsTask{
			scheduleTask: scheduleTask{defaults: []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}},
			onDefaults: func() {
				// Model an edit after the initial lookup but before atomic seeding.
				if err := store.SetTriggers(context.Background(), "schedule_test", saved); err != nil {
					t.Fatal(err)
				}
			},
		})
		ctx, cancel := context.WithCancel(context.Background())
		m.Start(ctx)
		cancel()
		m.Stop()
		if got := m.GetTaskInfo("schedule_test").Triggers; !slices.Equal(got, saved) {
			t.Errorf("concurrent schedule edit replaced by defaults: %+v, want %+v", got, saved)
		}
	}
}

type scheduleHistory struct{}

func (scheduleHistory) Insert(context.Context, taskmanager.ExecutionResult) error { return nil }
func (scheduleHistory) GetLatest(context.Context, string) (*taskmanager.ExecutionResult, error) {
	return nil, nil
}
func (scheduleHistory) List(context.Context, string, int) ([]taskmanager.ExecutionResult, error) {
	return nil, nil
}

// Scheduling is inert: these tests exercise persistence, not timer delivery.
type scheduleTrigger struct{ config taskmanager.TriggerConfig }

func (scheduleTrigger) Start(*taskmanager.ExecutionResult)  {}
func (scheduleTrigger) Stop()                               {}
func (scheduleTrigger) C() <-chan struct{}                  { return nil }
func (scheduleTrigger) NextRunTime() time.Time              { return time.Time{} }
func (s scheduleTrigger) Config() taskmanager.TriggerConfig { return s.config }

func startScheduleManager(t *testing.T, store *scheduleStore, defaults []taskmanager.TriggerConfig) *taskmanager.TaskManager {
	t.Helper()
	m := taskmanager.New(store, scheduleHistory{}, func(c taskmanager.TriggerConfig) taskmanager.Trigger {
		return scheduleTrigger{config: c}
	}, slog.New(slog.DiscardHandler))
	m.Register(scheduleTask{defaults: defaults})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); m.Stop() })
	m.Start(ctx)
	return m
}

func TestTaskScheduleSurvivesRestartAndChangedDefaults(t *testing.T) {
	defaults := []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "03:30"},
	}
	for _, tc := range []struct {
		name  string
		saved []taskmanager.TriggerConfig
	}{
		{"delete_all", []taskmanager.TriggerConfig{}},
		{"delete_one", defaults[1:]},
		{"add", append(slices.Clone(defaults), taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeWeekly, DayOfWeek: 2, TimeOfDay: "11:15"})},
		{"modify", []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 123000, MaxRuntimeMs: 45000}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &scheduleStore{}
			before := startScheduleManager(t, store, defaults)
			if err := before.UpdateTriggers("schedule_test", tc.saved); err != nil {
				t.Fatal(err)
			}
			before.Stop()
			// A new binary may ship different defaults. Neither old nor new
			// defaults should replace the administrator's saved schedule.
			upgradedDefaults := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "06:00"}}
			after := startScheduleManager(t, store, upgradedDefaults)
			if got := after.GetTaskInfo("schedule_test").Triggers; !slices.Equal(got, tc.saved) {
				t.Fatalf("schedule after restart = %+v, want %+v", got, tc.saved)
			}
		})
	}
}

func TestTaskScheduleLoadFailureDoesNotOverwriteOrRunDefaults(t *testing.T) {
	store := &scheduleStore{loadErr: errors.New("database unavailable")}
	defaults := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
	m := startScheduleManager(t, store, defaults)
	if store.saveCalls != 0 {
		t.Fatalf("load failure attempted %d writes", store.saveCalls)
	}
	if got := m.GetTaskInfo("schedule_test").Triggers; len(got) != 0 {
		t.Fatalf("load failure activated defaults: %+v", got)
	}
	// The task remains editable after startup could not load its schedule.
	store.loadErr = nil
	if err := m.UpdateTriggers("schedule_test", defaults); err != nil {
		t.Fatal(err)
	}
	if got := m.GetTaskInfo("schedule_test").Triggers; !slices.Equal(got, defaults) {
		t.Fatalf("schedule after recovery = %+v", got)
	}
}

func TestTaskScheduleSeedFailureDoesNotRunUnpersistedDefaults(t *testing.T) {
	store := &scheduleStore{saveErr: errors.New("write unavailable")}
	m := startScheduleManager(t, store, []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}})
	if got := m.GetTaskInfo("schedule_test").Triggers; len(got) != 0 {
		t.Fatalf("seed failure activated defaults: %+v", got)
	}
}
