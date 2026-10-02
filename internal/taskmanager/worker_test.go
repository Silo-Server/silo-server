package taskmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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

// Execution tests observe the worker boundary, including failures and cancellation.
type observedTask struct {
	testTask
	execute func(context.Context, ProgressReporter) error
}

func (task observedTask) Execute(ctx context.Context, progress ProgressReporter) error {
	return task.execute(ctx, progress)
}

func TestWorkloadObservationTracksTaskExecution(t *testing.T) {
	for _, tc := range []struct {
		name, outcome string
		fail          bool
	}{{"complete", "success", false}, {"failure", "error", true}} {
		t.Run(tc.name, func(t *testing.T) {
			before := workMetricValue(t, "silo_work_attempts_total", "other", tc.outcome)
			task := observedTask{execute: func(ctx context.Context, p ProgressReporter) error {
				if got := workMetricValue(t, "silo_work_active", "other", ""); got != 1 {
					t.Fatalf("active during execution %v", got)
				}
				p.Report(50, "private title is never a metric label")
				if tc.fail {
					return errors.New("secret failure")
				}
				return nil
			}}
			worker := newTaskWorker(task, nil)
			if _, err := worker.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			if after := workMetricValue(t, "silo_work_attempts_total", "other", tc.outcome); after-before != 1 {
				t.Fatalf("completion %v -> %v", before, after)
			}
			if got := workMetricValue(t, "silo_work_active", "other", ""); got != 0 {
				t.Fatalf("active after execution %v", got)
			}
		})
	}
}

func workMetricValue(t *testing.T, name, workload, outcome string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["workload"] != workload || labels["outcome"] != outcome {
				continue
			}
			if metric.Gauge != nil {
				return metric.Gauge.GetValue()
			}
			return metric.Counter.GetValue()
		}
	}
	return 0
}
