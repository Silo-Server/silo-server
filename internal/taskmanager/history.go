package taskmanager

import (
	"context"
	"encoding/json"
	"time"
)

// ExecutionStatusRunning marks an in-flight execution row kept while a worker
// runs. Completed history queries exclude it; rows whose heartbeat expires are
// reconciled to failed.
const ExecutionStatusRunning = "running"

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

// ExecutionTracker is an optional extension of ExecutionRepository that records
// in-flight task runs with an owner and heartbeat so a process crash leaves a
// failed history entry without disturbing healthy runs on peer servers.
type ExecutionTracker interface {
	Start(ctx context.Context, taskKey string, startedAt time.Time, ownerID string) (int64, error)
	Heartbeat(ctx context.Context, id int64, ownerID string, at time.Time) error
	Finish(ctx context.Context, id int64, ownerID string, result ExecutionResult) (bool, error)
	ReconcileStale(ctx context.Context, staleBefore time.Time, completedAt time.Time, reason string) (int64, error)
}
