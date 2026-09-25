package opslog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/logstream"
)

// Consumer batch-inserts one node's operational log entries into Postgres and
// fans them out to the admin live tail.
type Consumer struct {
	pool      *pgxpool.Pool
	batchSize int
	interval  time.Duration
	streamHub *logstream.Hub
}

func NewConsumer(pool *pgxpool.Pool, streamHub *logstream.Hub) *Consumer {
	return &Consumer{
		pool:      pool,
		batchSize: 100,
		interval:  2 * time.Second,
		streamHub: streamHub,
	}
}

// Run persists entries from ch until ctx ends, then flushes what is already
// buffered; see logstream.Drain for retries and drops. The consumer's own
// records reach stderr and OTLP but are not captured: a warning about a failed
// batch must not queue another entry behind it.
func (c *Consumer) Run(ctx context.Context, ch <-chan Entry) {
	logstream.Drain[Entry]{
		Stream:   logstream.StreamApp,
		Size:     c.batchSize,
		Interval: c.interval,
		Insert:   c.insertBatch,
		Failed: func(ctx context.Context, f logstream.InsertFailure) {
			if f.RetryIn > 0 {
				slog.WarnContext(ctx, "opslog batch insert failed; retrying", "component", "opslog",
					"entries", f.Entries, "attempt", f.Attempt, "retry_in", f.RetryIn, "error", f.Err)
				return
			}
			slog.ErrorContext(ctx, "opslog batch insert failed; entries dropped", "component", "opslog",
				"entries", f.Entries, "attempt", f.Attempt, "error", f.Err)
		},
	}.Run(withoutCapture(ctx), ch)
}

func (c *Consumer) insertBatch(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("INSERT INTO operational_logs (timestamp, level, component, message, request_id, user_id, session_id, playback_session_id, client_ip, node_id, attrs) VALUES ")

	args := make([]any, 0, len(entries)*11)
	for i, e := range entries {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i * 11
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, NULLIF($%d, '')::inet, $%d, $%d::jsonb)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11)
		attrsJSON, err := json.Marshal(e.Attrs)
		if err != nil {
			attrsJSON = []byte(`{}`)
		}
		// Messages and attrs carry paths, headers and file names.
		args = append(args, e.Timestamp, logstream.SafeText(e.Level), logstream.SafeText(e.Component),
			logstream.SafeText(e.Message), logstream.SafeText(e.RequestID), e.UserID, logstream.SafeText(e.SessionID),
			logstream.SafeText(e.PlaybackSessionID), logstream.SafeText(e.ClientIP), logstream.SafeText(e.NodeID),
			string(logstream.SafeJSON(attrsJSON)))
	}
	b.WriteString(" RETURNING id, timestamp, level, component, message, COALESCE(request_id, ''), user_id, COALESCE(session_id, ''), COALESCE(playback_session_id, ''), COALESCE(client_ip::text, ''), COALESCE(node_id, ''), attrs")

	rows, err := c.pool.Query(ctx, b.String(), args...)
	if err != nil {
		return fmt.Errorf("batch insert: %w", err)
	}
	defer rows.Close()

	inserted := make([]EntryRow, 0, len(entries))
	for rows.Next() {
		var entry EntryRow
		var attrsJSON []byte
		if err := rows.Scan(
			&entry.ID,
			&entry.Timestamp,
			&entry.Level,
			&entry.Component,
			&entry.Message,
			&entry.RequestID,
			&entry.UserID,
			&entry.SessionID,
			&entry.PlaybackSessionID,
			&entry.ClientIP,
			&entry.NodeID,
			&attrsJSON,
		); err != nil {
			return fmt.Errorf("scan inserted operational log row: %w", err)
		}
		if len(attrsJSON) > 0 {
			if err := json.Unmarshal(attrsJSON, &entry.Attrs); err != nil {
				return fmt.Errorf("decode inserted operational log attrs: %w", err)
			}
		}
		inserted = append(inserted, entry)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate inserted operational log rows: %w", err)
	}

	if failed, err := logstream.PublishAppends(c.streamHub, logstream.StreamApp, inserted); failed > 0 {
		slog.WarnContext(ctx, "opslog: failed to publish log stream appends", "component", "opslog", "error", err, "failed", failed, "entries", len(inserted))
	}

	return nil
}
