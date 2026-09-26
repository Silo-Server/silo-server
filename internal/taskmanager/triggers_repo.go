package taskmanager

import "context"

// TriggerRepository persists task trigger configuration.
type TriggerRepository interface {
	// GetTriggers returns nil only when no schedule has been saved. A saved
	// empty schedule returns a non-nil empty slice.
	GetTriggers(ctx context.Context, taskKey string) ([]TriggerConfig, error)
	// GetOrCreateTriggers persists defaults only if no schedule exists. A
	// concurrent edit takes precedence, including an explicitly empty schedule.
	GetOrCreateTriggers(ctx context.Context, taskKey string, defaults []TriggerConfig) ([]TriggerConfig, error)
	SetTriggers(ctx context.Context, taskKey string, triggers []TriggerConfig) error
}

// Schedule is the persisted configuration, independent of live worker state.
// Revision zero means the task has never had a persisted schedule.
type Schedule struct {
	Revision int64
	Triggers []TriggerConfig
}

type ScheduleConflict struct{ Actual int64 }

func (*ScheduleConflict) Error() string { return "task schedule changed" }

type GuardedTriggerRepository interface {
	GetSchedule(context.Context, string) (Schedule, error)
	ReplaceSchedule(context.Context, string, int64, []TriggerConfig) (Schedule, error)
}
