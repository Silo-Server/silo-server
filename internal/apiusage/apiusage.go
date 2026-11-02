// Package apiusage summarizes API requests per sign-in session, so an admin
// can see which clients load the server most. It reads activity_log and leaves
// out what the session quota leaves out: playback and media transfer routes,
// admins, and requests made while impersonating.
package apiusage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

const (
	// Retention is how long hourly summaries are kept.
	Retention = 30 * 24 * time.Hour
	// catchUp bounds how far back a summary pass starts after the task has
	// not run for a while; older hours stay unsummarized.
	catchUp = 48 * time.Hour
	// MaxWindowHours is the longest window TopClients reads.
	MaxWindowHours = 720
	// MaxEndpointHours is the longest window ClientEndpoints reads. It scans
	// one session's activity rows, so its window stays short.
	MaxEndpointHours = 24
	// endpointLimit is how many routes ClientEndpoints returns.
	endpointLimit = 10
)

// ErrSessionNotFound reports a session with no summarized requests in the
// window.
var ErrSessionNotFound = errors.New("no API requests from this session in the window")

// Store reads and writes api_client_usage_hourly.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns nil without a database.
func NewStore(pool *pgxpool.Pool) *Store {
	if pool == nil {
		return nil
	}
	return &Store{pool: pool}
}

// refreshLock spells "SILOAPIU".
const refreshLock int64 = 0x53494C4F41504955

// Refresh summarizes recent activity and prunes old summaries. Every API
// process runs the task that calls it, so it takes a transaction-scoped
// advisory lock first and returns ran false when another server holds it:
// overlapping passes would rescan the same activity log and upsert the same
// rows. The lock and both statements share one connection, so a pool of one
// connection cannot deadlock on it.
func (s *Store) Refresh(ctx context.Context, now time.Time) (written, pruned int64, ran bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, refreshLock).Scan(&ran); err != nil {
			return fmt.Errorf("take API usage summary lock: %w", err)
		}
		if !ran {
			return nil
		}
		var err error
		if written, err = summarize(ctx, tx, now); err != nil {
			return err
		}
		pruned, err = prune(ctx, tx, now)
		return err
	})
	return written, pruned, ran, err
}

