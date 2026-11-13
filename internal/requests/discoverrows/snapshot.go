package discoverrows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// Card is one title of a stored list: what a discovery card shows. MediaType
// is movie or series.
type Card struct {
	ID           int     `json:"id"`
	MediaType    string  `json:"media_type"`
	Title        string  `json:"title"`
	Overview     string  `json:"overview,omitempty"`
	PosterPath   string  `json:"poster_path,omitempty"`
	BackdropPath string  `json:"backdrop_path,omitempty"`
	ReleaseDate  string  `json:"release_date,omitempty"`
	Popularity   float64 `json:"popularity,omitempty"`
	VoteAverage  float64 `json:"vote_average,omitempty"`
}

func cardOf(r tmdb.MediaResult) Card {
	return Card{
		ID: r.ID, MediaType: r.MediaType, Title: r.Title, Overview: r.Overview, PosterPath: r.PosterPath,
		BackdropPath: r.BackdropPath, ReleaseDate: r.ReleaseDate, Popularity: r.Popularity, VoteAverage: r.VoteAverage,
	}
}

func (c Card) mediaResult() tmdb.MediaResult {
	r := tmdb.MediaResult{
		ID: c.ID, MediaType: c.MediaType, Title: c.Title, Overview: c.Overview, PosterPath: c.PosterPath,
		BackdropPath: c.BackdropPath, ReleaseDate: c.ReleaseDate, Popularity: c.Popularity, VoteAverage: c.VoteAverage,
	}
	if len(c.ReleaseDate) >= 4 {
		r.Year, _ = strconv.Atoi(c.ReleaseDate[:4])
	}
	return r
}

// Snapshot is the stored copy of one list.
type Snapshot struct {
	Key           string
	Cards         []Card
	RefreshedAt   *time.Time
	LastAttemptAt *time.Time
	// LastStatus is refreshing, ok, empty or error.
	LastStatus string
	LastError  string
}

// Source states an admin sees for a list row.
const (
	// StatePending: the list has not been fetched yet.
	StatePending = "pending"
	// StateReady: the stored copy is current as of RefreshedAt.
	StateReady = "ready"
	// StateEmpty: the list was fetched and had no movies or series.
	StateEmpty = "empty"
	// StateError: the last fetch failed; any earlier copy is still shown.
	StateError = "error"
)

// SourceStatus is how a list row's stored copy stands.
type SourceStatus struct {
	State       string
	Entries     int
	RefreshedAt *time.Time
	Error       string
}

func statusOf(s Snapshot, found bool) SourceStatus {
	out := SourceStatus{State: StatePending, Entries: len(s.Cards), RefreshedAt: s.RefreshedAt}
	switch {
	case !found:
	case s.LastStatus == snapshotError:
		out.State, out.Error = StateError, s.LastError
	case s.LastStatus == snapshotEmpty:
		out.State = StateEmpty
	case s.RefreshedAt != nil:
		out.State = StateReady
	}
	return out
}

// Stored last_status values.
const (
	snapshotRefreshing = "refreshing"
	snapshotOK         = "ok"
	snapshotEmpty      = "empty"
	snapshotError      = "error"
)

// ErrLeaseLost means another worker took the refresh over before this one
// saved its result.
var ErrLeaseLost = errors.New("discover list refresh lease lost")

// SnapshotRepository persists request_discover_list_snapshots, one row per
// list source, so every node serves the same copy.
type SnapshotRepository struct {
	pool *pgxpool.Pool
}

// NewSnapshotRepository creates a SnapshotRepository.
func NewSnapshotRepository(pool *pgxpool.Pool) *SnapshotRepository {
	return &SnapshotRepository{pool: pool}
}

