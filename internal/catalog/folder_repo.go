package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Retry parameters for transient serialization/deadlock failures. They are
// package vars (not consts) only so tests can shrink them; production code
// never mutates them.
var (
	deadlockMaxAttempts = 5
	deadlockBaseBackoff = 50 * time.Millisecond
)

const (
	// orphanDeleteBatch is small because each media_items row cascades across
	// ~15 child tables.
	orphanDeleteBatch = 1000
	// folderChildDeleteBatch covers the lighter folder-scoped media_files and
	// junction deletes.
	folderChildDeleteBatch = 5000
)

// retryOnDeadlock runs op, retrying when Postgres reports a deadlock (40P01) or
// serialization failure (40001), with exponential backoff. It returns
// immediately for any other error, and honors context cancellation between
// attempts.
func retryOnDeadlock(ctx context.Context, op func() error) error {
	backoff := deadlockBaseBackoff
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		var pgErr *pgconn.PgError
		if attempt < deadlockMaxAttempts && errors.As(err, &pgErr) &&
			(pgErr.Code == "40P01" || pgErr.Code == "40001") {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}
		return err
	}
}

// deleteInBatches repeatedly runs deleteBatch (each a single autocommit
// statement) until a batch removes fewer than batchSize rows. Each batch is
// retried on deadlock. It returns the total number of rows deleted.
func deleteInBatches(
	ctx context.Context,
	batchSize int,
	deleteBatch func(ctx context.Context) (int64, error),
) (int64, error) {
	var total int64
	for {
		var affected int64
		if err := retryOnDeadlock(ctx, func() error {
			n, e := deleteBatch(ctx)
			affected = n
			return e
		}); err != nil {
			return total, err
		}
		total += affected
		if affected < int64(batchSize) {
			return total, nil
		}
	}
}

// Sentinel errors for folder repository operations.
var (
	ErrFolderNotFound = errors.New("folder not found")
	ErrDuplicatePath  = errors.New("duplicate folder path")
)

// CreateFolderInput contains the fields required to create a new media folder.
type CreateFolderInput struct {
	Paths                    []string
	Type                     string
	Name                     string
	MetadataLanguage         string // ISO 639-1 code; defaults to "en" if empty
	ChapterThumbnailsEnabled bool
	IntroDetectionEnabled    bool
}

// FolderReorderEntry carries a folder ID and its new sort position.
type FolderReorderEntry struct {
	ID       int `json:"id"`
	Position int `json:"position"`
}

// UpdateFolderInput contains optional fields for a partial update. Only non-nil
// fields are written to the database.
type UpdateFolderInput struct {
	Paths                    *[]string // nil = no change, non-nil = replace all paths
	Type                     *string
	Name                     *string
	Enabled                  *bool
	MetadataLanguage         *string
	ChapterThumbnailsEnabled *bool
	IntroDetectionEnabled    *bool
}

// FolderRepository provides CRUD operations for the media_folders table.
type FolderRepository struct {
	pool *pgxpool.Pool
}

type DeleteFolderStats struct {
	LibraryName       string
	MediaFiles        int
	MediaItemLinks    int
	OrphanedItems     int
	OrphanedImageDirs []string // S3 base paths (directories) for orphaned images needing cleanup
}

// NewFolderRepository creates a new FolderRepository backed by the given pool.
func NewFolderRepository(pool *pgxpool.Pool) *FolderRepository {
	return &FolderRepository{pool: pool}
}

// Pool returns the underlying pgxpool.Pool. This is useful when other
// repositories need the same pool for cross-repo operations.
func (r *FolderRepository) Pool() *pgxpool.Pool {
	return r.pool
}

// folderColumns is the list of columns returned by all SELECT queries.
// Kept in one place so scanFolder stays in sync.
const folderColumns = `id, type, name, enabled, metadata_language, chapter_thumbnails_enabled, intro_detection_enabled, poster_path, last_scanned_at,
	scan_warning_code, scan_warning_message, scan_warning_at, allow_empty_cleanup_once, sort_order`