// summarize rewrites the hourly rows from the hour before the last summarized
// one, or from catchUp ago if that is later, through now. Rewriting those two
// hours completes the hour the previous pass caught part of and picks up rows
// the activity log wrote late: a row carries its request's start time and is
// written after the request ends. Hours are UTC, as the reads count them. It
// returns how many session-hours it wrote.
func summarize(ctx context.Context, tx pgx.Tx, now time.Time) (int64, error) {
	var last *time.Time
	if err := tx.QueryRow(ctx, `SELECT max(hour) FROM api_client_usage_hourly`).Scan(&last); err != nil {
		return 0, fmt.Errorf("read last summarized hour: %w", err)
	}
	// A last hour later than the current one (another server's clock is
	// ahead, or this one stepped back) is read as the current hour, so the
	// pass neither stalls nor rescans the whole catch-up window.
	from := now.Add(-catchUp).Truncate(time.Hour)
	if last != nil {
		redo := last.Truncate(time.Hour)
		if nowHour := now.Truncate(time.Hour); redo.After(nowHour) {
			redo = nowHour
		}
		if redo = redo.Add(-time.Hour); redo.After(from) {
			from = redo
		}
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO api_client_usage_hourly (hour, session_id, user_id, user_agent, requests, refused)
		SELECT date_trunc('hour', a."timestamp", 'UTC'), a.session_id, max(a.user_id),
		       left(coalesce(mode() WITHIN GROUP (ORDER BY a.user_agent), ''), 256),
		       count(*), count(*) FILTER (WHERE a.status_code = 429)
		FROM activity_log a
		WHERE a."timestamp" >= $1 AND a."timestamp" < $2
		  AND a.session_id IS NOT NULL AND a.session_id <> ''
		  AND a.impersonator_user_id IS NULL
		  AND a.path !~ $3
		  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = a.user_id AND u.role = 'admin')
		GROUP BY 1, 2
		ON CONFLICT (hour, session_id) DO UPDATE
		SET user_id = EXCLUDED.user_id, user_agent = EXCLUDED.user_agent,
		    requests = EXCLUDED.requests, refused = EXCLUDED.refused`,
		from, now, ratelimit.QuotaExemptPathPattern)
	if err != nil {
		return 0, fmt.Errorf("summarize API usage: %w", err)
	}
	return tag.RowsAffected(), nil
}

// prune deletes summaries older than Retention.
func prune(ctx context.Context, tx pgx.Tx, now time.Time) (int64, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM api_client_usage_hourly WHERE hour < $1`, now.Add(-Retention))
	if err != nil {
		return 0, fmt.Errorf("prune API usage: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Client is one sign-in session's API use in a window.
type Client struct {
	SessionID      string
	UserID         int
	Username       string
	DeviceName     string
	DevicePlatform string
	UserAgent      string
	Requests       int64
	Refused        int64
	PeakHour       int64
	FirstHour      time.Time
	LastHour       time.Time
	// SessionActive is false once the session was revoked, expired, or no
	// longer exists.
	SessionActive bool
}

// Clients is the busiest sessions of a window and the window's total.
type Clients struct {
	Hours         int
	From          time.Time
	TotalRequests int64
	Clients       []Client
}

// TopClients returns the limit sessions with the most API requests in the
// last hours hours.
func (s *Store) TopClients(ctx context.Context, now time.Time, hours, limit int) (*Clients, error) {
	from := windowStart(now, hours)
	out := &Clients{Hours: hours, From: from, Clients: []Client{}}
	if err := s.pool.QueryRow(ctx,
		`SELECT coalesce(sum(requests), 0) FROM api_client_usage_hourly WHERE hour >= $1`, from,
	).Scan(&out.TotalRequests); err != nil {
		return nil, fmt.Errorf("read API usage total: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		WITH top AS (
			SELECT session_id, max(user_id) AS user_id, sum(requests) AS requests, sum(refused) AS refused,
			       max(requests) AS peak_hour, min(hour) AS first_hour, max(hour) AS last_hour,
			       (array_agg(user_agent ORDER BY hour DESC))[1] AS user_agent
			FROM api_client_usage_hourly
			WHERE hour >= $1
			GROUP BY session_id
			ORDER BY sum(requests) DESC, session_id
			LIMIT $2
		)
		SELECT top.session_id, coalesce(top.user_id, 0), coalesce(u.username, ''),
		       coalesce(s.device_name, ''), coalesce(s.device_platform, ''), top.user_agent,
		       top.requests, top.refused, top.peak_hour, top.first_hour, top.last_hour,
		       s.id IS NOT NULL AND s.revoked_at IS NULL AND s.expires_at > now()
		FROM top
		LEFT JOIN users u ON u.id = top.user_id
		LEFT JOIN auth_sessions s ON s.id = top.session_id
		ORDER BY top.requests DESC, top.session_id`, from, limit)
	if err != nil {
		return nil, fmt.Errorf("read API clients: %w", err)
	}
	clients, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Client, error) {
		var c Client
		err := row.Scan(&c.SessionID, &c.UserID, &c.Username, &c.DeviceName, &c.DevicePlatform, &c.UserAgent,
			&c.Requests, &c.Refused, &c.PeakHour, &c.FirstHour, &c.LastHour, &c.SessionActive)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("read API clients: %w", err)
	}
	out.Clients = clients
	return out, nil
}

// Endpoint is one route a session called in a window.
type Endpoint struct {
	Method   string
	Route    string
	Requests int64
	Refused  int64
}

// ClientEndpoints returns the routes a session called most in the last hours
// hours, at most MaxEndpointHours. It reads activity_log through the
// account's index and filters the session's rows, so it answers for one
// session at a time over a short window.
func (s *Store) ClientEndpoints(ctx context.Context, now time.Time, sessionID string, hours int) ([]Endpoint, error) {
	hours = min(hours, MaxEndpointHours)
	var userID *int
	err := s.pool.QueryRow(ctx,
		`SELECT max(user_id) FROM api_client_usage_hourly WHERE session_id = $1 AND hour >= $2`, sessionID, windowStart(now, hours),
	).Scan(&userID)
	if err != nil {
		return nil, fmt.Errorf("read API client: %w", err)
	}
	if userID == nil {
		return nil, ErrSessionNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT method, coalesce(nullif(path_pattern, ''), path), count(*), count(*) FILTER (WHERE status_code = 429)
		FROM activity_log
		WHERE user_id = $1 AND "timestamp" >= $2 AND session_id = $3
		  AND impersonator_user_id IS NULL AND path !~ $4
		GROUP BY 1, 2
		ORDER BY 3 DESC, 1, 2
		LIMIT $5`, *userID, now.Add(-time.Duration(hours)*time.Hour), sessionID, ratelimit.QuotaExemptPathPattern, endpointLimit)
	if err != nil {
		return nil, fmt.Errorf("read API client endpoints: %w", err)
	}
	endpoints, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Endpoint, error) {
		var e Endpoint
		err := row.Scan(&e.Method, &e.Route, &e.Requests, &e.Refused)
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("read API client endpoints: %w", err)
	}
	return endpoints, nil
}

// windowStart is the first summarized hour a window of the last hours hours
// reads: the hour that window starts in. A one-hour window at 13:10 reads the
// 12:00 and 13:00 rows, so it is not empty while the current hour is still
// being summarized.
func windowStart(now time.Time, hours int) time.Time {
	return now.Add(-time.Duration(hours) * time.Hour).Truncate(time.Hour)
}
