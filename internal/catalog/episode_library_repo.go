package catalog

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EpisodeLibraryRepository provides maintenance operations for episode_libraries.
type EpisodeLibraryRepository struct {
	pool *pgxpool.Pool
}

// NewEpisodeLibraryRepository creates a new EpisodeLibraryRepository backed by the given pool.
func NewEpisodeLibraryRepository(pool *pgxpool.Pool) *EpisodeLibraryRepository {
	return &EpisodeLibraryRepository{pool: pool}
}

// ReconcileFolderMembership restores missing episode memberships and removes
// memberships for episodes that no longer have any present files in the given
// folder. The returned count is the number of removed stale memberships.
func (r *EpisodeLibraryRepository) ReconcileFolderMembership(ctx context.Context, folderID int) (int, error) {
	return r.reconcileFolderMembership(ctx, folderID, nil, true)
}

// RemoveStaleFolderMemberships removes the folder's stale memberships without
// restoring missing ones. Use it after present-state repair already restored
// them.
func (r *EpisodeLibraryRepository) RemoveStaleFolderMemberships(ctx context.Context, folderID int) (int, error) {
	return r.reconcileFolderMembership(ctx, folderID, nil, false)
}

// RemoveStaleEpisodeMemberships removes stale memberships only for the listed
// episodes. File presence is checked across the whole folder, so a version
// outside a scanned subtree keeps its episode available. An empty list removes
// nothing.
func (r *EpisodeLibraryRepository) RemoveStaleEpisodeMemberships(ctx context.Context, folderID int, episodeIDs []string) (int, error) {
	if len(episodeIDs) == 0 {
		return 0, nil
	}
	return r.reconcileFolderMembership(ctx, folderID, episodeIDs, false)
}

// reconcileFolderMembership limits removal to episodeIDs when it is non-nil;
// the exported methods decide which of the two a caller gets.
//
// Metadata and match writers update the same series rows concurrently, so
// Postgres can pick this transaction as a deadlock victim. It rolls the whole
// transaction back, and a rerun recomputes membership from the current rows,
// so a deadlock is retried rather than failing the scan.
func (r *EpisodeLibraryRepository) reconcileFolderMembership(ctx context.Context, folderID int, episodeIDs []string, restore bool) (int, error) {
	var removed int
	err := retryOnDeadlock(ctx, func() error {
		var err error
		removed, err = r.reconcileFolderMembershipOnce(ctx, folderID, episodeIDs, restore)
		return err
	})
	return removed, err
}

func (r *EpisodeLibraryRepository) reconcileFolderMembershipOnce(ctx context.Context, folderID int, episodeIDs []string, restore bool) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning episode membership reconciliation transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var insertedSeriesIDs []string
	if restore {
		if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO episode_libraries (
				episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id
			)
			SELECT mf.episode_id,
			       mf.media_folder_id,
			       MIN(mf.created_at),
			       (array_agg(mf.first_seen_scan_run_id ORDER BY mf.created_at ASC, mf.id ASC))[1]
			FROM media_files mf
			JOIN episodes e ON e.content_id = mf.episode_id
			WHERE mf.media_folder_id = $1
			  AND mf.missing_since IS NULL
			  AND mf.episode_id IS NOT NULL
			  AND NOT EXISTS (
				SELECT 1 FROM episode_libraries el
				WHERE el.episode_id = mf.episode_id
				  AND el.media_folder_id = mf.media_folder_id
			  )
			GROUP BY mf.episode_id, mf.media_folder_id
			ON CONFLICT (episode_id, media_folder_id) DO NOTHING
			RETURNING episode_id
		)
		SELECT COALESCE(array_agg(DISTINCT e.series_id), ARRAY[]::text[])
		FROM inserted i
		JOIN episodes e ON e.content_id = i.episode_id
	`, folderID).Scan(&insertedSeriesIDs); err != nil {
			return 0, fmt.Errorf("restoring episode library membership: %w", err)
		}
	}

	removed, deletedSeriesIDs, err := deleteStaleEpisodeMemberships(ctx, tx, folderID, episodeIDs)
	if err != nil {
		return 0, err
	}

	affectedSeriesIDs := append(insertedSeriesIDs, deletedSeriesIDs...)
	if err := RecomputeSeriesEpisodeMembershipDenorms(ctx, tx, affectedSeriesIDs); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing episode membership reconciliation transaction: %w", err)
	}
	return removed, nil
}

// noActiveEpisodeFileSQL holds for a membership el whose episode has no
// active file in its folder.
const noActiveEpisodeFileSQL = `NOT EXISTS (
		SELECT 1
		FROM media_files mf
		WHERE mf.media_folder_id = el.media_folder_id
		  AND mf.episode_id = el.episode_id
		  AND mf.missing_since IS NULL
	)`

// deleteStaleEpisodeMemberships removes the folder's memberships whose
// episode has no active file there, limited to episodeIDs when it is non-nil,
// and returns how many it removed and their series. It locks those series
// before deleting: curated rebind and split lock the series row and then
// delete its memberships, and do not retry a deadlock, so deleting first and
// locking in the recompute would take the two in the opposite order.
func deleteStaleEpisodeMemberships(ctx context.Context, tx pgx.Tx, folderID int, episodeIDs []string) (int, []string, error) {
	scope := "el.media_folder_id = $1"
	args := []any{folderID}
	if episodeIDs != nil {
		scope += " AND el.episode_id = ANY($2::text[])"
		args = append(args, episodeIDs)
	}
	var staleEpisodeIDs, staleSeriesIDs []string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(array_agg(el.episode_id), ARRAY[]::text[]),
		       COALESCE(
			       array_agg(DISTINCT e.series_id) FILTER (WHERE e.series_id IS NOT NULL),
			       ARRAY[]::text[]
		       )
		FROM episode_libraries el
		LEFT JOIN episodes e ON e.content_id = el.episode_id
		WHERE `+scope+` AND `+noActiveEpisodeFileSQL, args...).Scan(&staleEpisodeIDs, &staleSeriesIDs); err != nil {
		return 0, nil, fmt.Errorf("finding stale episode library membership: %w", err)
	}
	if len(staleEpisodeIDs) == 0 {
		return 0, nil, nil
	}
	if _, err := tx.Exec(ctx, lockSeriesAirDatesSQL, staleSeriesIDs); err != nil {
		return 0, nil, fmt.Errorf("locking series for episode library membership removal: %w", err)
	}

	// Recheck each membership: a file may have been linked since the select.
	var removed int
	var deletedSeriesIDs []string
	if err := tx.QueryRow(ctx, `
		WITH deleted AS (
			DELETE FROM episode_libraries el
			WHERE el.media_folder_id = $1
			  AND el.episode_id = ANY($2::text[])
			  AND `+noActiveEpisodeFileSQL+`
			RETURNING el.episode_id
		)
		SELECT COUNT(*)::int,
		       COALESCE(
			       array_agg(DISTINCT e.series_id) FILTER (WHERE e.series_id IS NOT NULL),
			       ARRAY[]::text[]
		       )
		FROM deleted d
		LEFT JOIN episodes e ON e.content_id = d.episode_id
	`, folderID, staleEpisodeIDs).Scan(&removed, &deletedSeriesIDs); err != nil {
		return 0, nil, fmt.Errorf("reconciling episode library membership: %w", err)
	}
	return removed, deletedSeriesIDs, nil
}
