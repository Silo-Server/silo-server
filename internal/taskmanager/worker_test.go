package taskmanager

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testTask struct{}

func (testTask) Key() string                                     { return "test" }
func (testTask) Name() string                                    { return "test" }
func (testTask) Description() string                             { return "test" }
func (testTask) Category() TaskCategory                          { return TaskCategorySystem }
func (testTask) IsHidden() bool                                  { return false }
func (testTask) DefaultTriggers() []TriggerConfig                { return nil }
func (testTask) Execute(context.Context, ProgressReporter) error { return nil }

type testTrigger struct {
	ch chan struct{}
}

func newTestTrigger(TriggerConfig) Trigger {
	return &testTrigger{ch: make(chan struct{}, 1)}
}

func (t *testTrigger) Start(*ExecutionResult) {
	t.ch <- struct{}{}
}

func (t *testTrigger) Stop()                  {}
func (t *testTrigger) NextRunTime() time.Time { return time.Time{} }
func (t *testTrigger) Config() TriggerConfig  { return TriggerConfig{} }
func (t *testTrigger) C() <-chan struct{}     { return t.ch }

func TestInitialSetTriggersDoesNotMarkTriggerChanged(t *testing.T) {
	worker := newTaskWorker(testTask{}, nil)
	worker.setTriggers([]TriggerConfig{{Type: TriggerTypeInterval, IntervalMs: 1}}, newTestTrigger, nil, false)

	if worker.triggerChanged.Load() {
		t.Fatal("initial trigger setup should not mark triggers changed")
	}
	select {
	case <-worker.triggerUpdate:
		t.Fatal("initial trigger setup should not queue a trigger update")
	default:
	}
}

type noTriggerRepository struct{}

func (noTriggerRepository) GetTriggers(context.Context, string) ([]TriggerConfig, error) {
	return []TriggerConfig{}, nil
}
func (noTriggerRepository) GetOrCreateTriggers(context.Context, string, []TriggerConfig) ([]TriggerConfig, error) {
	return []TriggerConfig{}, nil
}
func (noTriggerRepository) SetTriggers(context.Context, string, []TriggerConfig) error { return nil }

type noExecutionRepository struct{}

func (noExecutionRepository) Insert(context.Context, ExecutionResult) error { return nil }
func (noExecutionRepository) GetLatest(context.Context, string) (*ExecutionResult, error) {
	return nil, nil
}
func (noExecutionRepository) List(context.Context, string, int) ([]ExecutionResult, error) {
	return nil, nil
}

// pausedTask holds a scheduled firing in its preflight until released.
type pausedTask struct {
	testTask
	preflight chan struct{}
	release   chan struct{}
	executed  chan struct{}
}

func (t pausedTask) ShouldRun(context.Context) (bool, error) {
	close(t.preflight)
	<-t.release
	return true, nil
}

func (t pausedTask) Execute(context.Context, ProgressReporter) error {
	close(t.executed)
	return nil
}

// A firing the trigger loop consumed before Unregister, or a caller that
// looked the worker up before it, must not start a run once the task is gone.
func TestRetiredWorkerRefusesRuns(t *testing.T) {
	task := pausedTask{preflight: make(chan struct{}), release: make(chan struct{}), executed: make(chan struct{})}
	m := New(noTriggerRepository{}, noExecutionRepository{}, newTestTrigger, nil)
	m.Register(task)
	w, err := m.getWorker(task.Key())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.setTriggers([]TriggerConfig{{Type: TriggerTypeInterval, IntervalMs: 1}}, newTestTrigger, nil, false)
	loopDone := make(chan struct{})
	go func() {
		m.triggerLoop(ctx, w)
		close(loopDone)
	}()

	<-task.preflight
	m.Unregister(task.Key())
	close(task.release)
	<-loopDone
	select {
	case <-task.executed:
		t.Fatal("the unregistered task ran")
	default:
	}
	if _, err := w.run(ctx); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("run on a retired worker = %v, want ErrTaskNotFound", err)
	}
	if info := w.info(); info.State != TaskStateIdle {
		t.Fatalf("retired worker state = %s, want idle", info.State)
	}
}

// The schedule store keeps day_of_week only on weekly triggers, so a daily
// trigger installed with a weekday must still match the one read back, or
// every ReloadSchedule would reinstall it and restart its timer.
func TestRunsScheduleComparesNormalizedTriggers(t *testing.T) {
	w := newTaskWorker(testTask{}, nil)
	w.setTriggers([]TriggerConfig{{Type: TriggerTypeDaily, TimeOfDay: "02:00", DayOfWeek: 3}}, newTestTrigger, nil, false)
	if !w.runsSchedule([]TriggerConfig{{Type: TriggerTypeDaily, TimeOfDay: "02:00"}}) {
		t.Fatal("a daily trigger read back without its ignored weekday looks changed")
	}
	if w.runsSchedule([]TriggerConfig{{Type: TriggerTypeDaily, TimeOfDay: "03:00"}}) {
		t.Fatal("a changed time looks unchanged")
	}
}
