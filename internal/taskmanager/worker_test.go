package taskmanager

import (
	"context"
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
