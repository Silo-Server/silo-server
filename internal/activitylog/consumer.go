package activitylog

import (
	"context"
	"fmt"
	"log/slog"
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

// insertBatch performs a bulk INSERT into the activity_log table.
func (c *Consumer) insertBatch(ctx context.Context, entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

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
