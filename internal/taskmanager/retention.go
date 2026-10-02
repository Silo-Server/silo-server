package taskmanager

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Server setting keys and defaults for task execution history retention.
//
// The two knobs are independent bounds on the same table: history older than
// RetentionDays is dropped, and no task keeps more than KeepPerTask rows even
// if they are all recent. The newest execution of every task is always kept so
// the admin UI can still show "last run" for a task that has been idle longer
// than the retention window.
const (
	SettingKeyHistoryRetentionDays = "taskmanager.history_retention_days"
	SettingKeyHistoryKeepPerTask   = "taskmanager.history_keep_per_task"

	DefaultHistoryRetentionDays = 30
	DefaultHistoryKeepPerTask   = 1000
)

// SettingsStore is satisfied by *catalog.ServerSettingsRepo.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// HistoryRetention is the resolved retention policy for one cleanup run.
type HistoryRetention struct {
	RetentionDays int
	KeepPerTask   int
}

// SeedHistoryRetentionDefaults writes the default retention settings when no
// value has been persisted yet.
func SeedHistoryRetentionDefaults(ctx context.Context, store SettingsStore) error {
	defaults := map[string]string{
		SettingKeyHistoryRetentionDays: strconv.Itoa(DefaultHistoryRetentionDays),
		SettingKeyHistoryKeepPerTask:   strconv.Itoa(DefaultHistoryKeepPerTask),
	}
	for key, value := range defaults {
		existing, err := store.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("seed task history retention default %s: %w", key, err)
		}
		if existing != "" {
			continue
		}
		if err := store.Set(ctx, key, value); err != nil {
			return fmt.Errorf("seed task history retention default %s: %w", key, err)
		}
	}
	return nil
}

// LoadHistoryRetention reads the retention policy, falling back to the
// defaults when a setting is missing, unreadable, or out of range. Both values
// are clamped to at least 1: a zero or negative retention window would delete
// every execution record on the next boot, and "keep zero per task" would
// fight the invariant that the newest execution always survives.
func LoadHistoryRetention(ctx context.Context, store SettingsStore) HistoryRetention {
	return HistoryRetention{
		RetentionDays: loadPositiveSetting(ctx, store, SettingKeyHistoryRetentionDays, DefaultHistoryRetentionDays),
		KeepPerTask:   loadPositiveSetting(ctx, store, SettingKeyHistoryKeepPerTask, DefaultHistoryKeepPerTask),
	}
}

func loadPositiveSetting(ctx context.Context, store SettingsStore, key string, fallback int) int {
	if store == nil {
		return fallback
	}
	raw, err := store.Get(ctx, key)
	if err != nil {
		return fallback
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}
