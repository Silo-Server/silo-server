package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type maintenanceStepStub struct {
	key     string
	err     error
	result  string
	calls   *[]string
	onStart func()
}

func (s maintenanceStepStub) Key() string         { return s.key }
func (s maintenanceStepStub) Name() string        { return "Step " + s.key }
func (s maintenanceStepStub) Description() string { return s.key }
func (s maintenanceStepStub) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (s maintenanceStepStub) IsHidden() bool { return false }
func (s maintenanceStepStub) DefaultTriggers() []taskmanager.TriggerConfig {
	return nil
}

func (s maintenanceStepStub) Execute(_ context.Context, progress taskmanager.ProgressReporter) error {
	*s.calls = append(*s.calls, s.key)
	if s.onStart != nil {
		s.onStart()
	}
	progress.Report(50, "halfway")
	if s.result != "" {
		progress.SetResultData(json.RawMessage(s.result))
	}
	return s.err
}

type maintenanceProgress struct {
	percents []float64
	result   json.RawMessage
}

func (p *maintenanceProgress) Report(percent float64, _ string) {
	p.percents = append(p.percents, percent)
}
func (p *maintenanceProgress) SetResultData(data json.RawMessage) { p.result = data }

func TestDatabaseMaintenanceStopsBetweenStepsWhenCanceled(t *testing.T) {
	var calls []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := NewDatabaseMaintenanceTask(nil,
		maintenanceStepStub{key: "first", calls: &calls, onStart: cancel},
		maintenanceStepStub{key: "second", calls: &calls},
	)

	err := task.Execute(ctx, &maintenanceProgress{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
	if !slices.Equal(calls, []string{"first"}) {
		t.Fatalf("steps ran %v, want to stop after the step that canceled the run", calls)
	}
}

type maintenanceLockStub struct {
	acquired bool
	released bool
}

func (l *maintenanceLockStub) TryAcquire(context.Context) (func(), bool, error) {
	if !l.acquired {
		return nil, false, nil
	}
	return func() { l.released = true }, true, nil
}

func TestDatabaseMaintenanceSkipsWhileAnotherServerHoldsTheLock(t *testing.T) {
	var calls []string
	task := NewDatabaseMaintenanceTask(nil, maintenanceStepStub{key: "first", calls: &calls})
	task.lock = &maintenanceLockStub{acquired: false}

	if err := task.Execute(context.Background(), &maintenanceProgress{}); err != nil {
		t.Fatalf("Execute() error = %v, want a quiet skip", err)
	}
	if len(calls) != 0 {
		t.Fatalf("steps ran %v while another server held the lock", calls)
	}

	lock := &maintenanceLockStub{acquired: true}
	task.lock = lock
	if err := task.Execute(context.Background(), &maintenanceProgress{}); err != nil {
		t.Fatalf("Execute() with the lock error = %v", err)
	}
	if !slices.Equal(calls, []string{"first"}) || !lock.released {
		t.Fatalf("steps ran %v, released=%v; want the step to run and the lock released", calls, lock.released)
	}
}
