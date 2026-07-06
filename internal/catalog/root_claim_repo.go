package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// RootClaimRepository provides CRUD operations for the media_item_roots table.
// It tracks which content_id owns a given canonical root path within a library
// folder, replacing the previous path-prefix heuristics.
type RootClaimRepository struct {
	pool *pgxpool.Pool
}

// NewRootClaimRepository creates a new RootClaimRepository backed by the given pool.
func NewRootClaimRepository(pool *pgxpool.Pool) *RootClaimRepository {
	return &RootClaimRepository{pool: pool}
}

const rootClaimColumns = `media_folder_id, canonical_root_path, content_id, first_seen_at, last_seen_at`

// scanRootClaim scans a single row into a *models.MediaItemRoot.
func scanRootClaim(row pgx.Row) (*models.MediaItemRoot, error) {
	var root models.MediaItemRoot
	if err := row.Scan(
		&root.MediaFolderID,
		&root.CanonicalRootPath,
		&root.ContentID,
		&root.FirstSeenAt,
		&root.LastSeenAt,
	); err != nil {
		return nil, fmt.Errorf("scanning media item root: %w", err)
	}
	return &root, nil
}

// scanRootClaims scans multiple rows into a []*models.MediaItemRoot slice.
func scanRootClaims(rows pgx.Rows) ([]*models.MediaItemRoot, error) {
	var roots []*models.MediaItemRoot
	for rows.Next() {
		var root models.MediaItemRoot
		if err := rows.Scan(
			&root.MediaFolderID,
			&root.CanonicalRootPath,
			&root.ContentID,
			&root.FirstSeenAt,
			&root.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("scanning media item root row: %w", err)
		}
		roots = append(roots, &root)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media item root rows: %w", err)
	}
	if roots == nil {
		roots = []*models.MediaItemRoot{}
	}
	return roots, nil
}

// Get returns the root claim for the given folder and canonical root path,
// or nil if no claim exists.
func (r *RootClaimRepository) Get(ctx context.Context, folderID int, rootPath string) (*models.MediaItemRoot, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+rootClaimColumns+`
		FROM media_item_roots
		WHERE media_folder_id = $1 AND canonical_root_path = $2
	`, folderID, rootPath)

	root, err := scanRootClaim(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting root claim: %w", err)
	}
	return root, nil
}

// Claim inserts or updates a root claim. If the root path is already claimed,
// only last_seen_at is updated. The operation participates in the provided
// transaction so callers can atomically claim a root alongside other writes.
func (r *RootClaimRepository) Claim(ctx context.Context, tx pgx.Tx, folderID int, rootPath, contentID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO media_item_roots (media_folder_id, canonical_root_path, content_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (media_folder_id, canonical_root_path)
		DO UPDATE SET last_seen_at = now()
	`, folderID, rootPath, contentID)
	if err != nil {
		return fmt.Errorf("claiming root path: %w", err)
	}
	return nil
}

// ClaimRoot inserts or updates a root claim without requiring an external
// transaction. It uses first-writer-wins semantics: if the root is already
// claimed by a different content_id, the existing claim is preserved and only
// last_seen_at is refreshed. Callers should use Get first to check ownership.
// This is the non-transactional counterpart to Claim.
func (r *RootClaimRepository) ClaimRoot(ctx context.Context, folderID int, rootPath, contentID string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO media_item_roots (media_folder_id, canonical_root_path, content_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (media_folder_id, canonical_root_path)
		DO UPDATE SET last_seen_at = now()
	`, folderID, rootPath, contentID)
	if err != nil {
		return fmt.Errorf("claiming root path: %w", err)
	}
	return nil
}

// TouchByContentID updates last_seen_at for all root claims belonging to the
// given content_id.
func (r *RootClaimRepository) TouchByContentID(ctx context.Context, contentID string, seenAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE media_item_roots
		SET last_seen_at = $2
		WHERE content_id = $1
	`, contentID, seenAt)
	if err != nil {
		return fmt.Errorf("touching root claims by content_id: %w", err)
	}
	return nil
}

// ListByFolder returns all root claims for the given folder, ordered by
// canonical root path.
func (r *RootClaimRepository) ListByFolder(ctx context.Context, folderID int) ([]*models.MediaItemRoot, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+rootClaimColumns+`
		FROM media_item_roots
		WHERE media_folder_id = $1
		ORDER BY canonical_root_path ASC
	`, folderID)
	if err != nil {
		return nil, fmt.Errorf("listing root claims by folder: %w", err)
	}
	defer rows.Close()
	return scanRootClaims(rows)
}

// ClaimAndRelinkFiles atomically claims a canonical root path for a content_id,
// upserts the library membership, and bulk-links all unlinked present files
// under the path prefix to the content_id. All three operations happen inside a
// single transaction so the migration is all-or-nothing per root group.
//
// Returns the number of files whose content_id was updated.
func (r *RootClaimRepository) ClaimAndRelinkFiles(ctx context.Context, folderID int, rootPath, contentID string) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin claim-and-relink transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 1. Claim the root path for this content_id.
	if err := r.Claim(ctx, tx, folderID, rootPath, contentID); err != nil {
		return 0, fmt.Errorf("claiming root: %w", err)
	}

	// 2. Upsert library membership.
	_, err = tx.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (content_id, media_folder_id) DO NOTHING
	`, contentID, folderID)
	if err != nil {
		return 0, fmt.Errorf("upserting library membership: %w", err)
	}

	// 3. Bulk-link all unlinked present files under this root path.
	prefixLike := rootPath + "/%"
	tag, err := tx.Exec(ctx, `
		UPDATE media_files
		SET content_id = $1, updated_at = NOW()
		WHERE media_folder_id = $2
		  AND missing_since IS NULL
		  AND extra_id IS NULL
		  AND (content_id IS NULL OR content_id = '')
		  AND (file_path = $3 OR file_path LIKE $4)
	`, contentID, folderID, rootPath, prefixLike)
	if err != nil {
		return 0, fmt.Errorf("relinking files under root: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit claim-and-relink transaction: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ListByContentID returns all root claims for the given content_id, ordered by
// canonical root path.
func (r *RootClaimRepository) ListByContentID(ctx context.Context, contentID string) ([]*models.MediaItemRoot, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+rootClaimColumns+`
		FROM media_item_roots
		WHERE content_id = $1
		ORDER BY canonical_root_path ASC
	`, contentID)
	if err != nil {
		return nil, fmt.Errorf("listing root claims by content_id: %w", err)
	}
	defer rows.Close()
	return scanRootClaims(rows)
}

// DeleteByFolderAndRoot removes a single root claim.
func (r *RootClaimRepository) DeleteByFolderAndRoot(ctx context.Context, folderID int, rootPath string) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM media_item_roots
		WHERE media_folder_id = $1 AND canonical_root_path = $2
	`, folderID, rootPath)
	if err != nil {
		return fmt.Errorf("deleting root claim: %w", err)
	}
	return nil
}
