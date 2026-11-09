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

// Normalized keeps the fields Type reads, and MaxRuntimeMs: IntervalMs for
// interval, TimeOfDay for daily and weekly, DayOfWeek for weekly. The task
// schedule store keeps day_of_week only on weekly triggers, so comparing
// normalized configs compares what fires.
func (c TriggerConfig) Normalized() TriggerConfig {
	out := TriggerConfig{Type: c.Type, MaxRuntimeMs: c.MaxRuntimeMs}
	switch c.Type {
	case TriggerTypeInterval:
		out.IntervalMs = c.IntervalMs
	case TriggerTypeDaily:
		out.TimeOfDay = c.TimeOfDay
	case TriggerTypeWeekly:
		out.TimeOfDay, out.DayOfWeek = c.TimeOfDay, c.DayOfWeek
	}
	return out
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
