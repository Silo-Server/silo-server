package taskmanager

import "time"

// TaskInfo is the API response shape for a task.
type TaskInfo struct {
	Key             string           `json:"key"`
	Name            string           `json:"name"`
	Description     string           `json:"description"`
	Category        TaskCategory     `json:"category"`
	State           TaskState        `json:"state"`
	Progress        float64          `json:"progress"`
	ProgressMessage string           `json:"progress_message,omitempty"`
	ManualOnly      bool             `json:"manual_only"`
	LastExecution   *ExecutionResult `json:"last_execution,omitempty"`
	Triggers        []TriggerConfig  `json:"triggers"`
	NextRunAt       *time.Time       `json:"next_run_at,omitempty"`
}
