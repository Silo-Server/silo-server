package watchsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

// pendingWatchStore keeps provider watches that matched no library item, and
// whole-series and whole-season markers, which cover episodes a library may
// only gain later. Watched imports read providers incrementally, so such a
// watch is never returned again; ImportWatched retries the kept ones on every
// sync. Rows belong to the provider account that returned them. A repository
// without the store imports as before.
type pendingWatchStore interface {
	// ListPendingWatches returns the watches kept for providerAccountID, and
	// drops those of any account but the connection's current one.
	ListPendingWatches(ctx context.Context, connectionID, providerAccountID string) ([]PendingWatch, error)
	// SavePendingWatches keeps watches, replacing a kept one of the same kind
	// and key unless it is newer.
	SavePendingWatches(ctx context.Context, connectionID, providerAccountID string, watches []PendingWatch) error
	// DeletePendingWatches drops the kept watches of the same kind and key
	// that are no newer than the given ones.
	DeletePendingWatches(ctx context.Context, connectionID, providerAccountID string, watches []RemoteWatch) error
	// MatchPendingWatches matches movie, episode, and series watches in
	// batched catalog reads, nil for no match, so a large kept set costs a
	// few queries per sync rather than several per watch.
	MatchPendingWatches(ctx context.Context, records []historyimport.Record) ([]*historyimport.Match, error)
	// CountSeriesLeaves counts the episodes each whole-series marker covers
	// (historyimport.CountSeriesLeaves), in one query.
	CountSeriesLeaves(ctx context.Context, seriesIDs []string, watchedAt []time.Time) ([]int, error)
}

// PendingWatch is a kept watch. AppliedLeaves is how many local episodes an
// aggregate marker covered when it was last applied, and 0 for a watch that
// has not matched.
type PendingWatch struct {
	Watch         RemoteWatch
	AppliedLeaves int
}

// pendingWatchKey identifies a kept watch within a connection. Provider item
// keys are unique per media kind only (a movie and a series can both be
// tmdb:42).
type pendingWatchKey struct{ kind, key string }

func pendingKeyOf(w RemoteWatch) pendingWatchKey {
	return pendingWatchKey{kind: w.Kind, key: w.ProviderItemKey}
}

// keepable reports whether a watch can be kept for a later retry: it needs a
// key to replace and delete it by, and a time to import it at.
func keepable(w RemoteWatch) bool {
	return w.ProviderItemKey != "" && w.Kind != "" && w.LastWatchedAt != nil && !w.LastWatchedAt.IsZero()
}

// aggregateMarker reports whether a watch marks a whole series or season.
func aggregateMarker(w RemoteWatch) bool {
	return w.Kind == historyimport.KindSeries || w.Kind == historyimport.KindSeason
}

// batchMatchable reports whether a kept watch can go through
// MatchPendingWatches. A whole-season marker expands through MatchLeaves
// instead; no plugin sends one today.
func batchMatchable(w RemoteWatch) bool {
	return w.Kind == historyimport.KindMovie || w.Kind == historyimport.KindEpisode || w.Kind == historyimport.KindSeries
}

func (r *PostgresRepository) ListPendingWatches(ctx context.Context, connectionID, providerAccountID string) ([]PendingWatch, error) {
	// Rows of any account but the connection's current one go, judged by the
	// stored connection rather than the caller, so a sync still running for
	// the account it was rebound from cannot drop the new account's rows.
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM watch_provider_pending_watches p
		USING watch_provider_connections c
		WHERE p.connection_id = $1::uuid AND c.id = p.connection_id
		  AND p.provider_account_id <> c.provider_account_id
	`, connectionID); err != nil {
		return nil, fmt.Errorf("drop pending watches of another account: %w", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT watch, applied_leaves FROM watch_provider_pending_watches
		WHERE connection_id = $1::uuid AND provider_account_id = $2
		ORDER BY kind, provider_item_key
	`, connectionID, providerAccountID)
	if err != nil {
		return nil, fmt.Errorf("list pending watches: %w", err)
	}
	defer rows.Close()
	var watches []PendingWatch
	for rows.Next() {
		var raw []byte
		var applied int
		if err := rows.Scan(&raw, &applied); err != nil {
			return nil, fmt.Errorf("scan pending watch: %w", err)
		}
		var watch RemoteWatch
		if err := json.Unmarshal(raw, &watch); err != nil {
			return nil, fmt.Errorf("decode pending watch: %w", err)
		}
		if keepable(watch) {
			watches = append(watches, PendingWatch{Watch: watch, AppliedLeaves: applied})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending watches: %w", err)
	}
	return watches, nil
}

