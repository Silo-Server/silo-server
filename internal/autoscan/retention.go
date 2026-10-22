package autoscan

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Server setting key, default and upper bound for autoscan event retention.
// Every poll of every enabled source and every webhook delivery writes an
// autoscan_events row, so without a bound the Activity history grows for as
// long as autoscan runs. The admin settings API accepts 1..MaxEventsRetentionDays.
const (
	SettingKeyEventsRetentionDays = "autoscan.events_retention_days"
	DefaultEventsRetentionDays    = 30
	MaxEventsRetentionDays        = 3650
)

// SettingsStore is satisfied by *catalog.ServerSettingsRepo.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
}

// LoadEventsRetentionDays reads the retention window. A missing value, or one
// below one day, uses the default: a zero or negative window would delete
// every finished event. A value above the maximum, which only a direct
// database write can store, is capped so the cutoff stays a real date. A
// failed read is returned rather than replaced by the default, because a
// shorter window than the admin chose would delete history for good.
func LoadEventsRetentionDays(ctx context.Context, store SettingsStore) (int, error) {
	if store == nil {
		return DefaultEventsRetentionDays, nil
	}
	raw, err := store.Get(ctx, SettingKeyEventsRetentionDays)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", SettingKeyEventsRetentionDays, err)
	}
	days, ok := parseRetentionDays(raw)
	if !ok {
		return DefaultEventsRetentionDays, nil
	}
	return min(days, MaxEventsRetentionDays), nil
}

// parseRetentionDays reports whether raw is a whole number of days, at least
// one.
func parseRetentionDays(raw string) (int, bool) {
	days, err := strconv.Atoi(strings.TrimSpace(raw))
	return days, err == nil && days >= 1
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
// fail its FinishEvent and let a second poll of the source start. Events with
// a scan run still accepted or running are skipped too: follow-up runs inherit
// their event, so a busy scope can keep an old event in use, and clearing the
// link under a run that is finishing would race its follow-up insert.
const prunableEventsSQL = `
	SELECT id
	FROM autoscan_events
	WHERE completed_at < $1
	  AND status <> $2
	  AND NOT EXISTS (
		SELECT 1
		FROM scan_runs sr
		WHERE sr.autoscan_event_id = autoscan_events.id
		  AND sr.status IN ('accepted', 'running')
	  )
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
