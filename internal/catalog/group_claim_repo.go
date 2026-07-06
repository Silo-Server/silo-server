package catalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

type GroupClaimRepository struct {
	pool *pgxpool.Pool
}

func NewGroupClaimRepository(pool *pgxpool.Pool) *GroupClaimRepository {
	return &GroupClaimRepository{pool: pool}
}

const groupClaimColumns = `media_folder_id, group_key_version, content_group_key, content_id, first_seen_at, last_seen_at`

func scanGroupClaim(row pgx.Row) (*models.MediaItemGroup, error) {
	var group models.MediaItemGroup
	if err := row.Scan(
		&group.MediaFolderID,
		&group.GroupKeyVersion,
		&group.ContentGroupKey,
		&group.ContentID,
		&group.FirstSeenAt,
		&group.LastSeenAt,
	); err != nil {
		return nil, fmt.Errorf("scanning media item group: %w", err)
	}
	return &group, nil
}

func (r *GroupClaimRepository) Get(ctx context.Context, folderID int, groupKeyVersion int, contentGroupKey string) (*models.MediaItemGroup, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+groupClaimColumns+`
		FROM media_item_groups
		WHERE media_folder_id = $1 AND group_key_version = $2 AND content_group_key = $3
	`, folderID, groupKeyVersion, contentGroupKey)
	group, err := scanGroupClaim(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return group, nil
}

func (r *GroupClaimRepository) ClaimGroup(
	ctx context.Context,
	folderID int,
	groupKeyVersion int,
	contentGroupKey string,
	contentID string,
) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO media_item_groups (media_folder_id, group_key_version, content_group_key, content_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (media_folder_id, group_key_version, content_group_key)
		DO UPDATE SET last_seen_at = NOW()
	`, folderID, groupKeyVersion, contentGroupKey, contentID)
	if err != nil {
		return fmt.Errorf("claiming content group: %w", err)
	}
	return nil
}

func (r *GroupClaimRepository) TouchByContentID(ctx context.Context, contentID string, seenAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE media_item_groups
		SET last_seen_at = $2
		WHERE content_id = $1
	`, contentID, seenAt)
	if err != nil {
		return fmt.Errorf("touching group claims by content_id: %w", err)
	}
	return nil
}

func (r *GroupClaimRepository) ClaimAndRelinkFiles(
	ctx context.Context,
	folderID int,
	groupKeyVersion int,
	contentGroupKey string,
	contentID string,
) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin group claim transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_groups (media_folder_id, group_key_version, content_group_key, content_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (media_folder_id, group_key_version, content_group_key)
		DO UPDATE SET last_seen_at = NOW()
	`, folderID, groupKeyVersion, contentGroupKey, contentID); err != nil {
		return 0, fmt.Errorf("claiming content group: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (content_id, media_folder_id) DO NOTHING
	`, contentID, folderID); err != nil {
		return 0, fmt.Errorf("upserting library membership: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE media_files
		SET content_id = $1, updated_at = NOW()
		WHERE media_folder_id = $2
		  AND group_key_version = $3
		  AND content_group_key = $4
		  AND missing_since IS NULL
		  AND extra_id IS NULL
		  AND (content_id IS NULL OR content_id = '')
	`, contentID, folderID, groupKeyVersion, contentGroupKey)
	if err != nil {
		return 0, fmt.Errorf("relinking files for content group: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit group claim transaction: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *GroupClaimRepository) DeleteByFolderAndObservedPathPrefix(ctx context.Context, folderID int, pathPrefix string) error {
	pathPrefix = filepath.Clean(pathPrefix)
	_, err := r.pool.Exec(ctx, `
		DELETE FROM media_item_groups mig
		WHERE mig.media_folder_id = $1
		  AND (
			EXISTS (
				SELECT 1
				FROM media_group_locations mgl
				WHERE mgl.media_folder_id = mig.media_folder_id
				  AND mgl.group_key_version = mig.group_key_version
				  AND mgl.content_group_key = mig.content_group_key
				  AND (mgl.observed_root_path = $2 OR mgl.observed_root_path LIKE $3)
			)
			OR EXISTS (
				SELECT 1
				FROM media_files mf
				WHERE mf.media_folder_id = mig.media_folder_id
				  AND mf.group_key_version = mig.group_key_version
				  AND mf.content_group_key = mig.content_group_key
				  AND mf.missing_since IS NULL
				  AND (mf.file_path = $2 OR mf.file_path LIKE $3)
			)
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM media_group_locations mgl
			WHERE mgl.media_folder_id = mig.media_folder_id
			  AND mgl.group_key_version = mig.group_key_version
			  AND mgl.content_group_key = mig.content_group_key
			  AND NOT (mgl.observed_root_path = $2 OR mgl.observed_root_path LIKE $3)
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files mf
			WHERE mf.media_folder_id = mig.media_folder_id
			  AND mf.group_key_version = mig.group_key_version
			  AND mf.content_group_key = mig.content_group_key
			  AND mf.missing_since IS NULL
			  AND NOT (mf.file_path = $2 OR mf.file_path LIKE $3)
		  )
	`, folderID, pathPrefix, pathPrefix+"/%")
	if err != nil {
		return fmt.Errorf("deleting group claims by observed path prefix: %w", err)
	}
	return nil
}

func (r *GroupClaimRepository) ListObservedRootsByContentID(ctx context.Context, contentID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT mgl.observed_root_path
		FROM media_item_groups mig
		JOIN media_group_locations mgl
		  ON mgl.media_folder_id = mig.media_folder_id
		 AND mgl.group_key_version = mig.group_key_version
		 AND mgl.content_group_key = mig.content_group_key
		WHERE mig.content_id = $1
		ORDER BY mgl.observed_root_path ASC
	`, contentID)
	if err != nil {
		return nil, fmt.Errorf("listing observed roots for content_id %s: %w", contentID, err)
	}
	defer rows.Close()

	paths := make([]string, 0)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scanning observed root path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating observed root paths: %w", err)
	}
	return paths, nil
}
