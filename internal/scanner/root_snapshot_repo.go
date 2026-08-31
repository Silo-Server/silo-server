package scanner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ScannedRootRepository persists scanner root snapshots.
type ScannedRootRepository struct {
	pool *pgxpool.Pool
}

func NewScannedRootRepository(pool *pgxpool.Pool) *ScannedRootRepository {
	return &ScannedRootRepository{pool: pool}
}

const scannedRootColumns = `media_folder_id, root_path, state, inferred_type, type_confidence,
	title, year, tmdb_id, imdb_id, tvdb_id, observed_file_count, sample_file_path,
	evidence_json, override_source, first_seen_at, last_seen_at`

func scanScannedRoot(row pgx.Row) (*models.ScannedMediaRoot, error) {
	var root models.ScannedMediaRoot
	if err := row.Scan(
		&root.MediaFolderID,
		&root.RootPath,
		&root.State,
		&root.InferredType,
		&root.TypeConfidence,
		&root.Title,
		&root.Year,
		&root.TmdbID,
		&root.ImdbID,
		&root.TvdbID,
		&root.ObservedFileCount,
		&root.SampleFilePath,
		&root.EvidenceJSON,
		&root.OverrideSource,
		&root.FirstSeenAt,
		&root.LastSeenAt,
	); err != nil {
		return nil, fmt.Errorf("scanning scanned media root: %w", err)
	}
	return &root, nil
}

func (r *ScannedRootRepository) Get(ctx context.Context, folderID int, rootPath string) (*models.ScannedMediaRoot, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+scannedRootColumns+`
		FROM scanned_media_roots
		WHERE media_folder_id = $1 AND root_path = $2
	`, folderID, rootPath)
	root, err := scanScannedRoot(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return root, nil
}

func (r *ScannedRootRepository) ListByFolder(
	ctx context.Context,
	folderID int,
	state string,
	limit,
	offset int,
) ([]models.ScannedMediaRoot, int, error) {
	filterState := strings.TrimSpace(state)

	countQuery := `SELECT COUNT(*) FROM scanned_media_roots WHERE media_folder_id = $1`
	countArgs := []any{folderID}
	if filterState != "" {
		countQuery += ` AND state = $2`
		countArgs = append(countArgs, filterState)
	}

	var total int
	if err := r.pool.QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting scanned media roots: %w", err)
	}

	listQuery := `
		SELECT ` + scannedRootColumns + `
		FROM scanned_media_roots
		WHERE media_folder_id = $1`
	listArgs := []any{folderID}
	argPos := 2
	if filterState != "" {
		listQuery += fmt.Sprintf(` AND state = $%d`, argPos)
		listArgs = append(listArgs, filterState)
		argPos++
	}
	listQuery += fmt.Sprintf(` ORDER BY last_seen_at DESC, root_path ASC LIMIT $%d OFFSET $%d`, argPos, argPos+1)
	listArgs = append(listArgs, limit, offset)

	rows, err := r.pool.Query(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing scanned media roots: %w", err)
	}
	defer rows.Close()

	roots := make([]models.ScannedMediaRoot, 0)
	for rows.Next() {
		root, err := scanScannedRoot(rows)
		if err != nil {
			return nil, 0, err
		}
		roots = append(roots, *root)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating scanned media roots: %w", err)
	}
	return roots, total, nil
}

