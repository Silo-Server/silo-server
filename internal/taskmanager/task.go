package taskmanager

import (
	"context"
	"encoding/json"
	"errors"
)

var (
	ErrTaskAlreadyRunning = errors.New("task is already running")
	ErrTaskNotRunning     = errors.New("task is not running")
	ErrTaskNotFound       = errors.New("task not found")
)

// TaskState represents the current runtime state of a task.
type TaskState string

const (
	TaskStateIdle       TaskState = "idle"
	TaskStateRunning    TaskState = "running"
	TaskStateCancelling TaskState = "cancelling"
)

// TaskCategory groups tasks for display.
type TaskCategory string

const (
	TaskCategoryLibrary  TaskCategory = "library"
	TaskCategoryMetadata TaskCategory = "metadata"
	TaskCategorySystem   TaskCategory = "system"
)

// Task is the interface that all background jobs implement.
type Task interface {
	Key() string
	Name() string
	Description() string
	Category() TaskCategory
	IsHidden() bool
	DefaultTriggers() []TriggerConfig
	Execute(ctx context.Context, progress ProgressReporter) error
}

// ScheduledConditionalTask lets a task suppress automatic trigger executions
// when there is no work to do. Manual RunTask calls still execute normally so
// admins can force a check and see a result.
type ScheduledConditionalTask interface {
	ShouldRun(ctx context.Context) (bool, error)
}

// ProgressReporter allows tasks to report progress and result data during execution.
type ProgressReporter interface {
	Report(percent float64, message string)
	SetResultData(data json.RawMessage)
}
