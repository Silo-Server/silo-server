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

// pendingWatchStore keeps provider watches that matched no library item.
// Watched imports read providers incrementally, so a watch of a title that is
// added to a library later is never returned again; ImportWatched retries the
// kept ones on every sync. Rows belong to the provider account that returned
// them. A repository without the store imports as before.
type pendingWatchStore interface {
	// ListPendingWatches returns the watches kept for providerAccountID, and
	// drops those of any account but the connection's current one.
	ListPendingWatches(ctx context.Context, connectionID, providerAccountID string) ([]RemoteWatch, error)
	// SavePendingWatches keeps watches, replacing a kept one of the same kind
	// and key unless it is newer.
	SavePendingWatches(ctx context.Context, connectionID, providerAccountID string, watches []RemoteWatch) error
	// DeletePendingWatches drops the kept watches of the same kind and key
	// that are no newer than the given ones.
	DeletePendingWatches(ctx context.Context, connectionID, providerAccountID string, watches []RemoteWatch) error
	// MatchPendingWatches matches movie and episode watches in batched
	// catalog reads, nil for no match, so a large kept set costs a few
	// queries per sync rather than several per watch.
	MatchPendingWatches(ctx context.Context, records []historyimport.Record) ([]*historyimport.Match, error)
}

// maxPendingWatches bounds the watches kept per connection. Past it the oldest
// go: a title watched longest ago is the least likely to reach a library.
const maxPendingWatches = 50_000

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

// batchMatchable reports whether a kept watch can go through
// MatchPendingWatches; aggregate markers expand through MatchLeaves instead.
func batchMatchable(w RemoteWatch) bool {
	return w.Kind == historyimport.KindMovie || w.Kind == historyimport.KindEpisode
}

func (r *PostgresRepository) ListPendingWatches(ctx context.Context, connectionID, providerAccountID string) ([]RemoteWatch, error) {
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
	// A caller whose account is no longer the connection's gets nothing, so a
	// sync that a rebind overtook does not import the previous account's kept
	// watches either.
	rows, err := r.pool.Query(ctx, `
		SELECT p.watch FROM watch_provider_pending_watches p
		JOIN watch_provider_connections c ON c.id = p.connection_id AND c.provider_account_id = $2
		WHERE p.connection_id = $1::uuid AND p.provider_account_id = $2
		ORDER BY p.kind, p.provider_item_key
	`, connectionID, providerAccountID)
	if err != nil {
		return nil, fmt.Errorf("list pending watches: %w", err)
	}
	defer rows.Close()
	var watches []RemoteWatch
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan pending watch: %w", err)
		}
		var watch RemoteWatch
		if err := json.Unmarshal(raw, &watch); err != nil {
			return nil, fmt.Errorf("decode pending watch: %w", err)
		}
		if keepable(watch) {
			watches = append(watches, watch)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending watches: %w", err)
	}
	return watches, nil
}

func (r *PostgresRepository) SavePendingWatches(ctx context.Context, connectionID, providerAccountID string, watches []RemoteWatch) error {
	kinds, keys, times, encoded, err := pendingWatchColumns(watches)
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
			(connection_id, provider_account_id, kind, provider_item_key, last_watched_at, watch, updated_at)
		SELECT DISTINCT ON (w.kind, w.key) $1::uuid, $2, w.kind, w.key, w.watched_at, w.watch, now()
		FROM unnest($3::text[], $4::text[], $5::timestamptz[], $6::jsonb[]) AS w(kind, key, watched_at, watch)
		ORDER BY w.kind, w.key, w.watched_at DESC
		ON CONFLICT (connection_id, kind, provider_item_key) DO UPDATE
		SET provider_account_id = EXCLUDED.provider_account_id,
		    last_watched_at = EXCLUDED.last_watched_at,
		    watch = EXCLUDED.watch,
		    updated_at = now()
		WHERE EXCLUDED.last_watched_at >= watch_provider_pending_watches.last_watched_at
		   OR EXCLUDED.provider_account_id <> watch_provider_pending_watches.provider_account_id
	`, connectionID, providerAccountID, kinds, keys, times, encoded); err != nil {
		return fmt.Errorf("save pending watches: %w", err)
	}
	// Keep the newest maxPendingWatches, so a provider history that matches
	// little of the library cannot grow what every sync reloads without bound.
	// The subquery names the oldest row kept; with fewer rows it is NULL and
	// nothing goes.
	if _, err := tx.Exec(ctx, `
		DELETE FROM watch_provider_pending_watches p
		WHERE p.connection_id = $1::uuid
		  AND (p.last_watched_at, p.kind, p.provider_item_key) < (
			SELECT last_watched_at, kind, provider_item_key FROM watch_provider_pending_watches
			WHERE connection_id = $1::uuid
			ORDER BY last_watched_at DESC, kind DESC, provider_item_key DESC
			OFFSET $2 - 1 LIMIT 1
		  )
	`, connectionID, maxPendingWatches); err != nil {
		return fmt.Errorf("trim pending watches: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit pending watches: %w", err)
	}
	return nil
}

func (r *PostgresRepository) DeletePendingWatches(ctx context.Context, connectionID, providerAccountID string, watches []RemoteWatch) error {
	kinds, keys, times, _, err := pendingWatchColumns(watches)
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

func pendingWatchColumns(watches []RemoteWatch) (kinds, keys []string, times []time.Time, encoded [][]byte, err error) {
	for _, watch := range watches {
		if !keepable(watch) {
			continue
		}
		raw, err := json.Marshal(watch)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("encode pending watch: %w", err)
		}
		kinds = append(kinds, watch.Kind)
		keys = append(keys, watch.ProviderItemKey)
		times = append(times, watch.LastWatchedAt.UTC())
		encoded = append(encoded, raw)
	}
	return kinds, keys, times, encoded, nil
}
