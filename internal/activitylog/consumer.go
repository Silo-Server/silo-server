package activitylog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/logstream"
)

// Lua script: atomically LRANGE + LTRIM to pop a batch from the Redis list.
var popBatchScript = redis.NewScript(`
local key = KEYS[1]
local count = tonumber(ARGV[1])
local items = redis.call('LRANGE', key, 0, count - 1)
if #items > 0 then
    redis.call('LTRIM', key, count, -1)
end
return items
`)

// Consumer reads log entries from Redis (or a memory channel) and batch-inserts
// them into PostgreSQL.
type Consumer struct {
	pool       *pgxpool.Pool
	redis      *redis.Client // nil when Redis not configured
	batchSize  int
	interval   time.Duration
	maxRetries int
	streamHub  *logstream.Hub
}

// NewConsumer creates a new activity log consumer.
func NewConsumer(pool *pgxpool.Pool, redisClient *redis.Client, streamHub *logstream.Hub) *Consumer {
	return &Consumer{
		pool:       pool,
		redis:      redisClient,
		batchSize:  100,
		interval:   2 * time.Second,
		maxRetries: 3,
		streamHub:  streamHub,
	}
}

// RunRedis starts the Redis consumer loop. Blocks until ctx is cancelled.
func (c *Consumer) RunRedis(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final drain
			c.drainRedis(context.Background())
			return
		case <-ticker.C:
			c.drainRedis(ctx)
		}
	}
}

func (c *Consumer) drainRedis(ctx context.Context) {
	for {
		entries, err := c.popRedisBatch(ctx)
		if err != nil {
			slog.WarnContext(ctx, "activitylog: Redis pop error", "component", "activitylog", "error", err)
			return
		}
		if len(entries) == 0 {
			return
		}
		if err := c.insertBatchWithRetry(ctx, entries); err != nil {
			slog.ErrorContext(ctx, "activitylog: batch insert failed after retries, dropping batch", "component", "activitylog",
				"error", err, "count", len(entries))
		}
	}
}

func (c *Consumer) popRedisBatch(ctx context.Context) ([]LogEntry, error) {
	result, err := popBatchScript.Run(ctx, c.redis, []string{redisKey}, c.batchSize).StringSlice()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("pop batch script: %w", err)
	}

	entries := make([]LogEntry, 0, len(result))
	for _, raw := range result {
		var entry LogEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			slog.WarnContext(ctx, "activitylog: skipping malformed entry", "component", "activitylog", "error", err)
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (c *Consumer) insertBatchWithRetry(ctx context.Context, entries []LogEntry) error {
	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if err := c.insertBatch(ctx, entries); err != nil {
			lastErr = err
			slog.WarnContext(ctx, "activitylog: batch insert attempt failed", "component", "activitylog",
				"attempt", attempt+1, "error", err)
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		return nil
	}
	return lastErr
}

// RunMemory starts the in-memory consumer loop. Blocks until ctx is cancelled
// or the channel is closed.
func (c *Consumer) RunMemory(ctx context.Context, ch <-chan LogEntry) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	var batch []LogEntry

	for {
		select {
		case <-ctx.Done():
			if len(batch) > 0 {
				if err := c.insertBatch(context.Background(), batch); err != nil {
					slog.WarnContext(ctx, "activity log batch insert failed", "component", "activitylog", "entries", len(batch), "error", err)
				}
			}
			return
		case entry, ok := <-ch:
			if !ok {
				if len(batch) > 0 {
					if err := c.insertBatch(context.Background(), batch); err != nil {
						slog.WarnContext(ctx, "activity log batch insert failed", "component", "activitylog", "entries", len(batch), "error", err)
					}
				}
				return
			}
			batch = append(batch, entry)
			if len(batch) >= c.batchSize {
				if err := c.insertBatch(ctx, batch); err != nil {
					slog.WarnContext(ctx, "activity log batch insert failed", "component", "activitylog", "entries", len(batch), "error", err)
				}
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				if err := c.insertBatch(ctx, batch); err != nil {
					slog.WarnContext(ctx, "activity log batch insert failed", "component", "activitylog", "entries", len(batch), "error", err)
				}
				batch = batch[:0]
			}
		}
	}
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
		args = append(args, e.Timestamp, e.ClientIP, e.UserID, e.ImpersonatorUserID, e.SessionID, e.PlaybackSessionID,
			e.RequestID, e.NodeID, e.Method, e.Path, e.PathPattern, e.StatusCode, e.UserAgent, e.DurationMs)
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

	for _, entry := range inserted {
		if err := c.streamHub.PublishAppend(ctx, logstream.StreamAudit, entry); err != nil {
			slog.WarnContext(ctx, "activitylog: failed to publish log stream append", "component", "activitylog", "error", err, "id", entry.ID)
		}
	}

	return nil
}
