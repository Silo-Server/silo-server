package catalog

import (
	"context"
	"fmt"

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
func (r *EpisodeLibraryRepository) reconcileFolderMembership(ctx context.Context, folderID int, episodeIDs []string, restore bool) (int, error) {
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

	args := []any{folderID}
	episodePredicate := ""
	if episodeIDs != nil {
		args = append(args, episodeIDs)
		episodePredicate = " AND el.episode_id = ANY($2::text[])"
	}

	var removed int
	var deletedSeriesIDs []string
	if err := tx.QueryRow(ctx, `
		WITH deleted AS (
			DELETE FROM episode_libraries el
			WHERE el.media_folder_id = $1`+episodePredicate+`
			  AND NOT EXISTS (
				SELECT 1
				FROM media_files mf
				WHERE mf.media_folder_id = el.media_folder_id
				  AND mf.episode_id = el.episode_id
				  AND mf.missing_since IS NULL
			)
			RETURNING el.episode_id
		)
		SELECT COUNT(*)::int,
		       COALESCE(
			       array_agg(DISTINCT e.series_id) FILTER (WHERE e.series_id IS NOT NULL),
			       ARRAY[]::text[]
		       )
		FROM deleted d
		LEFT JOIN episodes e ON e.content_id = d.episode_id
	`, args...).Scan(&removed, &deletedSeriesIDs); err != nil {
		return 0, fmt.Errorf("reconciling episode library membership: %w", err)
	}

	affectedSeriesIDs := append(insertedSeriesIDs, deletedSeriesIDs...)
	if err := RecomputeSeriesLatestEpisodeAdded(ctx, tx, affectedSeriesIDs); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing episode membership reconciliation transaction: %w", err)
	}
	return removed, nil
}