func (r *ScannedRootRepository) Upsert(ctx context.Context, root models.ScannedMediaRoot) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO scanned_media_roots (
			media_folder_id, root_path, state, inferred_type, type_confidence,
			title, year, tmdb_id, imdb_id, tvdb_id, observed_file_count,
			sample_file_path, evidence_json, override_source, first_seen_at, last_seen_at
		)
		VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11,
			$12, COALESCE($13, '{}'::jsonb), $14, NOW(), NOW()
		)
		ON CONFLICT (media_folder_id, root_path) DO UPDATE SET
			state = EXCLUDED.state,
			inferred_type = EXCLUDED.inferred_type,
			type_confidence = EXCLUDED.type_confidence,
			title = EXCLUDED.title,
			year = EXCLUDED.year,
			tmdb_id = EXCLUDED.tmdb_id,
			imdb_id = EXCLUDED.imdb_id,
			tvdb_id = EXCLUDED.tvdb_id,
			observed_file_count = EXCLUDED.observed_file_count,
			sample_file_path = EXCLUDED.sample_file_path,
			evidence_json = EXCLUDED.evidence_json,
			override_source = EXCLUDED.override_source,
			last_seen_at = NOW()
	`,
		root.MediaFolderID,
		root.RootPath,
		root.State,
		root.InferredType,
		root.TypeConfidence,
		root.Title,
		root.Year,
		root.TmdbID,
		root.ImdbID,
		root.TvdbID,
		root.ObservedFileCount,
		root.SampleFilePath,
		root.EvidenceJSON,
		root.OverrideSource,
	)
	if err != nil {
		return fmt.Errorf("upserting scanned media root: %w", err)
	}
	return nil
}

func (r *ScannedRootRepository) UpsertMany(ctx context.Context, roots []models.ScannedMediaRoot) error {
	if len(roots) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, root := range roots {
		batch.Queue(`
		INSERT INTO scanned_media_roots (
			media_folder_id, root_path, state, inferred_type, type_confidence,
			title, year, tmdb_id, imdb_id, tvdb_id, observed_file_count,
			sample_file_path, evidence_json, override_source, first_seen_at, last_seen_at
		)
		VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11,
			$12, COALESCE($13, '{}'::jsonb), $14, NOW(), NOW()
		)
		ON CONFLICT (media_folder_id, root_path) DO UPDATE SET
			state = EXCLUDED.state,
			inferred_type = EXCLUDED.inferred_type,
			type_confidence = EXCLUDED.type_confidence,
			title = EXCLUDED.title,
			year = EXCLUDED.year,
			tmdb_id = EXCLUDED.tmdb_id,
			imdb_id = EXCLUDED.imdb_id,
			tvdb_id = EXCLUDED.tvdb_id,
			observed_file_count = EXCLUDED.observed_file_count,
			sample_file_path = EXCLUDED.sample_file_path,
			evidence_json = EXCLUDED.evidence_json,
			override_source = EXCLUDED.override_source,
			last_seen_at = NOW()
		`,
			root.MediaFolderID,
			root.RootPath,
			root.State,
			root.InferredType,
			root.TypeConfidence,
			root.Title,
			root.Year,
			root.TmdbID,
			root.ImdbID,
			root.TvdbID,
			root.ObservedFileCount,
			root.SampleFilePath,
			root.EvidenceJSON,
			root.OverrideSource,
		)
	}

	results := r.pool.SendBatch(ctx, batch)
	defer results.Close()
	for range roots {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("upserting scanned media roots: %w", err)
		}
	}
	return nil
}

func (r *ScannedRootRepository) DeleteMissingInScope(ctx context.Context, folderID int, scopePath string, seenRoots []string) error {
	if seenRoots == nil {
		seenRoots = []string{}
	}
	scopePath = filepath.Clean(scopePath)
	_, err := r.pool.Exec(ctx, `
		DELETE FROM scanned_media_roots
		WHERE media_folder_id = $1
		  AND (root_path = $2 OR strpos(root_path, $2 || '/') = 1)
		  AND NOT (root_path = ANY($3))
	`, folderID, scopePath, seenRoots)
	if err != nil {
		return fmt.Errorf("deleting missing scanned roots in scope: %w", err)
	}
	return nil
}

// DeleteMissingByFolder removes scanned roots for a folder that were not seen
// anywhere in the library during an authoritative full scan.
func (r *ScannedRootRepository) DeleteMissingByFolder(ctx context.Context, folderID int, seenRoots []string) error {
	if seenRoots == nil {
		seenRoots = []string{}
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM scanned_media_roots
		WHERE media_folder_id = $1
		  AND NOT (root_path = ANY($2))
	`, folderID, seenRoots)
	if err != nil {
		return fmt.Errorf("deleting missing scanned roots by folder: %w", err)
	}
	return nil
}
