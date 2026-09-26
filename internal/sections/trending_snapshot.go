package sections

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TrendingSnapshot is the persisted result of one external-trending refresh for
// a canonical (Source, Window). ContentIDs are resolved to library catalog
// content IDs and ordered by trending rank. The list is viewer-agnostic;
// per-viewer access filtering happens at read time.
type TrendingSnapshot struct {
	Source        string
	Window        string
	ContentIDs    []string
	EntryCount    int
	RefreshedAt   *time.Time
	LastAttemptAt *time.Time
	LastStatus    string
	LastError     string
}

// Canonical trending source and window values used as snapshot keys.
const (
	sourceTMDB  = "tmdb"
	sourceTrakt = "trakt"
	windowDay   = "day"
	windowWeek  = "week"
)

// canonicalTrendingKey normalizes a section's configured source/window into the
// snapshot key space. Source is "trakt" only when explicitly set; everything
// else collapses to "tmdb". Trakt ignores the time window, so it is pinned to
// "week" to avoid duplicate identical rows. For TMDB, "day" is honored only
// when explicitly set; anything else is "week".
func canonicalTrendingKey(source, window string) (string, string) {
	if source != sourceTrakt {
		source = sourceTMDB
	}
	if source == sourceTrakt {
		return sourceTrakt, windowWeek
	}
	if window != windowDay {
		window = windowWeek
	}
	return sourceTMDB, window
}

// TrendingSnapshotRepository persists and reads trending_discover_snapshots.
type TrendingSnapshotRepository struct {
	pool *pgxpool.Pool
}

// ErrTrendingRefreshLeaseLost means a newer worker replaced this refresh
// before it attempted to persist its result.
var ErrTrendingRefreshLeaseLost = errors.New("trending refresh lease lost")

// TryClaimRefresh atomically claims one canonical feed for a bounded period.
// Unlike a session advisory lock, the claim does not retain a pool connection
// while the caller performs external HTTP requests or catalog lookups. A
// crashed worker can be replaced after lease expires.
func (r *TrendingSnapshotRepository) TryClaimRefresh(ctx context.Context, source, window string, lease time.Duration) (time.Time, bool, error) {
	source, window = canonicalTrendingKey(source, window)
	leaseMilliseconds := lease.Milliseconds()
	if leaseMilliseconds < 1 {
		leaseMilliseconds = 1
	}
	var claimedAt time.Time
	err := r.pool.QueryRow(ctx, `
		WITH lease_clock AS (
			SELECT clock_timestamp() AS claimed_at
		)
		INSERT INTO trending_discover_snapshots
			(source, time_window, last_attempt_at, last_status, last_error)
		SELECT $1, $2, claimed_at, 'refreshing', ''
		FROM lease_clock
		ON CONFLICT (source, time_window) DO UPDATE SET
			last_attempt_at = EXCLUDED.last_attempt_at,
			last_status     = 'refreshing',
			last_error      = ''
		WHERE (trending_discover_snapshots.last_attempt_at IS NULL
		       OR trending_discover_snapshots.last_attempt_at <= EXCLUDED.last_attempt_at)
		  AND (trending_discover_snapshots.last_status <> 'refreshing'
		       OR trending_discover_snapshots.last_attempt_at <=
		          EXCLUDED.last_attempt_at - ($3::bigint * INTERVAL '1 millisecond'))
		RETURNING last_attempt_at`, source, window, leaseMilliseconds).Scan(&claimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("claiming trending refresh: %w", err)
	}
	return claimedAt, true, nil
}

// NewTrendingSnapshotRepository creates a new TrendingSnapshotRepository.
func NewTrendingSnapshotRepository(pool *pgxpool.Pool) *TrendingSnapshotRepository {
	return &TrendingSnapshotRepository{pool: pool}
}

// Get returns the snapshot for the canonical (source, window). found is false
// when no row exists yet (before the first refresh).
func (r *TrendingSnapshotRepository) Get(ctx context.Context, source, window string) (TrendingSnapshot, bool, error) {
	source, window = canonicalTrendingKey(source, window)
	row := r.pool.QueryRow(ctx, `
		SELECT source, time_window, content_ids, entry_count,
		       refreshed_at, last_attempt_at, last_status, last_error
		FROM trending_discover_snapshots
		WHERE source = $1 AND time_window = $2`, source, window)

	var s TrendingSnapshot
	err := row.Scan(&s.Source, &s.Window, &s.ContentIDs, &s.EntryCount,
		&s.RefreshedAt, &s.LastAttemptAt, &s.LastStatus, &s.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return TrendingSnapshot{}, false, nil
	}
	if err != nil {
		return TrendingSnapshot{}, false, fmt.Errorf("getting trending snapshot: %w", err)
	}
	return s, true, nil
}

// SaveSuccess records a completed refresh, replacing the content list. status is
// "ok" when at least one entry matched the catalog and "empty" when the provider
// returned entries but none matched. Used only when the provider actually
// returned data; see RecordAttempt for the no-data / failure paths.
func (r *TrendingSnapshotRepository) SaveSuccess(ctx context.Context, source, window string, contentIDs []string, entryCount int, status string, claimAt, refreshedAt time.Time) error {
	source, window = canonicalTrendingKey(source, window)
	if contentIDs == nil {
		contentIDs = []string{}
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE trending_discover_snapshots SET
			content_ids  = $3,
			entry_count  = $4,
			refreshed_at = $5,
			last_status  = $6,
			last_error   = ''
		WHERE source = $1 AND time_window = $2
		  AND last_status = 'refreshing' AND last_attempt_at = $7`,
		source, window, contentIDs, entryCount, refreshedAt, status, claimAt)
	if err != nil {
		return fmt.Errorf("saving trending snapshot: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrTrendingRefreshLeaseLost
	}
	return nil
}

// RecordAttempt records an attempt that produced no new content (an upstream
// failure or an unconfigured/empty provider) WITHOUT clearing the last-good
// content_ids. status is "error" or "empty". TryClaimRefresh creates the row
// before work begins, so even the first failed attempt remains observable.
func (r *TrendingSnapshotRepository) RecordAttempt(ctx context.Context, source, window, status, message string, claimAt time.Time) error {
	source, window = canonicalTrendingKey(source, window)
	tag, err := r.pool.Exec(ctx, `
		UPDATE trending_discover_snapshots SET
			last_status = $3,
			last_error  = $4
		WHERE source = $1 AND time_window = $2
		  AND last_status = 'refreshing' AND last_attempt_at = $5`,
		source, window, status, message, claimAt)
	if err != nil {
		return fmt.Errorf("recording trending snapshot attempt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrTrendingRefreshLeaseLost
	}
	return nil
}

// ListAll returns every snapshot row, ordered, for inspection and tests.
func (r *TrendingSnapshotRepository) ListAll(ctx context.Context) ([]TrendingSnapshot, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT source, time_window, content_ids, entry_count,
		       refreshed_at, last_attempt_at, last_status, last_error
		FROM trending_discover_snapshots
		ORDER BY source, time_window`)
	if err != nil {
		return nil, fmt.Errorf("listing trending snapshots: %w", err)
	}
	defer rows.Close()

	var out []TrendingSnapshot
	for rows.Next() {
		var s TrendingSnapshot
		if err := rows.Scan(&s.Source, &s.Window, &s.ContentIDs, &s.EntryCount,
			&s.RefreshedAt, &s.LastAttemptAt, &s.LastStatus, &s.LastError); err != nil {
			return nil, fmt.Errorf("scanning trending snapshot: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