// scanFolder scans a single row into a *models.MediaFolder.
func scanFolder(row pgx.Row) (*models.MediaFolder, error) {
	var f models.MediaFolder

	err := row.Scan(
		&f.ID,
		&f.Type,
		&f.Name,
		&f.Enabled,
		&f.MetadataLanguage,
		&f.ChapterThumbnailsEnabled,
		&f.IntroDetectionEnabled,
		&f.PosterPath,
		&f.LastScannedAt,
		&f.ScanWarningCode,
		&f.ScanWarningMessage,
		&f.ScanWarningAt,
		&f.AllowEmptyCleanupOnce,
		&f.SortOrder,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFolderNotFound
		}
		return nil, fmt.Errorf("scanning folder: %w", err)
	}

	return &f, nil
}

// scanFolders scans multiple rows into a []*models.MediaFolder slice.
func scanFolders(rows pgx.Rows) ([]*models.MediaFolder, error) {
	var folders []*models.MediaFolder
	for rows.Next() {
		var f models.MediaFolder

		err := rows.Scan(
			&f.ID,
			&f.Type,
			&f.Name,
			&f.Enabled,
			&f.MetadataLanguage,
			&f.ChapterThumbnailsEnabled,
			&f.IntroDetectionEnabled,
			&f.PosterPath,
			&f.LastScannedAt,
			&f.ScanWarningCode,
			&f.ScanWarningMessage,
			&f.ScanWarningAt,
			&f.AllowEmptyCleanupOnce,
			&f.SortOrder,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning folder row: %w", err)
		}

		folders = append(folders, &f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating folder rows: %w", err)
	}
	return folders, nil
}

// loadPaths bulk-loads paths from media_folder_paths for the given folders
// and populates each folder's Paths field.
func (r *FolderRepository) loadPaths(ctx context.Context, folders []*models.MediaFolder) error {
	if len(folders) == 0 {
		return nil
	}

	ids := make([]int, len(folders))
	folderByID := make(map[int]*models.MediaFolder, len(folders))
	for i, f := range folders {
		ids[i] = f.ID
		folderByID[f.ID] = f
		f.Paths = []string{} // initialize to empty slice
	}

	rows, err := r.pool.Query(ctx,
		`SELECT media_folder_id, path FROM media_folder_paths WHERE media_folder_id = ANY($1) ORDER BY id ASC`,
		ids,
	)
	if err != nil {
		return fmt.Errorf("loading folder paths: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var folderID int
		var path string
		if err := rows.Scan(&folderID, &path); err != nil {
			return fmt.Errorf("scanning folder path row: %w", err)
		}
		if f, ok := folderByID[folderID]; ok {
			f.Paths = append(f.Paths, path)
		}
	}
	return rows.Err()
}

// Create inserts a new media folder with its paths and returns the created row.
func (r *FolderRepository) Create(ctx context.Context, input CreateFolderInput) (*models.MediaFolder, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning create transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	metaLang := input.MetadataLanguage
	if metaLang == "" {
		metaLang = "en"
	}

	query := `INSERT INTO media_folders (type, name, metadata_language, chapter_thumbnails_enabled, intro_detection_enabled, sort_order)
		VALUES ($1, $2, $3, $4, $5, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM media_folders))
		RETURNING ` + folderColumns

	row := tx.QueryRow(ctx, query,
		input.Type,
		input.Name,
		metaLang,
		input.ChapterThumbnailsEnabled,
		input.IntroDetectionEnabled,
	)

	folder, err := scanFolder(row)
	if err != nil {
		return nil, fmt.Errorf("creating folder: %w", err)
	}

	// Insert paths into child table.
	for _, p := range input.Paths {
		_, err := tx.Exec(ctx,
			`INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`,
			folder.ID, p,
		)
		if err != nil {
			if isDuplicateKeyError(err) {
				return nil, fmt.Errorf("%w: %s", ErrDuplicatePath, extractConstraint(err))
			}
			return nil, fmt.Errorf("inserting folder path %q: %w", p, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing create transaction: %w", err)
	}

	folder.Paths = input.Paths
	return folder, nil
}

// GetByID retrieves a media folder by its numeric ID.
func (r *FolderRepository) GetByID(ctx context.Context, id int) (*models.MediaFolder, error) {
	query := `SELECT ` + folderColumns + ` FROM media_folders WHERE id = $1`
	folder, err := scanFolder(r.pool.QueryRow(ctx, query, id))
	if err != nil {
		return nil, err
	}
	if err := r.loadPaths(ctx, []*models.MediaFolder{folder}); err != nil {
		return nil, fmt.Errorf("loading paths for folder %d: %w", id, err)
	}
	return folder, nil
}

// List returns all media folders ordered by ID ascending.
func (r *FolderRepository) List(ctx context.Context) ([]*models.MediaFolder, error) {
	query := `SELECT ` + folderColumns + ` FROM media_folders ORDER BY sort_order ASC, id ASC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing folders: %w", err)
	}
	defer rows.Close()

	folders, err := scanFolders(rows)
	if err != nil {
		return nil, err
	}
	if err := r.loadPaths(ctx, folders); err != nil {
		return nil, fmt.Errorf("loading paths for list: %w", err)
	}
	return folders, nil
}

// GetEnabled returns all enabled media folders ordered by ID ascending.
func (r *FolderRepository) GetEnabled(ctx context.Context) ([]*models.MediaFolder, error) {
	query := `SELECT ` + folderColumns + ` FROM media_folders WHERE enabled = true ORDER BY sort_order ASC, id ASC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing enabled folders: %w", err)
	}
	defer rows.Close()

	folders, err := scanFolders(rows)
	if err != nil {
		return nil, err
	}
	if err := r.loadPaths(ctx, folders); err != nil {
		return nil, fmt.Errorf("loading paths for enabled: %w", err)
	}
	return folders, nil
}

// ListByIDs returns enabled media folders matching the given IDs, ordered by ID ascending.
func (r *FolderRepository) ListByIDs(ctx context.Context, ids []int) ([]*models.MediaFolder, error) {
	query := `SELECT ` + folderColumns + ` FROM media_folders WHERE id = ANY($1) AND enabled = true ORDER BY sort_order ASC, id ASC`
	rows, err := r.pool.Query(ctx, query, ids)
	if err != nil {
		return nil, fmt.Errorf("listing folders by IDs: %w", err)
	}
	defer rows.Close()

	folders, err := scanFolders(rows)
	if err != nil {
		return nil, err
	}
	if err := r.loadPaths(ctx, folders); err != nil {
		return nil, fmt.Errorf("loading paths for IDs: %w", err)
	}
	return folders, nil
}

// Update modifies a folder's fields. Only non-nil fields in the input are updated.
func (r *FolderRepository) Update(ctx context.Context, id int, input UpdateFolderInput) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning update transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	setClauses := []string{}
	args := []any{}
	argIndex := 1

	if input.Type != nil {
		setClauses = append(setClauses, fmt.Sprintf("type = $%d", argIndex))
		args = append(args, *input.Type)
		argIndex++
	}
	if input.Name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", argIndex))
		args = append(args, *input.Name)
		argIndex++
	}
	if input.Enabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("enabled = $%d", argIndex))
		args = append(args, *input.Enabled)
		argIndex++
	}
	if input.MetadataLanguage != nil {
		setClauses = append(setClauses, fmt.Sprintf("metadata_language = $%d", argIndex))
		args = append(args, *input.MetadataLanguage)
		argIndex++
	}
	if input.ChapterThumbnailsEnabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("chapter_thumbnails_enabled = $%d", argIndex))
		args = append(args, *input.ChapterThumbnailsEnabled)
		argIndex++
	}
	if input.IntroDetectionEnabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("intro_detection_enabled = $%d", argIndex))
		args = append(args, *input.IntroDetectionEnabled)
		argIndex++
	}
	if len(setClauses) > 0 {
		query := fmt.Sprintf("UPDATE media_folders SET %s WHERE id = $%d",
			strings.Join(setClauses, ", "), argIndex)
		args = append(args, id)

		tag, execErr := tx.Exec(ctx, query, args...)
		if execErr != nil {
			return fmt.Errorf("updating folder: %w", execErr)
		}
		if tag.RowsAffected() == 0 {
			return ErrFolderNotFound
		}
	} else if input.Paths == nil {
		// Nothing to update; still verify the folder exists.
		var exists bool
		err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM media_folders WHERE id = $1)", id).Scan(&exists)
		if err != nil {
			return fmt.Errorf("checking folder existence: %w", err)
		}
		if !exists {
			return ErrFolderNotFound
		}
	}

	// Replace paths if provided.
	if input.Paths != nil {
		// Verify folder exists if we haven't already via SET clauses.
		if len(setClauses) == 0 {
			var exists bool
			err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM media_folders WHERE id = $1)", id).Scan(&exists)
			if err != nil {
				return fmt.Errorf("checking folder existence: %w", err)
			}
			if !exists {
				return ErrFolderNotFound
			}
		}

		_, err := tx.Exec(ctx, "DELETE FROM media_folder_paths WHERE media_folder_id = $1", id)
		if err != nil {
			return fmt.Errorf("deleting old paths: %w", err)
		}

		for _, p := range *input.Paths {
			_, err := tx.Exec(ctx,
				`INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`,
				id, p,
			)
			if err != nil {
				if isDuplicateKeyError(err) {
					return fmt.Errorf("%w: %s", ErrDuplicatePath, extractConstraint(err))
				}
				return fmt.Errorf("inserting folder path %q: %w", p, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing update transaction: %w", err)
	}
	return nil
}

// Delete removes a media folder by its ID and cleans up any media items that
// were exclusively associated with this library (orphaned items).
func (r *FolderRepository) Delete(ctx context.Context, id int) error {
	_, err := r.DeleteWithStats(ctx, id, nil)
	return err
}

func (r *FolderRepository) DeleteWithStats(
	ctx context.Context,
	id int,
	progress func(current, total int, message string),
) (*DeleteFolderStats, error) {
	stats := &DeleteFolderStats{}

	// Phase 0: preflight reads (no long-lived transaction).
	if err := r.pool.QueryRow(ctx, `SELECT name FROM media_folders WHERE id = $1`, id).Scan(&stats.LibraryName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFolderNotFound
		}
		return nil, fmt.Errorf("loading folder before delete: %w", err)
	}
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM media_files WHERE media_folder_id = $1`, id).Scan(&stats.MediaFiles); err != nil {
		return nil, fmt.Errorf("counting media files: %w", err)
	}
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM media_item_libraries WHERE media_folder_id = $1`, id).Scan(&stats.MediaItemLinks); err != nil {
		return nil, fmt.Errorf("counting media item links: %w", err)
	}
	var orphanTotal int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_item_libraries mil
		WHERE mil.media_folder_id = $1
		AND NOT EXISTS (
			SELECT 1 FROM media_item_libraries other
			WHERE other.content_id = mil.content_id
			AND other.media_folder_id <> $1
		)`, id).Scan(&orphanTotal); err != nil {
		return nil, fmt.Errorf("counting orphaned items: %w", err)
	}

	// Phase 1: delete orphaned media_items in detect-then-delete batches.
	// Cascade removes their junctions, provider IDs, episodes, seasons, etc.
	if progress != nil {
		progress(0, orphanTotal, "Deleting orphaned items")
	}
	rawDirs := make(map[string]struct{})
	for {
		ids, err := r.collectOrphanBatch(ctx, id, orphanDeleteBatch)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			break
		}
		dirs, err := collectRawImageDirs(ctx, r.pool, ids)
		if err != nil {
			return nil, err
		}
		for _, d := range dirs {
			rawDirs[d] = struct{}{}
		}
		var deleted int64
		if err := retryOnDeadlock(ctx, func() error {
			// Re-check the orphan invariant inside the delete. A concurrent
			// scan/import may have attached one of these content IDs to another
			// library after collectOrphanBatch returned; without this guard the
			// cascade would delete the shared media_items row (and the
			// newly-added membership), dropping the item from the other library.
			tag, e := r.pool.Exec(ctx, `
				DELETE FROM media_items
				WHERE content_id = ANY($1)
				AND NOT EXISTS (
					SELECT 1 FROM media_item_libraries other
					WHERE other.content_id = media_items.content_id
					AND other.media_folder_id <> $2
				)`, ids, id)
			if e != nil {
				return e
			}
			deleted = tag.RowsAffected()
			return nil
		}); err != nil {
			return nil, fmt.Errorf("deleting orphaned items: %w", err)
		}
		stats.OrphanedItems += int(deleted)
		if progress != nil {
			progress(stats.OrphanedItems, orphanTotal, "Deleting orphaned items")
		}
	}

	// Phase 2: delete this folder's media_files (folder-tied, not item-tied).
	if progress != nil {
		progress(orphanTotal, orphanTotal, "Deleting media files")
	}
	if _, err := deleteInBatches(ctx, folderChildDeleteBatch, func(ctx context.Context) (int64, error) {
		tag, e := r.pool.Exec(ctx, `
			DELETE FROM media_files
			WHERE id IN (
				SELECT id FROM media_files WHERE media_folder_id = $1 LIMIT $2
			)`, id, folderChildDeleteBatch)
		if e != nil {
			return 0, e
		}
		return tag.RowsAffected(), nil
	}); err != nil {
		return nil, fmt.Errorf("deleting media files: %w", err)
	}

	// Phase 3: delete remaining folder memberships (shared items kept; only the
	// membership in this folder is removed). Deleting the memberships with
	// RETURNING lets us catch skeleton items that a matcher created after the
	// phase-1 orphan sweep but before the delete job reached this phase.
	if progress != nil {
		progress(orphanTotal, orphanTotal, "Removing library memberships")
	}
	lateOrphans, lateImageDirs, err := r.deleteFolderMemberships(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("deleting media item links: %w", err)
	}
	stats.OrphanedItems += lateOrphans
	for _, d := range lateImageDirs {
		rawDirs[d] = struct{}{}
	}

	// Filter accumulated image dirs once, now that all item orphans are gone,
	// against any surviving content. Empty deleting-set means "exclude nothing".
	if len(rawDirs) > 0 {
		filtered, err := filterUnreferencedImageDirs(ctx, r.pool, dirSetToSlice(rawDirs), []string{})
		if err != nil {
			return nil, err
		}
		stats.OrphanedImageDirs = filtered
	}

	// Phase 4: delete the now-lightweight folder row. Tolerate 0 rows so a
	// resumed run that already removed it still succeeds.
	if err := retryOnDeadlock(ctx, func() error {
		_, e := r.pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, id)
		return e
	}); err != nil {
		return nil, fmt.Errorf("deleting folder: %w", err)
	}

	if progress != nil {
		progress(orphanTotal, orphanTotal, "Library deletion completed")
	}
	return stats, nil
}

// collectOrphanBatch returns up to limit content IDs whose only library
// membership is the given folder.
func (r *FolderRepository) collectOrphanBatch(ctx context.Context, folderID, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT mil.content_id
		FROM media_item_libraries mil
		WHERE mil.media_folder_id = $1
		AND NOT EXISTS (
			SELECT 1 FROM media_item_libraries other
			WHERE other.content_id = mil.content_id
			AND other.media_folder_id <> $1
		)
		LIMIT $2`, folderID, limit)
	if err != nil {
		return nil, fmt.Errorf("finding orphaned items: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning orphan content_id: %w", err)
		}
		ids = append(ids, contentID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating orphan rows: %w", err)
	}
	return ids, nil
}

