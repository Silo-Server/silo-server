package taskmanager

import "time"

// TriggerType identifies the kind of schedule trigger.
type TriggerType string

const (
	TriggerTypeInterval TriggerType = "interval"
	TriggerTypeDaily    TriggerType = "daily"
	TriggerTypeWeekly   TriggerType = "weekly"
	TriggerTypeStartup  TriggerType = "startup"
)

// TriggerConfig is the serializable trigger configuration stored in the database
// and exposed via the API.
type TriggerConfig struct {
	Type         TriggerType `json:"type"`
	IntervalMs   int64       `json:"interval_ms,omitempty"`
	TimeOfDay    string      `json:"time_of_day,omitempty"`
	DayOfWeek    int         `json:"day_of_week,omitempty"`
	MaxRuntimeMs int64       `json:"max_runtime_ms,omitempty"`
}

// Trigger is a live scheduling trigger that fires on a channel.
type Trigger interface {
	Start(lastResult *ExecutionResult)
	Stop()
	NextRunTime() time.Time
	Config() TriggerConfig
	C() <-chan struct{}
}

// SpreadableTrigger is a trigger that accepts its task's start spread. The
// manager sets it before Start.
type SpreadableTrigger interface {
	SetStartSpread(spread time.Duration)
}
