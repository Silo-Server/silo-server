package opslog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type EntryRow struct {
	ID                int64          `json:"id"`
	Timestamp         time.Time      `json:"timestamp"`
	Level             string         `json:"level"`
	Component         string         `json:"component"`
	Message           string         `json:"message"`
	RequestID         string         `json:"request_id,omitempty"`
	UserID            *int           `json:"user_id,omitempty"`
	SessionID         string         `json:"session_id,omitempty"`
	PlaybackSessionID string         `json:"playback_session_id,omitempty"`
	ClientIP          string         `json:"client_ip,omitempty"`
	NodeID            string         `json:"node_id,omitempty"`
	Attrs             map[string]any `json:"attrs,omitempty"`
}

type ListOptions struct {
	From *time.Time
	To   *time.Time
	// Level filters on a single level. Levels filters on any of several and
	// wins when both are set; Level stays for callers that only ever ask for
	// one.
	Level             string
	Levels            []string
	Component         string
	NodeID            string
	RequestID         string
	UserID            *int
	SessionID         string
	PlaybackSessionID string
	Query             string
	Limit             int
	Cursor            string
}

type ListResult struct {
	Entries    []EntryRow `json:"entries"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) List(ctx context.Context, opts ListOptions) (ListResult, error) {
	query, args, limit, err := buildListQuery(opts)
	if err != nil {
		return ListResult{}, err
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list operational logs: %w", err)
	}
	defer rows.Close()

	entries := make([]EntryRow, 0, limit+1)
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
			return ListResult{}, fmt.Errorf("scan operational log row: %w", err)
		}
		if len(attrsJSON) > 0 {
			if err := json.Unmarshal(attrsJSON, &entry.Attrs); err != nil {
				return ListResult{}, fmt.Errorf("decode operational log attrs: %w", err)
			}
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, fmt.Errorf("iterate operational logs: %w", err)
	}

	result := ListResult{}
	if len(entries) > limit {
		last := entries[limit-1]
		result.NextCursor = encodeCursor(last.Timestamp, last.ID)
		entries = entries[:limit]
	}
	result.Entries = entries
	return result, nil
}

// NormalizeLevels lowercases, trims and de-duplicates a level filter, dropping
// empty entries. It returns nil when nothing usable is left so callers can test
// the result for "no level filter".
func NormalizeLevels(levels []string) []string {
	if len(levels) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(levels))
	seen := make(map[string]struct{}, len(levels))
	for _, level := range levels {
		level = strings.ToLower(strings.TrimSpace(level))
		if level == "" {
			continue
		}
		if _, ok := seen[level]; ok {
			continue
		}
		seen[level] = struct{}{}
		normalized = append(normalized, level)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// buildListQuery renders the filtered query and its arguments. It is separate
// from List so the predicate assembly can be tested without a database.
func buildListQuery(opts ListOptions) (string, []any, int, error) {
	limit := opts.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	conditions := []string{"1=1"}
	args := make([]any, 0, 12)
	argIdx := 1

	if opts.From != nil {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", argIdx))
		args = append(args, *opts.From)
		argIdx++
	}
	if opts.To != nil {
		conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", argIdx))
		args = append(args, *opts.To)
		argIdx++
	}
	if levels := NormalizeLevels(opts.Levels); len(levels) > 0 {
		conditions = append(conditions, fmt.Sprintf("level = ANY($%d)", argIdx))
		args = append(args, levels)
		argIdx++
	} else if opts.Level != "" {
		conditions = append(conditions, fmt.Sprintf("level = $%d", argIdx))
		args = append(args, strings.ToLower(opts.Level))
		argIdx++
	}
	if opts.Component != "" {
		conditions = append(conditions, fmt.Sprintf("component = $%d", argIdx))
		args = append(args, opts.Component)
		argIdx++
	}
	if opts.NodeID != "" {
		conditions = append(conditions, fmt.Sprintf("node_id = $%d", argIdx))
		args = append(args, opts.NodeID)
		argIdx++
	}
	if opts.RequestID != "" {
		conditions = append(conditions, fmt.Sprintf("request_id = $%d", argIdx))
		args = append(args, opts.RequestID)
		argIdx++
	}
	if opts.UserID != nil {
		conditions = append(conditions, fmt.Sprintf("user_id = $%d", argIdx))
		args = append(args, *opts.UserID)
		argIdx++
	}
	if opts.SessionID != "" {
		conditions = append(conditions, fmt.Sprintf("session_id = $%d", argIdx))
		args = append(args, opts.SessionID)
		argIdx++
	}
	if opts.PlaybackSessionID != "" {
		conditions = append(conditions, fmt.Sprintf("playback_session_id = $%d", argIdx))
		args = append(args, opts.PlaybackSessionID)
		argIdx++
	}
	if opts.Query != "" {
		conditions = append(conditions, fmt.Sprintf("message ILIKE $%d", argIdx))
		args = append(args, "%"+opts.Query+"%")
		argIdx++
	}
	if opts.Cursor != "" {
		cursorTs, cursorID, err := decodeCursor(opts.Cursor)
		if err != nil {
			return "", nil, 0, err
		}
		conditions = append(conditions, fmt.Sprintf("(timestamp, id) < ($%d, $%d)", argIdx, argIdx+1))
		args = append(args, cursorTs, cursorID)
		argIdx += 2
	}

	query := fmt.Sprintf(`
		SELECT id, timestamp, level, component, message, COALESCE(request_id, ''), user_id, COALESCE(session_id, ''), COALESCE(playback_session_id, ''),
		       COALESCE(client_ip::text, ''), COALESCE(node_id, ''), attrs
		FROM operational_logs
		WHERE %s
		ORDER BY timestamp DESC, id DESC
		LIMIT $%d
	`, strings.Join(conditions, " AND "), argIdx)
	args = append(args, limit+1)

	return query, args, limit, nil
}

func encodeCursor(ts time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%d", ts.UnixNano(), id)))
}

func decodeCursor(cursor string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("decode cursor: %w", err)
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 2 {
		return time.Time{}, 0, fmt.Errorf("invalid cursor")
	}
	var nanos int64
	var id int64
	if _, err := fmt.Sscanf(parts[0], "%d", &nanos); err != nil {
		return time.Time{}, 0, fmt.Errorf("parse cursor timestamp: %w", err)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &id); err != nil {
		return time.Time{}, 0, fmt.Errorf("parse cursor id: %w", err)
	}
	return time.Unix(0, nanos).UTC(), id, nil
}
