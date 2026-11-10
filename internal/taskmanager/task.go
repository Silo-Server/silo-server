package taskmanager

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrTaskAlreadyRunning = errors.New("task is already running")
	ErrTaskNotRunning     = errors.New("task is not running")
	ErrTaskNotFound       = errors.New("task not found")
	ErrTaskManualOnly     = errors.New("manual-only task does not accept scheduled triggers")
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

// ManualOnlyTask marks a task that may be invoked through RunTask but must
// never accept scheduled triggers.
type ManualOnlyTask interface {
	ManualOnly() bool
}

// StartSpreader spreads a task's scheduled starts across servers that share a
// remote service, so a fleet scheduled at the same local time does not arrive
// at once. Each daily or weekly run starts at its configured time plus a
// random offset in [0, StartSpread()), chosen each time the run is scheduled.
// The spread applies to stored and administrator-edited schedules alike; it
// is not part of TriggerConfig. Manual runs start immediately.
type StartSpreader interface {
	StartSpread() time.Duration
}

// LibraryScopedTask marks a task whose work exists only for one kind of
// library. ListRelevantTasks omits it while no library of that kind exists; it
// still runs on schedule and stays in ListTasks and reachable by key.
type LibraryScopedTask interface {
	// ServesLibrary reports whether a library with this media_folders.type
	// gives the task work.
	ServesLibrary(libraryType string) bool
}

// ProgressReporter allows tasks to report progress and result data during execution.
type ProgressReporter interface {
	Report(percent float64, message string)
	SetResultData(data json.RawMessage)
}
