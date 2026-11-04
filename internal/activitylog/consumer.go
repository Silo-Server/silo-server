package activitylog

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/logstream"
)

// Consumer batch-inserts one node's activity log entries into PostgreSQL and
// fans them out to the admin live tail.
type Consumer struct {
	pool      *pgxpool.Pool
	batchSize int
	interval  time.Duration
	streamHub *logstream.Hub
}

// NewConsumer creates a new activity log consumer.
func NewConsumer(pool *pgxpool.Pool, streamHub *logstream.Hub) *Consumer {
	return &Consumer{
		pool:      pool,
		batchSize: 100,
		interval:  2 * time.Second,
		streamHub: streamHub,
	}
}

// Run persists entries from ch until ctx ends, then flushes what is already
// buffered; see logstream.Drain for retries and drops.
func (c *Consumer) Run(ctx context.Context, ch <-chan LogEntry) {
	logstream.Drain[LogEntry]{
		Stream:   logstream.StreamAudit,
		Size:     c.batchSize,
		Interval: c.interval,
		Insert:   c.insertBatch,
		Failed: func(ctx context.Context, f logstream.InsertFailure) {
			if f.RetryIn > 0 {
				slog.WarnContext(ctx, "activity log batch insert failed; retrying", "component", "activitylog",
					"entries", f.Entries, "attempt", f.Attempt, "retry_in", f.RetryIn, "error", f.Err)
				return
			}
			slog.ErrorContext(ctx, "activity log batch insert failed; entries dropped", "component", "activitylog",
				"entries", f.Entries, "attempt", f.Attempt, "error", f.Err)
		},
	}.Run(ctx, ch)
}

// recordLastActivity moves each account's user_last_activity row forward to
// its newest entry in the batch. Accounts are written in id order so batches
// from different nodes lock the same rows in the same order and cannot
// deadlock; an older timestamp never overwrites a newer one.
func recordLastActivity(ctx context.Context, pool *pgxpool.Pool, entries []LogEntry) error {
	latest := map[int]time.Time{}
	for _, e := range entries {
		if e.UserID == nil {
			continue
		}
		if ts, ok := latest[*e.UserID]; !ok || e.Timestamp.After(ts) {
			latest[*e.UserID] = e.Timestamp
		}
	}
	if len(latest) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(latest))
	times := make([]time.Time, len(ids))
	for i, id := range ids {
		times[i] = latest[id]
	}
	// The join skips accounts deleted since the request was logged.
	_, err := pool.Exec(ctx, `
		INSERT INTO user_last_activity (user_id, last_active_at)
		SELECT v.user_id, v.ts
		FROM unnest($1::bigint[], $2::timestamptz[]) WITH ORDINALITY AS v(user_id, ts, n)
		JOIN users u ON u.id = v.user_id
		ORDER BY v.n
		ON CONFLICT (user_id) DO UPDATE SET last_active_at = EXCLUDED.last_active_at
		WHERE user_last_activity.last_active_at < EXCLUDED.last_active_at`, ids, times)
	if err != nil {
		return fmt.Errorf("recording last activity: %w", err)
	}
	return nil
}

// insertBatch stores a batch in activity_log and records its accounts' last
// activity, including requests whose audited mutation already stored the row.
func (c *Consumer) insertBatch(ctx context.Context, entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	stored := slices.DeleteFunc(slices.Clone(entries), func(e LogEntry) bool { return e.Persisted })
	if len(stored) > 0 {
		if err := c.insertRows(ctx, stored); err != nil {
			return err
		}
	}
	// The rows are stored; a failure here only leaves last activity stale
	// until the account's next batch, so it must not retry the insert.
	if err := recordLastActivity(ctx, c.pool, entries); err != nil {
		slog.WarnContext(ctx, "activitylog: failed to record last activity", "component", "activitylog", "error", err)
	}
	return nil
}

// insertRows performs a bulk INSERT into the activity_log table and publishes
// the stored rows to the live tail.
func (c *Consumer) insertRows(ctx context.Context, entries []LogEntry) error {
	var b strings.Builder
	b.WriteString("INSERT INTO activity_log (timestamp, client_ip, user_id, impersonator_user_id, session_id, playback_session_id, request_id, node_id, method, path, path_pattern, status_code, user_agent, duration_ms) VALUES ")

	args := make([]interface{}, 0, len(entries)*14)
	for i, e := range entries {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i * 14
		fmt.Fprintf(&b, "($%d, $%d::inet, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12, base+13, base+14)
		// Path and User-Agent come from the client.
		args = append(args, e.Timestamp, logstream.SafeText(e.ClientIP), e.UserID, e.ImpersonatorUserID,
			logstream.SafeText(e.SessionID), logstream.SafeText(e.PlaybackSessionID), logstream.SafeText(e.RequestID),
			logstream.SafeText(e.NodeID), logstream.SafeText(e.Method), logstream.SafeText(e.Path),
			logstream.SafeText(e.PathPattern), e.StatusCode, logstream.SafeText(e.UserAgent), e.DurationMs)
	}
	b.WriteString(" RETURNING id, timestamp, client_ip::text, user_id, impersonator_user_id, COALESCE(session_id, ''), COALESCE(playback_session_id, ''), COALESCE(request_id, ''), COALESCE(node_id, ''), method, path, COALESCE(path_pattern, ''), COALESCE(status_code, 0), COALESCE(user_agent, ''), COALESCE(duration_ms, 0)")

	rows, err := c.pool.Query(ctx, b.String(), args...)
	if err != nil {
		return fmt.Errorf("batch insert: %w", err)
	}
	defer rows.Close()

	inserted := make([]AuditEntry, 0, len(entries))
	for rows.Next() {
		var entry AuditEntry
		if err := rows.Scan(
			&entry.ID,
			&entry.Timestamp,
			&entry.ClientIP,
			&entry.UserID,
			&entry.ImpersonatorUserID,
			&entry.SessionID,
			&entry.PlaybackSessionID,
			&entry.RequestID,
			&entry.NodeID,
			&entry.Method,
			&entry.Path,
			&entry.PathPattern,
			&entry.StatusCode,
			&entry.UserAgent,
			&entry.DurationMs,
		); err != nil {
			return fmt.Errorf("scan inserted activity log row: %w", err)
		}
		inserted = append(inserted, entry)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate inserted activity log rows: %w", err)
	}

	if failed, err := logstream.PublishAppends(c.streamHub, logstream.StreamAudit, inserted); failed > 0 {
		slog.WarnContext(ctx, "activitylog: failed to publish log stream appends", "component", "activitylog", "error", err, "failed", failed, "entries", len(inserted))
	}

	return nil
}
