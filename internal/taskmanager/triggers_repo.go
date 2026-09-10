package taskmanager

import "context"

// TriggerRepository persists task trigger configuration.
type TriggerRepository interface {
	// GetTriggers returns the saved schedule and whether it exists. An empty
	// saved schedule exists; a task with no saved configuration does not.
	GetTriggers(ctx context.Context, taskKey string) ([]TriggerConfig, bool, error)
	// GetOrCreateTriggers atomically loads a saved schedule or persists defaults
	// on first use. A saved empty schedule must remain empty.
	GetOrCreateTriggers(ctx context.Context, taskKey string, defaults []TriggerConfig) ([]TriggerConfig, error)
	SetTriggers(ctx context.Context, taskKey string, triggers []TriggerConfig) error
}
