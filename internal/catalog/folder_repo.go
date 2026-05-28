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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning delete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	stats := &DeleteFolderStats{}
	if err := tx.QueryRow(ctx, `SELECT name FROM media_folders WHERE id = $1`, id).Scan(&stats.LibraryName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFolderNotFound
		}
		return nil, fmt.Errorf("loading folder before delete: %w", err)
	}

	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM media_files WHERE media_folder_id = $1`, id).Scan(&stats.MediaFiles); err != nil {
		return nil, fmt.Errorf("counting media files: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM media_item_libraries WHERE media_folder_id = $1`, id).Scan(&stats.MediaItemLinks); err != nil {
		return nil, fmt.Errorf("counting media item links: %w", err)
	}

	// Collect items that exist ONLY in this library before cascade removes the
	// junction rows, so we know which media_items to clean up.
	rows, err := tx.Query(ctx, `
		SELECT mil.content_id
		FROM media_item_libraries mil
		WHERE mil.media_folder_id = $1
		AND NOT EXISTS (
			SELECT 1 FROM media_item_libraries other
			WHERE other.content_id = mil.content_id
			AND other.media_folder_id != $1
		)`, id)
	if err != nil {
		return nil, fmt.Errorf("finding orphaned items: %w", err)
	}

	var orphanIDs []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning orphan content_id: %w", err)
		}
		orphanIDs = append(orphanIDs, contentID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating orphan rows: %w", err)
	}
	stats.OrphanedItems = len(orphanIDs)

	// Collect S3 image paths from orphaned items (and their seasons/episodes)
	// before deletion so the caller can clean up S3 objects afterward.
	if len(orphanIDs) > 0 {
		stats.OrphanedImageDirs, err = collectImageDirs(ctx, tx, orphanIDs)
		if err != nil {
			return nil, err
		}
	}

	// Delete the folder; CASCADE removes junction rows, media_files, and
	// library_provider_chains automatically.
	if progress != nil {
		progress(1, 3, "Deleting library folder data")
	}
	tag, err := tx.Exec(ctx, "DELETE FROM media_folders WHERE id = $1", id)
	if err != nil {
		return nil, fmt.Errorf("deleting folder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrFolderNotFound
	}

	// Remove orphaned media items (episodes CASCADE from media_items).
	if len(orphanIDs) > 0 {
		if progress != nil {
			progress(2, 3, "Deleting orphaned items")
		}
		_, err = tx.Exec(ctx, "DELETE FROM media_items WHERE content_id = ANY($1)", orphanIDs)
		if err != nil {
			return nil, fmt.Errorf("deleting orphaned items: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing delete transaction: %w", err)
	}
	if progress != nil {
		progress(3, 3, "Library deletion completed")
	}
	return stats, nil
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

// collectImageDirs returns S3 directory prefixes for all images belonging to
// the given content IDs (items, seasons, and episodes).
func collectImageDirs(ctx context.Context, tx pgx.Tx, contentIDs []string) ([]string, error) {
	imgRows, err := tx.Query(ctx, `
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
	return filterUnreferencedImageDirs(ctx, tx, dirs, contentIDs)
}

func filterUnreferencedImageDirs(ctx context.Context, tx pgx.Tx, dirs, deletingContentIDs []string) ([]string, error) {
	if len(dirs) == 0 {
		return nil, nil
	}

	rows, err := tx.Query(ctx, `
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
