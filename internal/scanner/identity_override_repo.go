package scanner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// MediaIdentityOverrideRepository persists operator-provided path-scoped
// identity overrides (the durable half of a version split).
type MediaIdentityOverrideRepository struct {
	pool *pgxpool.Pool
}

func NewMediaIdentityOverrideRepository(pool *pgxpool.Pool) *MediaIdentityOverrideRepository {
	return &MediaIdentityOverrideRepository{pool: pool}
}

const mediaIdentityOverrideColumns = `id, media_folder_id, scope, root_path, file_path,
	forced_type, forced_title, forced_year, forced_tmdb_id, forced_imdb_id, forced_tvdb_id,
	note, created_by_user_id, updated_by_user_id, created_at, updated_at`

func scanMediaIdentityOverride(row pgx.Row) (*models.MediaIdentityOverride, error) {
	var override models.MediaIdentityOverride
	if err := row.Scan(
		&override.ID,
		&override.MediaFolderID,
		&override.Scope,
		&override.RootPath,
		&override.FilePath,
		&override.ForcedType,
		&override.ForcedTitle,
		&override.ForcedYear,
		&override.ForcedTmdbID,
		&override.ForcedImdbID,
		&override.ForcedTvdbID,
		&override.Note,
		&override.CreatedByUserID,
		&override.UpdatedByUserID,
		&override.CreatedAt,
		&override.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("scanning media identity override: %w", err)
	}
	return &override, nil
}

func (r *MediaIdentityOverrideRepository) ListByFolder(
	ctx context.Context,
	folderID int,
) ([]models.MediaIdentityOverride, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+mediaIdentityOverrideColumns+`
		FROM media_identity_overrides
		WHERE media_folder_id = $1
		ORDER BY scope ASC, root_path ASC, file_path ASC
	`, folderID)
	if err != nil {
		return nil, fmt.Errorf("listing media identity overrides: %w", err)
	}
	defer rows.Close()

	overrides := make([]models.MediaIdentityOverride, 0)
	for rows.Next() {
		override, err := scanMediaIdentityOverride(rows)
		if err != nil {
			return nil, err
		}
		overrides = append(overrides, *override)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media identity overrides: %w", err)
	}
	return overrides, nil
}

// Upsert persists one override keyed by (folder, scope, root_path, file_path).
func (r *MediaIdentityOverrideRepository) Upsert(
	ctx context.Context,
	override models.MediaIdentityOverride,
) error {
	return upsertMediaIdentityOverride(ctx, r.pool, override)
}

// UpsertTx is Upsert inside a caller-owned transaction (the split endpoint
// persists overrides atomically with the file re-point).
func (r *MediaIdentityOverrideRepository) UpsertTx(
	ctx context.Context,
	tx pgx.Tx,
	override models.MediaIdentityOverride,
) error {
	return upsertMediaIdentityOverride(ctx, tx, override)
}

type identityOverrideExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func upsertMediaIdentityOverride(ctx context.Context, exec identityOverrideExecutor, override models.MediaIdentityOverride) error {
	_, err := exec.Exec(ctx, `
		INSERT INTO media_identity_overrides (
			media_folder_id, scope, root_path, file_path,
			forced_type, forced_title, forced_year, forced_tmdb_id, forced_imdb_id, forced_tvdb_id,
			note, created_by_user_id, updated_by_user_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (media_folder_id, scope, root_path, file_path) DO UPDATE SET
			forced_type = EXCLUDED.forced_type,
			forced_title = EXCLUDED.forced_title,
			forced_year = EXCLUDED.forced_year,
			forced_tmdb_id = EXCLUDED.forced_tmdb_id,
			forced_imdb_id = EXCLUDED.forced_imdb_id,
			forced_tvdb_id = EXCLUDED.forced_tvdb_id,
			note = EXCLUDED.note,
			updated_by_user_id = EXCLUDED.updated_by_user_id,
			updated_at = NOW()
	`,
		override.MediaFolderID,
		override.Scope,
		normalizeOverridePath(override.RootPath),
		normalizeOverridePath(override.FilePath),
		strings.TrimSpace(override.ForcedType),
		strings.TrimSpace(override.ForcedTitle),
		override.ForcedYear,
		strings.TrimSpace(override.ForcedTmdbID),
		strings.TrimSpace(override.ForcedImdbID),
		strings.TrimSpace(override.ForcedTvdbID),
		strings.TrimSpace(override.Note),
		override.CreatedByUserID,
		override.UpdatedByUserID,
	)
	if err != nil {
		return fmt.Errorf("upserting media identity override: %w", err)
	}
	return nil
}

func (r *MediaIdentityOverrideRepository) Delete(ctx context.Context, folderID int, id int64) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM media_identity_overrides
		WHERE media_folder_id = $1 AND id = $2
	`, folderID, id)
	if err != nil {
		return fmt.Errorf("deleting media identity override: %w", err)
	}
	return nil
}

func normalizeOverridePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}
