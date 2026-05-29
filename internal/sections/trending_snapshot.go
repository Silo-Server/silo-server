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
func (r *TrendingSnapshotRepository) SaveSuccess(ctx context.Context, source, window string, contentIDs []string, entryCount int, status string, at time.Time) error {
	source, window = canonicalTrendingKey(source, window)
	if contentIDs == nil {
		contentIDs = []string{}
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO trending_discover_snapshots
			(source, time_window, content_ids, entry_count, refreshed_at, last_attempt_at, last_status, last_error)
		VALUES ($1, $2, $3, $4, $5, $5, $6, '')
		ON CONFLICT (source, time_window) DO UPDATE SET
			content_ids     = EXCLUDED.content_ids,
			entry_count     = EXCLUDED.entry_count,
			refreshed_at    = EXCLUDED.refreshed_at,
			last_attempt_at = EXCLUDED.last_attempt_at,
			last_status     = EXCLUDED.last_status,
			last_error      = ''`,
		source, window, contentIDs, entryCount, at, status)
	if err != nil {
		return fmt.Errorf("saving trending snapshot: %w", err)
	}
	return nil
}

// RecordAttempt records an attempt that produced no new content (an upstream
// failure or an unconfigured/empty provider) WITHOUT clearing the last-good
// content_ids. status is "error" or "empty". If no row exists yet it inserts a
// placeholder so the attempt is still observable.
func (r *TrendingSnapshotRepository) RecordAttempt(ctx context.Context, source, window, status, message string, at time.Time) error {
	source, window = canonicalTrendingKey(source, window)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO trending_discover_snapshots
			(source, time_window, last_attempt_at, last_status, last_error)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (source, time_window) DO UPDATE SET
			last_attempt_at = EXCLUDED.last_attempt_at,
			last_status     = EXCLUDED.last_status,
			last_error      = EXCLUDED.last_error`,
		source, window, at, status, message)
	if err != nil {
		return fmt.Errorf("recording trending snapshot attempt: %w", err)
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
