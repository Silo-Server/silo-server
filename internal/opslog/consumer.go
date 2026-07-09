package opslog

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

var popBatchScript = redis.NewScript(`
local key = KEYS[1]
local count = tonumber(ARGV[1])
local items = redis.call('LRANGE', key, 0, count - 1)
if #items > 0 then
    redis.call('LTRIM', key, count, -1)
end
return items
`)

type Consumer struct {
	pool       *pgxpool.Pool
	redis      *redis.Client
	batchSize  int
	interval   time.Duration
	maxRetries int
	streamHub  *logstream.Hub
}

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

func (c *Consumer) RunRedis(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
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
			slog.WarnContext(ctx, "opslog: Redis pop error", "component", "opslog", "error", err)
			return
		}
		if len(entries) == 0 {
			return
		}
		if err := c.insertBatchWithRetry(ctx, entries); err != nil {
			slog.ErrorContext(ctx, "opslog: batch insert failed after retries, dropping batch", "component", "opslog", "error", err, "count", len(entries))
		}
	}
}

func (c *Consumer) popRedisBatch(ctx context.Context) ([]Entry, error) {
	result, err := popBatchScript.Run(ctx, c.redis, []string{redisKey}, c.batchSize).StringSlice()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("pop batch script: %w", err)
	}

	entries := make([]Entry, 0, len(result))
	for _, raw := range result {
		var entry Entry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			slog.WarnContext(ctx, "opslog: skipping malformed entry", "component", "opslog", "error", err)
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (c *Consumer) insertBatchWithRetry(ctx context.Context, entries []Entry) error {
	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if err := c.insertBatch(ctx, entries); err != nil {
			lastErr = err
			slog.WarnContext(ctx, "opslog: batch insert attempt failed", "component", "opslog", "attempt", attempt+1, "error", err)
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		return nil
	}
	return lastErr
}

func (c *Consumer) RunMemory(ctx context.Context, ch <-chan Entry) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	var batch []Entry

	for {
		select {
		case <-ctx.Done():
			if len(batch) > 0 {
				if err := c.insertBatch(context.Background(), batch); err != nil {
					slog.WarnContext(ctx, "opslog batch insert failed", "component", "opslog", "entries", len(batch), "error", err)
				}
			}
			return
		case entry, ok := <-ch:
			if !ok {
				if len(batch) > 0 {
					if err := c.insertBatch(context.Background(), batch); err != nil {
						slog.WarnContext(ctx, "opslog batch insert failed", "component", "opslog", "entries", len(batch), "error", err)
					}
				}
				return
			}
			batch = append(batch, entry)
			if len(batch) >= c.batchSize {
				if err := c.insertBatch(ctx, batch); err != nil {
					slog.WarnContext(ctx, "opslog batch insert failed", "component", "opslog", "entries", len(batch), "error", err)
				}
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				if err := c.insertBatch(ctx, batch); err != nil {
					slog.WarnContext(ctx, "opslog batch insert failed", "component", "opslog", "entries", len(batch), "error", err)
				}
				batch = batch[:0]
			}
		}
	}
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
		args = append(args, e.Timestamp, e.Level, e.Component, e.Message, e.RequestID, e.UserID, e.SessionID, e.PlaybackSessionID, e.ClientIP, e.NodeID, string(attrsJSON))
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

	for _, entry := range inserted {
		if err := c.streamHub.PublishAppend(ctx, logstream.StreamApp, entry); err != nil {
			slog.WarnContext(ctx, "opslog: failed to publish log stream append", "component", "opslog", "error", err, "id", entry.ID)
		}
	}

	return nil
}
