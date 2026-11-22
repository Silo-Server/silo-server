package taskmanager

import (
	"context"
	"encoding/json"
	"time"
)

// ExecutionResult records the outcome of a single task execution.
type ExecutionResult struct {
	ID           int64           `json:"id"`
	TaskKey      string          `json:"task_key"`
	StartedAt    time.Time       `json:"started_at"`
	CompletedAt  time.Time       `json:"completed_at"`
	Status       string          `json:"status"` // "completed", "failed", "cancelled"
	ErrorMessage string          `json:"error_message,omitempty"`
	ResultData   json.RawMessage `json:"result_data,omitempty"`
	DurationMs   int64           `json:"duration_ms"`
}

// HistoryPruneResult describes one bounded task history cleanup run. It is
// persisted verbatim as the cleanup task's result_data.
type HistoryPruneResult struct {
	Deleted      int64 `json:"deleted"`
	LimitReached bool  `json:"limit_reached"`
	Skipped      bool  `json:"skipped"`
}

// ExecutionRepository persists task execution history.
type ExecutionRepository interface {
	Insert(ctx context.Context, result ExecutionResult) error
	GetLatest(ctx context.Context, taskKey string) (*ExecutionResult, error)
	List(ctx context.Context, taskKey string, limit int) ([]ExecutionResult, error)
}

// ExecutionStatusFailed is the status of a run that failed.
const ExecutionStatusFailed = "failed"

// InterruptedExecutionMessage is the error message of a run the server
// stopped or lost before the task finished: a crash, a kill, or a shutdown
// that canceled it.
const InterruptedExecutionMessage = "Interrupted: the server stopped before the task finished"

// IsInterrupted reports whether r records a run cut off by its server
// stopping rather than by the task.
func (r *ExecutionResult) IsInterrupted() bool {
	return r != nil && r.Status == ExecutionStatusFailed && r.ErrorMessage == InterruptedExecutionMessage
}

// ExecutionTracker is an optional extension of ExecutionRepository that
// leases each run in progress, so a run whose server dies is still recorded.
// Lease times come from the database clock, so servers with skewed clocks
// agree on a lease's age.
type ExecutionTracker interface {
	// StartRun leases a run in progress and returns the lease ID.
	StartRun(ctx context.Context, taskKey string) (int64, error)
	// RenewRun renews a lease. It reports false when the lease is gone
	// because a server already recorded the run as interrupted.
	RenewRun(ctx context.Context, leaseID int64) (bool, error)
	// FinishRun records a finished run and releases its lease atomically.
	FinishRun(ctx context.Context, leaseID int64, result ExecutionResult) error
	// ReconcileStaleRuns records each run whose lease is older than
	// staleAfter as failed with InterruptedExecutionMessage, releases its
	// lease, and returns the task keys it recorded.
	ReconcileStaleRuns(ctx context.Context, staleAfter time.Duration) ([]string, error)
}
