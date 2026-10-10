package autoscan

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Server setting key and default for autoscan event retention. Every poll of
// every enabled source and every webhook delivery writes an autoscan_events
// row, so without a bound the Activity history grows for as long as autoscan
// runs.
const (
	SettingKeyEventsRetentionDays = "autoscan.events_retention_days"
	DefaultEventsRetentionDays    = 30
)

// SettingsStore is satisfied by *catalog.ServerSettingsRepo.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
}

// LoadEventsRetentionDays reads the retention window, falling back to the
// default when the setting is missing, unreadable, or below one day: a zero
// or negative window would delete every finished event on the next run.
func LoadEventsRetentionDays(ctx context.Context, store SettingsStore) int {
	if store == nil {
		return DefaultEventsRetentionDays
	}
	raw, err := store.Get(ctx, SettingKeyEventsRetentionDays)
	if err != nil {
		return DefaultEventsRetentionDays
	}
	days, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || days < 1 {
		return DefaultEventsRetentionDays
	}
	return days
}

// EventPruneResult describes one bounded autoscan event cleanup run. It is
// persisted verbatim as the cleanup step's result data.
type EventPruneResult struct {
	Deleted      int64 `json:"deleted"`
	LimitReached bool  `json:"limit_reached"`
}

// prunableEventsSQL selects finished events that completed before $1, oldest
// first, from idx_autoscan_events_completed. Running events are skipped: their
// completed_at holds the start time until they finish, and deleting one would
// fail its FinishEvent and let a second poll of the source start.
const prunableEventsSQL = `
	SELECT id
	FROM autoscan_events
	WHERE completed_at < $1
	  AND status <> $2
	ORDER BY completed_at`

// PruneEvents deletes finished autoscan events that completed before cutoff,
// at most batchSize rows per statement and maxBatches statements per call, so
// a large backlog never holds one long transaction. Scan runs of a deleted
// event stay; the foreign key clears their autoscan_event_id. The database
// maintenance task's advisory lock keeps it to one server at a time.
func (r *Repository) PruneEvents(ctx context.Context, cutoff time.Time, batchSize, maxBatches int) (EventPruneResult, error) {
	var result EventPruneResult
	for batch := 0; batch < maxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		tag, err := r.pool.Exec(ctx, `
			DELETE FROM autoscan_events
			WHERE id IN (`+prunableEventsSQL+` LIMIT $3)`,
			cutoff, string(EventStatusRunning), batchSize,
		)
		if err != nil {
			return result, fmt.Errorf("prune autoscan events: %w", err)
		}
		result.Deleted += tag.RowsAffected()
		if tag.RowsAffected() < int64(batchSize) {
			return result, nil
		}
	}
	// The budget ran out on a full batch. Only report the cap when work
	// remains, so a backlog that is an exact multiple of batchSize does not
	// look like a truncated run.
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (`+prunableEventsSQL+` LIMIT 1)`,
		cutoff, string(EventStatusRunning),
	).Scan(&result.LimitReached); err != nil {
		return result, fmt.Errorf("check remaining autoscan events: %w", err)
	}
	return result, nil
}
