package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DatabaseMaintenanceTask runs the routine retention sweeps as one scheduled
// task. Each step keeps its own retention settings and runs even when an
// earlier step fails. Steps that must run more often than daily, such as
// operational log and client diagnostics cleanup, stay separate tasks.
//
// Every API process runs the task manager and fires the same daily trigger, so
// an advisory lock lets one server run the sweeps; the others skip.
type DatabaseMaintenanceTask struct {
	steps []taskmanager.Task
	lock  clusterLock
}

// databaseMaintenanceAdvisoryLock spells "SILODBMT".
const databaseMaintenanceAdvisoryLock int64 = 0x53494C4F44424D54

const (
	maintenanceStepCompleted = "completed"
	maintenanceStepFailed    = "failed"
	// databaseMaintenanceTime is off-peak local time, shared with the other
	// daily retention jobs.
	databaseMaintenanceTime = "05:00"
)

type databaseMaintenanceStepResult struct {
	Key    string          `json:"key"`
	Name   string          `json:"name"`
	Status string          `json:"status"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// NewDatabaseMaintenanceTask runs steps in the given order. Nil steps are
// skipped so optional subsystems can pass through unconfigured. A nil pool
// runs without the cluster lock.
func NewDatabaseMaintenanceTask(pool *pgxpool.Pool, steps ...taskmanager.Task) *DatabaseMaintenanceTask {
	t := &DatabaseMaintenanceTask{}
	if pool != nil {
		t.lock = advisoryClusterLock{pool: pool, key: databaseMaintenanceAdvisoryLock}
	}
	for _, step := range steps {
		if step != nil {
			t.steps = append(t.steps, step)
		}
	}
	return t
}

func (t *DatabaseMaintenanceTask) Key() string  { return "database_maintenance" }
func (t *DatabaseMaintenanceTask) Name() string { return "Database Maintenance" }
func (t *DatabaseMaintenanceTask) Description() string {
	return "Prunes expired activity and policy logs, task history, login sessions, search index events, and notifications, and prepares upcoming log partitions"
}
func (t *DatabaseMaintenanceTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *DatabaseMaintenanceTask) IsHidden() bool { return false }

// DefaultTriggers runs off-peak once a day. Startup already creates the upcoming
// log partitions, so a restart needs no maintenance pass, and a fixed time keeps
// rolling deploys of several servers from each sweeping again.
func (t *DatabaseMaintenanceTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: databaseMaintenanceTime}}
}

func (t *DatabaseMaintenanceTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.lock != nil {
		release, acquired, err := t.lock.TryAcquire(ctx)
		if err != nil {
			return fmt.Errorf("acquiring database maintenance lock: %w", err)
		}
		if !acquired {
			progress.Report(100, "Another server is running database maintenance")
			return nil
		}
		defer release()
	}
	results := make([]databaseMaintenanceStepResult, 0, len(t.steps))
	var errs []error
	span := 100 / float64(max(len(t.steps), 1))
	for i, step := range t.steps {
		if ctx.Err() != nil {
			break
		}
		stepProgress := &maintenanceStepProgress{parent: progress, base: float64(i) * span, span: span}
		stepProgress.Report(0, step.Name())
		result := databaseMaintenanceStepResult{Key: step.Key(), Name: step.Name(), Status: maintenanceStepCompleted}
		err := step.Execute(ctx, stepProgress)
		if err != nil && ctx.Err() != nil {
			// Canceled mid-step: the step was interrupted, not failed.
			break
		}
		if err != nil {
			// Task history hides error text from the admin API; the log keeps it.
			slog.WarnContext(ctx, "database maintenance step failed", "step", step.Key(), "error", err)
			result.Status = maintenanceStepFailed
			result.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", step.Name(), err))
		}
		result.Result = stepProgress.resultData
		results = append(results, result)
	}
	if data, err := json.Marshal(struct {
		Steps []databaseMaintenanceStepResult `json:"steps"`
	}{results}); err == nil {
		progress.SetResultData(data)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	progress.Report(100, fmt.Sprintf("Ran %d maintenance steps", len(results)))
	return errors.Join(errs...)
}

// maintenanceStepProgress maps one step's 0-100 progress onto its share of the
// whole run and keeps the step's result data for the combined result.
type maintenanceStepProgress struct {
	parent     taskmanager.ProgressReporter
	base       float64
	span       float64
	resultData json.RawMessage
}

func (p *maintenanceStepProgress) Report(percent float64, message string) {
	p.parent.Report(p.base+min(max(percent, 0), 100)*p.span/100, message)
}

func (p *maintenanceStepProgress) SetResultData(data json.RawMessage) {
	p.resultData = data
}