// Get returns the stored list; found is false before its first fetch.
func (r *SnapshotRepository) Get(ctx context.Context, key string) (Snapshot, bool, error) {
	var s Snapshot
	var raw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT source_key, entries, refreshed_at, last_attempt_at, last_status, last_error
		FROM request_discover_list_snapshots WHERE source_key = $1`, key).
		Scan(&s.Key, &raw, &s.RefreshedAt, &s.LastAttemptAt, &s.LastStatus, &s.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("getting discover list: %w", err)
	}
	if err := json.Unmarshal(raw, &s.Cards); err != nil {
		return Snapshot{}, false, fmt.Errorf("decoding discover list %s: %w", key, err)
	}
	return s, true, nil
}

// Statuses reports how each list stands, by key. A key with no row is
// pending.
func (r *SnapshotRepository) Statuses(ctx context.Context, keys []string) (map[string]SourceStatus, error) {
	out := make(map[string]SourceStatus, len(keys))
	for _, key := range keys {
		out[key] = statusOf(Snapshot{}, false)
	}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT source_key, entry_count, refreshed_at, last_status, last_error
		FROM request_discover_list_snapshots WHERE source_key = ANY($1)`, keys)
	if err != nil {
		return nil, fmt.Errorf("reading discover list statuses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s Snapshot
		var count int
		if err := rows.Scan(&s.Key, &count, &s.RefreshedAt, &s.LastStatus, &s.LastError); err != nil {
			return nil, fmt.Errorf("reading discover list statuses: %w", err)
		}
		status := statusOf(s, true)
		status.Entries = count
		out[s.Key] = status
	}
	return out, rows.Err()
}

// TryClaimRefresh claims one list for a bounded lease, as seasonal and
// trending snapshots are claimed: no connection is held while the list
// downloads, and a crashed worker's claim expires.
func (r *SnapshotRepository) TryClaimRefresh(ctx context.Context, key string, lease time.Duration) (time.Time, bool, error) {
	var claimedAt time.Time
	err := r.pool.QueryRow(ctx, `
		WITH lease_clock AS (SELECT clock_timestamp() AS claimed_at)
		INSERT INTO request_discover_list_snapshots (source_key, last_attempt_at, last_status, last_error)
		SELECT $1, claimed_at, 'refreshing', '' FROM lease_clock
		ON CONFLICT (source_key) DO UPDATE SET
			last_attempt_at = EXCLUDED.last_attempt_at,
			last_status     = 'refreshing',
			last_error      = ''
		WHERE (request_discover_list_snapshots.last_attempt_at IS NULL
		       OR request_discover_list_snapshots.last_attempt_at <= EXCLUDED.last_attempt_at)
		  AND (request_discover_list_snapshots.last_status <> 'refreshing'
		       OR request_discover_list_snapshots.last_attempt_at <=
		          EXCLUDED.last_attempt_at - ($2::bigint * INTERVAL '1 millisecond'))
		RETURNING last_attempt_at`, key, max(lease.Milliseconds(), 1)).Scan(&claimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("claiming discover list refresh: %w", err)
	}
	return claimedAt, true, nil
}

// SaveSuccess replaces the stored cards after a completed fetch.
func (r *SnapshotRepository) SaveSuccess(ctx context.Context, key string, cards []Card, status string, claimAt, refreshedAt time.Time) error {
	if cards == nil {
		cards = []Card{}
	}
	raw, err := json.Marshal(cards)
	if err != nil {
		return fmt.Errorf("encoding discover list: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE request_discover_list_snapshots SET
			entries = $2, entry_count = $3, refreshed_at = $4, last_status = $5, last_error = ''
		WHERE source_key = $1 AND last_status = 'refreshing' AND last_attempt_at = $6`,
		key, raw, len(cards), refreshedAt, status, claimAt)
	if err != nil {
		return fmt.Errorf("saving discover list: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// RecordFailure records a failed fetch and keeps the last good cards.
func (r *SnapshotRepository) RecordFailure(ctx context.Context, key, message string, claimAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE request_discover_list_snapshots SET last_status = 'error', last_error = $2
		WHERE source_key = $1 AND last_status = 'refreshing' AND last_attempt_at = $3`,
		key, message, claimAt)
	if err != nil {
		return fmt.Errorf("recording discover list failure: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// DeleteUnreferenced removes the lists no row names any more once nothing
// has touched them for olderThan, so a list a row stops showing for a while
// is kept, and a save that names a list again races no delete: the save's
// refresh moves last_attempt_at past the cutoff.
func (r *SnapshotRepository) DeleteUnreferenced(ctx context.Context, keep []string, olderThan time.Duration) (int64, error) {
	if keep == nil {
		keep = []string{}
	}
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM request_discover_list_snapshots
		WHERE NOT (source_key = ANY($1))
		  AND coalesce(last_attempt_at, refreshed_at, 'epoch'::timestamptz) < now() - ($2::bigint * INTERVAL '1 millisecond')`,
		keep, olderThan.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("deleting unused discover lists: %w", err)
	}
	return tag.RowsAffected(), nil
}
