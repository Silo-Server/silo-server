package scanner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pathscope"

	"github.com/Silo-Server/silo-server/internal/database"
)

type ObservedLocationRepository struct {
	pool *pgxpool.Pool
}

func NewObservedLocationRepository(pool *pgxpool.Pool) *ObservedLocationRepository {
	return &ObservedLocationRepository{pool: pool}
}

func (r *ObservedLocationRepository) Get(
	ctx context.Context,
	folderID int,
	observedRootPath string,
) (*models.ObservedMediaLocation, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT media_folder_id, observed_root_path, location_type, sample_file_path,
		       observed_file_count, content_group_count, primary_group_key_version, primary_content_group_key,
		       state, evidence_json, first_seen_at, last_seen_at
		FROM observed_media_locations
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, folderID, observedRootPath)
	var location models.ObservedMediaLocation
	if err := row.Scan(
		&location.MediaFolderID,
		&location.ObservedRootPath,
		&location.LocationType,
		&location.SampleFilePath,
		&location.ObservedFileCount,
		&location.ContentGroupCount,
		&location.PrimaryGroupKeyVersion,
		&location.PrimaryContentGroupKey,
		&location.State,
		&location.EvidenceJSON,
		&location.FirstSeenAt,
		&location.LastSeenAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning observed media location: %w", err)
	}
	return &location, nil
}

func (r *ObservedLocationRepository) UpsertMany(ctx context.Context, locations []models.ObservedMediaLocation) error {
	if len(locations) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, location := range locations {
		batch.Queue(`
			INSERT INTO observed_media_locations (
				media_folder_id, observed_root_path, location_type, sample_file_path,
				observed_file_count, content_group_count, primary_group_key_version, primary_content_group_key,
				state, evidence_json, first_seen_at, last_seen_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10, '{}'::jsonb), NOW(), NOW())
			ON CONFLICT (media_folder_id, observed_root_path) DO UPDATE SET
				location_type = EXCLUDED.location_type,
				sample_file_path = EXCLUDED.sample_file_path,
				observed_file_count = EXCLUDED.observed_file_count,
				content_group_count = EXCLUDED.content_group_count,
				primary_group_key_version = EXCLUDED.primary_group_key_version,
				primary_content_group_key = EXCLUDED.primary_content_group_key,
				state = EXCLUDED.state,
				evidence_json = EXCLUDED.evidence_json,
				last_seen_at = NOW()
		`,
			location.MediaFolderID,
			location.ObservedRootPath,
			location.LocationType,
			location.SampleFilePath,
			location.ObservedFileCount,
			location.ContentGroupCount,
			location.PrimaryGroupKeyVersion,
			location.PrimaryContentGroupKey,
			location.State,
			location.EvidenceJSON,
		)
	}
	results := database.SendBatch(ctx, r.pool, batch)
	defer results.Close()
	for range locations {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("upserting observed media locations: %w", err)
		}
	}
	return nil
}

func (r *ObservedLocationRepository) ReplaceForFolder(ctx context.Context, folderID int, locations []models.ObservedMediaLocation) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin observed location replace transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM observed_media_locations WHERE media_folder_id = $1`, folderID); err != nil {
		return fmt.Errorf("clearing observed media locations: %w", err)
	}

	batch := &pgx.Batch{}
	for _, location := range locations {
		batch.Queue(`
			INSERT INTO observed_media_locations (
				media_folder_id, observed_root_path, location_type, sample_file_path,
				observed_file_count, content_group_count, primary_group_key_version, primary_content_group_key,
				state, evidence_json, first_seen_at, last_seen_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10, '{}'::jsonb), NOW(), NOW())
		`,
			location.MediaFolderID,
			location.ObservedRootPath,
			location.LocationType,
			location.SampleFilePath,
			location.ObservedFileCount,
			location.ContentGroupCount,
			location.PrimaryGroupKeyVersion,
			location.PrimaryContentGroupKey,
			location.State,
			location.EvidenceJSON,
		)
	}
	results := database.SendBatch(ctx, tx, batch)
	for range locations {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("inserting observed media locations: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("closing observed location batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit observed location replace transaction: %w", err)
	}
	return nil
}

func (r *ObservedLocationRepository) ReplaceInScope(
	ctx context.Context,
	folderID int,
	scopePath string,
	locations []models.ObservedMediaLocation,
) error {
	scopePath = filepath.Clean(scopePath)
	scopeLike := pathscope.PrefixLike(scopePath)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin observed location scoped replace transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		DELETE FROM observed_media_locations
		WHERE media_folder_id = $1
		  AND (observed_root_path = $2 OR observed_root_path LIKE $3 ESCAPE '\')
	`, folderID, scopePath, scopeLike); err != nil {
		return fmt.Errorf("clearing observed media locations in scope: %w", err)
	}

	batch := &pgx.Batch{}
	for _, location := range locations {
		batch.Queue(`
			INSERT INTO observed_media_locations (
				media_folder_id, observed_root_path, location_type, sample_file_path,
				observed_file_count, content_group_count, primary_group_key_version, primary_content_group_key,
				state, evidence_json, first_seen_at, last_seen_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10, '{}'::jsonb), NOW(), NOW())
		`,
			location.MediaFolderID,
			location.ObservedRootPath,
			location.LocationType,
			location.SampleFilePath,
			location.ObservedFileCount,
			location.ContentGroupCount,
			location.PrimaryGroupKeyVersion,
			location.PrimaryContentGroupKey,
			location.State,
			location.EvidenceJSON,
		)
	}
	results := database.SendBatch(ctx, tx, batch)
	for range locations {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("inserting observed media locations in scope: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("closing observed location scoped batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit observed location scoped replace transaction: %w", err)
	}
	return nil
}

