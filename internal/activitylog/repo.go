package activitylog

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UserIPEntry is a row in the per-user IP history view.
type UserIPEntry struct {
	ClientIP     string    `json:"client_ip"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	RequestCount int       `json:"request_count"`
}

// IPUserEntry is a row in the per-IP user view.
type IPUserEntry struct {
	UserID       int       `json:"user_id"`
	Username     string    `json:"username"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	RequestCount int       `json:"request_count"`
}

type AuditEntry struct {
	ID                 int64     `json:"id"`
	Timestamp          time.Time `json:"timestamp"`
	ClientIP           string    `json:"client_ip"`
	UserID             *int      `json:"user_id,omitempty"`
	ImpersonatorUserID *int      `json:"impersonator_user_id,omitempty"`
	SessionID          string    `json:"session_id,omitempty"`
	PlaybackSessionID  string    `json:"playback_session_id,omitempty"`
	RequestID          string    `json:"request_id,omitempty"`
	NodeID             string    `json:"node_id,omitempty"`
	Method             string    `json:"method"`
	Path               string    `json:"path"`
	PathPattern        string    `json:"path_pattern,omitempty"`
	StatusCode         int       `json:"status_code"`
	UserAgent          string    `json:"user_agent,omitempty"`
	DurationMs         int       `json:"duration_ms"`
}

type ListOptions struct {
	From              *time.Time
	To                *time.Time
	Method            string
	StatusCode        *int
	PathPrefix        string
	ClientIP          string
	RequestID         string
	UserID            *int
	SessionID         string
	PlaybackSessionID string
	Limit             int
	Cursor            string
}