// deleteFolderMemberships removes this folder's remaining item memberships and
// deletes any content that becomes orphaned as a result. This is intentionally
// separate from the phase-1 orphan sweep because in-flight matchers can create
// new skeleton memberships after phase 1 has already drained.
func (r *FolderRepository) deleteFolderMemberships(ctx context.Context, folderID int) (int, []string, error) {
	var orphaned int
	var imageDirs []string
	for {
		var contentIDs []string
		if err := retryOnDeadlock(ctx, func() error {
			ids, err := r.deleteFolderMembershipBatch(ctx, folderID, folderChildDeleteBatch)
			if err != nil {
				return err
			}
			contentIDs = ids
			return nil
		}); err != nil {
			return orphaned, imageDirs, err
		}
		if len(contentIDs) > 0 {
			deleted, dirs, err := r.deleteOrphanedItemsByContentID(ctx, contentIDs)
			if err != nil {
				return orphaned, imageDirs, err
			}
			orphaned += deleted
			imageDirs = append(imageDirs, dirs...)
		}
		if len(contentIDs) < folderChildDeleteBatch {
			return orphaned, imageDirs, nil
		}
	}
}

func (r *FolderRepository) deleteFolderMembershipBatch(ctx context.Context, folderID, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		DELETE FROM media_item_libraries
		WHERE ctid IN (
			SELECT ctid FROM media_item_libraries WHERE media_folder_id = $1 LIMIT $2
		)
		RETURNING content_id`, folderID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contentIDs []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning deleted membership content_id: %w", err)
		}
		contentIDs = append(contentIDs, contentID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return contentIDs, nil
}

const collectOrphanedProvisionalIDsByContentIDSQL = `
	SELECT mi.content_id
	FROM public.media_items mi
	WHERE mi.content_id = ANY($1)
	  AND ` + orphanedProvisionalMediaItemConditions

const deleteOrphanedProvisionalIDsByContentIDSQL = `
	DELETE FROM public.media_items mi
	WHERE mi.content_id = ANY($1)
	  AND ` + orphanedProvisionalMediaItemConditions

func (r *FolderRepository) deleteOrphanedItemsByContentID(ctx context.Context, contentIDs []string) (int, []string, error) {
	if len(contentIDs) == 0 {
		return 0, nil, nil
	}

	orphanIDs, err := r.collectOrphanedProvisionalIDsByContentID(ctx, contentIDs)
	if err != nil {
		return 0, nil, err
	}
	if len(orphanIDs) == 0 {
		return 0, nil, nil
	}

	imageDirs, err := collectImageDirs(ctx, r.pool, orphanIDs)
	if err != nil {
		return 0, nil, err
	}

	var deleted int64
	if err := retryOnDeadlock(ctx, func() error {
		tag, err := r.pool.Exec(ctx, deleteOrphanedProvisionalIDsByContentIDSQL, orphanIDs)
		if err != nil {
			return err
		}
		deleted = tag.RowsAffected()
		return nil
	}); err != nil {
		return 0, nil, err
	}
	return int(deleted), imageDirs, nil
}

func (r *FolderRepository) collectOrphanedProvisionalIDsByContentID(ctx context.Context, contentIDs []string) ([]string, error) {
	rows, err := r.pool.Query(ctx, collectOrphanedProvisionalIDsByContentIDSQL, contentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orphanIDs []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning orphan content_id: %w", err)
		}
		orphanIDs = append(orphanIDs, contentID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return orphanIDs, nil
}

// dirSetToSlice returns the keys of set as a slice (nil if empty).
func dirSetToSlice(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(set))
	for d := range set {
		dirs = append(dirs, d)
	}
	return dirs
}

// pathDir extracts the directory portion of an S3 image path.
// e.g. "tmdb/movies/550/poster/original.webp" → "tmdb/movies/550/poster/"
func pathDir(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return ""
	}
	return path[:idx+1]
}

// rowQuerier is satisfied by both *pgxpool.Pool and pgx.Tx, letting read
// helpers run inside or outside an explicit transaction.
type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// collectOrphanIDs returns content IDs from the given set that have no
// remaining library memberships. Must be called within a transaction.
func collectOrphanIDs(ctx context.Context, tx pgx.Tx, contentIDs []string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT cid FROM unnest($1::text[]) AS cid
		WHERE NOT EXISTS (
			SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = cid
		)
	`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("finding orphaned items: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning orphan id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// collectImageDirs returns S3 directory prefixes for images belonging to the
// given content IDs that are not still referenced by other surviving content.
func collectImageDirs(ctx context.Context, q rowQuerier, contentIDs []string) ([]string, error) {
	dirs, err := collectRawImageDirs(ctx, q, contentIDs)
	if err != nil {
		return nil, err
	}
	return filterUnreferencedImageDirs(ctx, q, dirs, contentIDs)
}

// collectRawImageDirs returns the deduped S3 directory prefixes referenced by
// the given content IDs (items, their seasons, and their episodes), without
// filtering out dirs still used by other content.
func collectRawImageDirs(ctx context.Context, q rowQuerier, contentIDs []string) ([]string, error) {
	imgRows, err := q.Query(ctx, `
		SELECT poster_path, backdrop_path, logo_path FROM media_items WHERE content_id = ANY($1)
		UNION ALL
		SELECT poster_path, '', '' FROM seasons WHERE series_id = ANY($1)
		UNION ALL
		SELECT still_path, '', '' FROM episodes WHERE series_id = ANY($1)
	`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("collecting image paths: %w", err)
	}
	defer imgRows.Close()
	dirSet := make(map[string]struct{})
	for imgRows.Next() {
		var p1, p2, p3 string
		if err := imgRows.Scan(&p1, &p2, &p3); err != nil {
			return nil, fmt.Errorf("scanning image path: %w", err)
		}
		for _, p := range []string{p1, p2, p3} {
			if p != "" && !strings.Contains(p, "://") {
				if dir := pathDir(p); dir != "" {
					dirSet[dir] = struct{}{}
				}
			}
		}
	}
	if err := imgRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating image paths: %w", err)
	}
	dirs := make([]string, 0, len(dirSet))
	for dir := range dirSet {
		dirs = append(dirs, dir)
	}
	return dirs, nil
}

func filterUnreferencedImageDirs(ctx context.Context, q rowQuerier, dirs, deletingContentIDs []string) ([]string, error) {
	if len(dirs) == 0 {
		return nil, nil
	}

	rows, err := q.Query(ctx, `
		SELECT candidate.dir
		FROM unnest($1::text[]) AS candidate(dir)
		WHERE NOT EXISTS (
			SELECT 1
			FROM media_items mi
			WHERE NOT (mi.content_id = ANY($2::text[]))
			  AND (
				mi.poster_path LIKE candidate.dir || '%'
				OR mi.backdrop_path LIKE candidate.dir || '%'
				OR mi.logo_path LIKE candidate.dir || '%'
			  )
		)
		AND NOT EXISTS (
			SELECT 1
			FROM seasons s
			WHERE NOT (s.series_id = ANY($2::text[]))
			  AND s.poster_path LIKE candidate.dir || '%'
		)
		AND NOT EXISTS (
			SELECT 1
			FROM episodes e
			WHERE NOT (e.series_id = ANY($2::text[]))
			  AND e.still_path LIKE candidate.dir || '%'
		)
		ORDER BY candidate.dir
	`, dirs, deletingContentIDs)
	if err != nil {
		return nil, fmt.Errorf("filtering referenced image dirs: %w", err)
	}
	defer rows.Close()

	var unreferenced []string
	for rows.Next() {
		var dir string
		if err := rows.Scan(&dir); err != nil {
			return nil, fmt.Errorf("scanning unreferenced image dir: %w", err)
		}
		unreferenced = append(unreferenced, dir)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating unreferenced image dirs: %w", err)
	}
	return unreferenced, nil
}

// UpdateLastScanned sets the last_scanned_at timestamp for the given folder.
func (r *FolderRepository) UpdateLastScanned(ctx context.Context, id int, scannedAt time.Time) error {
	tag, err := r.pool.Exec(ctx,
		"UPDATE media_folders SET last_scanned_at = $1 WHERE id = $2",
		scannedAt, id,
	)
	if err != nil {
		return fmt.Errorf("updating last_scanned_at: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return ErrFolderNotFound
	}

	return nil
}

// SetScanWarning records a non-fatal scan warning on the folder.
func (r *FolderRepository) SetScanWarning(ctx context.Context, id int, code, message string, warnedAt time.Time) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE media_folders
		SET scan_warning_code = $1,
			scan_warning_message = $2,
			scan_warning_at = $3
		WHERE id = $4`,
		code, message, warnedAt, id,
	)
	if err != nil {
		return fmt.Errorf("updating scan warning: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFolderNotFound
	}

	return nil
}

// ClearScanWarning clears any scan warning state and resets one-shot cleanup confirmation.
func (r *FolderRepository) ClearScanWarning(ctx context.Context, id int) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE media_folders
		SET scan_warning_code = NULL,
			scan_warning_message = NULL,
			scan_warning_at = NULL,
			allow_empty_cleanup_once = false
		WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("clearing scan warning: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFolderNotFound
	}

	return nil
}

// AllowEmptyCleanupOnce arms a single destructive empty-root cleanup pass.
func (r *FolderRepository) AllowEmptyCleanupOnce(ctx context.Context, id int) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE media_folders
		SET allow_empty_cleanup_once = true
		WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("arming empty cleanup confirmation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFolderNotFound
	}

	return nil
}

// ConsumeEmptyCleanupAllowance returns whether a destructive empty-root cleanup
// has been approved and clears the one-shot flag.
func (r *FolderRepository) ConsumeEmptyCleanupAllowance(ctx context.Context, id int) (bool, error) {
	var allowed bool
	err := r.pool.QueryRow(ctx,
		`WITH current_state AS (
			SELECT allow_empty_cleanup_once
			FROM media_folders
			WHERE id = $1
		)
		UPDATE media_folders
		SET allow_empty_cleanup_once = false
		WHERE id = $1
		RETURNING (SELECT allow_empty_cleanup_once FROM current_state)`,
		id,
	).Scan(&allowed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrFolderNotFound
		}
		return false, fmt.Errorf("consuming empty cleanup confirmation: %w", err)
	}

	return allowed, nil
}

// SetPosterPath stores the S3 key of the library poster image.
func (r *FolderRepository) SetPosterPath(ctx context.Context, id int, posterPath string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE media_folders SET poster_path = $1 WHERE id = $2`,
		posterPath, id,
	)
	if err != nil {
		return fmt.Errorf("setting poster path: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFolderNotFound
	}
	return nil
}

// ClearPosterPath removes the library poster path.
func (r *FolderRepository) ClearPosterPath(ctx context.Context, id int) error {
	return r.SetPosterPath(ctx, id, "")
}

// Reorder batch-updates sort_order for the given folders in a transaction.
func (r *FolderRepository) Reorder(ctx context.Context, entries []FolderReorderEntry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning reorder transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, e := range entries {
		if _, err := tx.Exec(ctx,
			"UPDATE media_folders SET sort_order = $1 WHERE id = $2",
			e.Position, e.ID,
		); err != nil {
			return fmt.Errorf("reordering folder %d: %w", e.ID, err)
		}
	}
	return tx.Commit(ctx)
}

// isDuplicateKeyError checks if the error is a PostgreSQL unique_violation (code 23505).
func isDuplicateKeyError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// extractConstraint extracts the constraint name from a PgError for diagnostic messages.
func extractConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return "unknown"
}