type GroupLocationRepository struct {
	pool *pgxpool.Pool
}

func NewGroupLocationRepository(pool *pgxpool.Pool) *GroupLocationRepository {
	return &GroupLocationRepository{pool: pool}
}

func (r *GroupLocationRepository) UpsertMany(ctx context.Context, locations []models.MediaGroupLocation) error {
	if len(locations) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, location := range locations {
		batch.Queue(`
			INSERT INTO media_group_locations (
				media_folder_id, group_key_version, content_group_key, observed_root_path, is_primary, first_seen_at, last_seen_at
			)
			VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
			ON CONFLICT (media_folder_id, group_key_version, content_group_key, observed_root_path) DO UPDATE SET
				is_primary = EXCLUDED.is_primary,
				last_seen_at = NOW()
		`,
			location.MediaFolderID,
			location.GroupKeyVersion,
			location.ContentGroupKey,
			location.ObservedRootPath,
			location.IsPrimary,
		)
	}
	results := database.SendBatch(ctx, r.pool, batch)
	defer results.Close()
	for range locations {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("upserting group locations: %w", err)
		}
	}
	return nil
}

func (r *GroupLocationRepository) ReplaceForFolder(ctx context.Context, folderID int, locations []models.MediaGroupLocation) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin group location replace transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM media_group_locations WHERE media_folder_id = $1`, folderID); err != nil {
		return fmt.Errorf("clearing group locations: %w", err)
	}

	batch := &pgx.Batch{}
	for _, location := range locations {
		batch.Queue(`
			INSERT INTO media_group_locations (
				media_folder_id, group_key_version, content_group_key, observed_root_path, is_primary, first_seen_at, last_seen_at
			)
			VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		`,
			location.MediaFolderID,
			location.GroupKeyVersion,
			location.ContentGroupKey,
			location.ObservedRootPath,
			location.IsPrimary,
		)
	}
	results := database.SendBatch(ctx, tx, batch)
	for range locations {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("inserting group locations: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("closing group location batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit group location replace transaction: %w", err)
	}
	return nil
}

func (r *GroupLocationRepository) ReplaceInScope(
	ctx context.Context,
	folderID int,
	scopePath string,
	locations []models.MediaGroupLocation,
) error {
	scopePath = filepath.Clean(scopePath)
	scopeLike := pathscope.PrefixLike(scopePath)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin group location scoped replace transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		DELETE FROM media_group_locations
		WHERE media_folder_id = $1
		  AND (observed_root_path = $2 OR observed_root_path LIKE $3 ESCAPE '\')
	`, folderID, scopePath, scopeLike); err != nil {
		return fmt.Errorf("clearing group locations in scope: %w", err)
	}

	batch := &pgx.Batch{}
	for _, location := range locations {
		batch.Queue(`
			INSERT INTO media_group_locations (
				media_folder_id, group_key_version, content_group_key, observed_root_path, is_primary, first_seen_at, last_seen_at
			)
			VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		`,
			location.MediaFolderID,
			location.GroupKeyVersion,
			location.ContentGroupKey,
			location.ObservedRootPath,
			location.IsPrimary,
		)
	}
	results := database.SendBatch(ctx, tx, batch)
	for range locations {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("inserting group locations in scope: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("closing group location scoped batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit group location scoped replace transaction: %w", err)
	}
	return nil
}
