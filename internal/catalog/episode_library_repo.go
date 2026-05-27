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
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at)
		SELECT mf.episode_id, mf.media_folder_id, MIN(mf.created_at)
		FROM media_files mf
		JOIN episodes e ON e.content_id = mf.episode_id
		WHERE mf.media_folder_id = $1
		  AND mf.missing_since IS NULL
		  AND mf.episode_id IS NOT NULL
		GROUP BY mf.episode_id, mf.media_folder_id
		ON CONFLICT (episode_id, media_folder_id) DO NOTHING
	`, folderID); err != nil {
		return 0, fmt.Errorf("restoring episode library membership: %w", err)
	}

	tag, err := r.pool.Exec(ctx, `
		DELETE FROM episode_libraries el
		WHERE el.media_folder_id = $1
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files mf
			WHERE mf.media_folder_id = el.media_folder_id
			  AND mf.episode_id = el.episode_id
			  AND mf.missing_since IS NULL
		  )
	`, folderID)
	if err != nil {
		return 0, fmt.Errorf("reconciling episode library membership: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