type ListResult struct {
	Entries    []AuditEntry `json:"entries"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// Repo provides query access to the activity_log table.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new activity log repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// UserIPs returns the IP addresses used by a given user within the lookback window.
func (r *Repo) UserIPs(ctx context.Context, userID int, days int) ([]UserIPEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT client_ip::text, min(timestamp) AS first_seen, max(timestamp) AS last_seen, count(*) AS request_count
		FROM activity_log
		WHERE user_id = $1 AND timestamp > now() - make_interval(days => $2)
		GROUP BY client_ip
		ORDER BY last_seen DESC
	`, userID, days)
	if err != nil {
		return nil, fmt.Errorf("query user IPs: %w", err)
	}
	defer rows.Close()

	var results []UserIPEntry
	for rows.Next() {
		var e UserIPEntry
		if err := rows.Scan(&e.ClientIP, &e.FirstSeen, &e.LastSeen, &e.RequestCount); err != nil {
			return nil, fmt.Errorf("scan user IP row: %w", err)
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

// IPUsers returns the users that have connected from a given IP within the lookback window.
func (r *Repo) IPUsers(ctx context.Context, ip string, days int) ([]IPUserEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT a.user_id, COALESCE(u.username, ''), min(a.timestamp) AS first_seen,
		       max(a.timestamp) AS last_seen, count(*) AS request_count
		FROM activity_log a
		LEFT JOIN users u ON u.id = a.user_id
		WHERE a.client_ip = $1::inet AND a.timestamp > now() - make_interval(days => $2) AND a.user_id IS NOT NULL
		GROUP BY a.user_id, u.username
		ORDER BY last_seen DESC
	`, ip, days)
	if err != nil {
		return nil, fmt.Errorf("query IP users: %w", err)
	}
	defer rows.Close()

	var results []IPUserEntry
	for rows.Next() {
		var e IPUserEntry
		if err := rows.Scan(&e.UserID, &e.Username, &e.FirstSeen, &e.LastSeen, &e.RequestCount); err != nil {
			return nil, fmt.Errorf("scan IP user row: %w", err)
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

func (r *Repo) List(ctx context.Context, opts ListOptions) (ListResult, error) {
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
	if opts.Method != "" {
		conditions = append(conditions, fmt.Sprintf("method = $%d", argIdx))
		args = append(args, strings.ToUpper(opts.Method))
		argIdx++
	}
	if opts.StatusCode != nil {
		conditions = append(conditions, fmt.Sprintf("status_code = $%d", argIdx))
		args = append(args, *opts.StatusCode)
		argIdx++
	}
	if opts.PathPrefix != "" {
		conditions = append(conditions, fmt.Sprintf("path LIKE $%d", argIdx))
		args = append(args, opts.PathPrefix+"%")
		argIdx++
	}
	if opts.ClientIP != "" {
		conditions = append(conditions, fmt.Sprintf("client_ip = $%d::inet", argIdx))
		args = append(args, opts.ClientIP)
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
	if opts.Cursor != "" {
		cursorTs, cursorID, err := decodeCursor(opts.Cursor)
		if err != nil {
			return ListResult{}, err
		}
		conditions = append(conditions, fmt.Sprintf("(timestamp, id) < ($%d, $%d)", argIdx, argIdx+1))
		args = append(args, cursorTs, cursorID)
		argIdx += 2
	}

	query := fmt.Sprintf(`
		SELECT id, timestamp, client_ip::text, user_id, impersonator_user_id, COALESCE(session_id, ''), COALESCE(playback_session_id, ''), COALESCE(request_id, ''), COALESCE(node_id, ''),
		       method, path, COALESCE(path_pattern, ''), COALESCE(status_code, 0), COALESCE(user_agent, ''), COALESCE(duration_ms, 0)
		FROM activity_log
		WHERE %s
		ORDER BY timestamp DESC, id DESC
		LIMIT $%d
	`, strings.Join(conditions, " AND "), argIdx)
	args = append(args, limit+1)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list activity logs: %w", err)
	}
	defer rows.Close()

	entries := make([]AuditEntry, 0, limit+1)
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
			return ListResult{}, fmt.Errorf("scan activity log row: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, fmt.Errorf("iterate activity logs: %w", err)
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

// IPPagePosition resumes a bounded aggregate view at a fixed observation window.
type IPPagePosition struct {
	Until    time.Time
	LastSeen time.Time
	IP       string
	UserID   int
}

func (r *Repo) UserIPsPage(ctx context.Context, userID, days, limit int, pos IPPagePosition) ([]UserIPEntry, bool, error) {
	limit = max(1, min(limit, 200))
	rows, err := r.pool.Query(ctx, `SELECT host(client_ip),min(timestamp),max(timestamp),count(*) FROM activity_log WHERE user_id=$1 AND timestamp>$2::timestamptz-make_interval(days=>$3) AND timestamp<=$2 GROUP BY client_ip HAVING ($4::timestamptz IS NULL OR (max(timestamp),host(client_ip))<($4,$5)) ORDER BY max(timestamp) DESC,host(client_ip) DESC LIMIT $6`, userID, pos.Until, days, optionalIPPageTime(pos.LastSeen), pos.IP, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]UserIPEntry, 0, limit+1)
	for rows.Next() {
		var row UserIPEntry
		if err := rows.Scan(&row.ClientIP, &row.FirstSeen, &row.LastSeen, &row.RequestCount); err != nil {
			return nil, false, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(result) > limit
	if more {
		result = result[:limit]
	}
	return result, more, nil
}
func (r *Repo) IPUsersPage(ctx context.Context, ip string, days, limit int, pos IPPagePosition) ([]IPUserEntry, bool, error) {
	limit = max(1, min(limit, 200))
	rows, err := r.pool.Query(ctx, `SELECT a.user_id,COALESCE(u.username,''),min(a.timestamp),max(a.timestamp),count(*) FROM activity_log a LEFT JOIN users u ON u.id=a.user_id WHERE a.client_ip=$1::inet AND a.timestamp>$2::timestamptz-make_interval(days=>$3) AND a.timestamp<=$2 AND a.user_id IS NOT NULL GROUP BY a.user_id,u.username HAVING ($4::timestamptz IS NULL OR (max(a.timestamp),a.user_id)<($4,$5)) ORDER BY max(a.timestamp) DESC,a.user_id DESC LIMIT $6`, ip, pos.Until, days, optionalIPPageTime(pos.LastSeen), pos.UserID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]IPUserEntry, 0, limit+1)
	for rows.Next() {
		var row IPUserEntry
		if err := rows.Scan(&row.UserID, &row.Username, &row.FirstSeen, &row.LastSeen, &row.RequestCount); err != nil {
			return nil, false, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(result) > limit
	if more {
		result = result[:limit]
	}
	return result, more, nil
}
func optionalIPPageTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