func (r *PostgresRepository) SavePendingWatches(ctx context.Context, connectionID, providerAccountID string, watches []PendingWatch) error {
	kinds, keys, times, encoded, applied, err := pendingWatchColumns(watches)
	if err != nil || len(kinds) == 0 {
		return err
	}
	// Only the connection's current provider account writes, so a sync still
	// running for the account it was rebound from cannot replace the new
	// account's watches. The account is checked under a lock on the
	// connection row, which a rebind's update waits for, and held through the
	// write: checked in the write's own statement, a rebind and the new
	// account's write could both commit between the check and the conflict.
	// Within an account the newest watch per key wins, so an overlapping older
	// sync cannot replace a newer one.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin saving pending watches: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current string
	if err := tx.QueryRow(ctx, `
		SELECT provider_account_id FROM watch_provider_connections
		WHERE id = $1::uuid
		FOR NO KEY UPDATE
	`, connectionID).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lock connection for pending watches: %w", err)
	}
	if current != providerAccountID {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO watch_provider_pending_watches
			(connection_id, provider_account_id, kind, provider_item_key, last_watched_at, watch, applied_leaves, updated_at)
		SELECT DISTINCT ON (w.kind, w.key) $1::uuid, $2, w.kind, w.key, w.watched_at, w.watch, w.applied, now()
		FROM unnest($3::text[], $4::text[], $5::timestamptz[], $6::jsonb[], $7::integer[]) AS w(kind, key, watched_at, watch, applied)
		ORDER BY w.kind, w.key, w.watched_at DESC, w.applied DESC
		ON CONFLICT (connection_id, kind, provider_item_key) DO UPDATE
		SET provider_account_id = EXCLUDED.provider_account_id,
		    last_watched_at = EXCLUDED.last_watched_at,
		    watch = EXCLUDED.watch,
		    applied_leaves = EXCLUDED.applied_leaves,
		    updated_at = now()
		WHERE EXCLUDED.last_watched_at >= watch_provider_pending_watches.last_watched_at
		   OR EXCLUDED.provider_account_id <> watch_provider_pending_watches.provider_account_id
	`, connectionID, providerAccountID, kinds, keys, times, encoded, applied); err != nil {
		return fmt.Errorf("save pending watches: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit pending watches: %w", err)
	}
	return nil
}

func (r *PostgresRepository) DeletePendingWatches(ctx context.Context, connectionID, providerAccountID string, watches []RemoteWatch) error {
	pending := make([]PendingWatch, len(watches))
	for i, watch := range watches {
		pending[i] = PendingWatch{Watch: watch}
	}
	kinds, keys, times, _, _, err := pendingWatchColumns(pending)
	if err != nil || len(kinds) == 0 {
		return err
	}
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM watch_provider_pending_watches p
		USING unnest($3::text[], $4::text[], $5::timestamptz[]) AS w(kind, key, watched_at)
		WHERE p.connection_id = $1::uuid AND p.provider_account_id = $2
		  AND p.kind = w.kind AND p.provider_item_key = w.key
		  AND p.last_watched_at <= w.watched_at
	`, connectionID, providerAccountID, kinds, keys, times); err != nil {
		return fmt.Errorf("delete pending watches: %w", err)
	}
	return nil
}

func (r *PostgresRepository) MatchPendingWatches(ctx context.Context, records []historyimport.Record) ([]*historyimport.Match, error) {
	return historyimport.MatchAllOn(ctx, r.pool, records)
}

func (r *PostgresRepository) CountSeriesLeaves(ctx context.Context, seriesIDs []string, watchedAt []time.Time) ([]int, error) {
	return historyimport.CountSeriesLeaves(ctx, r.pool, seriesIDs, watchedAt)
}

// pendingWatchColumns returns the keepable watches as parallel columns.
func pendingWatchColumns(watches []PendingWatch) (kinds, keys []string, times []time.Time, encoded [][]byte, applied []int, err error) {
	for _, pending := range watches {
		watch := pending.Watch
		if !keepable(watch) {
			continue
		}
		raw, err := json.Marshal(watch)
		if err != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("encode pending watch: %w", err)
		}
		kinds = append(kinds, watch.Kind)
		keys = append(keys, watch.ProviderItemKey)
		times = append(times, watch.LastWatchedAt.UTC())
		encoded = append(encoded, raw)
		applied = append(applied, pending.AppliedLeaves)
	}
	return kinds, keys, times, encoded, applied, nil
}
