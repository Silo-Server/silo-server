package watchsync

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// UpdateConnectionSettings locks the account/profile/provider row before
// comparing its version or clearing provider-owned watchlist ordering. Only
// preference columns change; tokens and sync state belong to other writers.
// The cleanup callback may commit in a separate user store. If this transaction
// later fails, ordering can already be cleared; callers must not promise replay
// safety or atomic effects across the connection database and user store.
func (r *PostgresRepository) UpdateConnectionSettings(ctx context.Context, provider string, userID int, profileID string, expected *ConnectionVersion, update ConnectionUpdate, before func(Connection) error) (Connection, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Connection{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := r.scanConnection(tx.QueryRow(ctx, `SELECT `+connectionColumns+` FROM watch_provider_connections WHERE provider=$1 AND user_id=$2 AND profile_id=$3 FOR UPDATE`, provider, userID, profileID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrConnectionNotFound
	}
	if err != nil {
		return Connection{}, err
	}
	if expected != nil && (current.ID != expected.ID || !current.UpdatedAt.Equal(expected.UpdatedAt)) {
		return Connection{}, ErrStaleConnection
	}
	if before != nil {
		if err := before(current); err != nil {
			return Connection{}, err
		}
	}
	updated, err := r.scanConnection(tx.QueryRow(ctx, `
 UPDATE watch_provider_connections SET
 import_watched_enabled=COALESCE($2,import_watched_enabled),
 import_progress_enabled=COALESCE($3,import_progress_enabled),
 export_watched_enabled=COALESCE($4,export_watched_enabled),
 export_unwatched_enabled=COALESCE($5,export_unwatched_enabled),
 import_favorites_enabled=COALESCE($6,import_favorites_enabled),
 export_favorites_enabled=COALESCE($7,export_favorites_enabled),
 sync_favorite_removals_enabled=COALESCE($8,sync_favorite_removals_enabled),
 import_watchlist_enabled=COALESCE($9,import_watchlist_enabled),
 export_watchlist_enabled=COALESCE($10,export_watchlist_enabled),
 sync_watchlist_removals_enabled=COALESCE($11,sync_watchlist_removals_enabled),
 sync_watchlist_order_enabled=COALESCE($12,sync_watchlist_order_enabled),
 scrobble_enabled=COALESCE($13,scrobble_enabled),
 import_ratings_enabled=COALESCE($14,import_ratings_enabled),
 export_ratings_enabled=COALESCE($15,export_ratings_enabled),
 updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond')
 WHERE id=$1 RETURNING `+connectionColumns, current.ID, update.ImportWatchedEnabled, update.ImportProgressEnabled, update.ExportWatchedEnabled, update.ExportUnwatchedEnabled, update.ImportFavoritesEnabled, update.ExportFavoritesEnabled, update.SyncFavoriteRemovalsEnabled, update.ImportWatchlistEnabled, update.ExportWatchlistEnabled, update.SyncWatchlistRemovalsEnabled, update.SyncWatchlistOrderEnabled, update.ScrobbleEnabled, update.ImportRatingsEnabled, update.ExportRatingsEnabled))
	if err != nil {
		return Connection{}, fmt.Errorf("update watch provider settings: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Connection{}, err
	}
	return updated, nil
}
