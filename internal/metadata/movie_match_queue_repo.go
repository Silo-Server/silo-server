package metadata

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	scannerrepo "github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	movieQueueRetryDelay       = 15 * time.Second
	seriesRootQueueQuietWindow = 10 * time.Second
	seriesRootQueueRetryDelay  = 30 * time.Second

	// matchQueueRetryMaxDelay caps the exponential retry backoff shared by both
	// match queues. Terminal outcomes like "no metadata found from any
	// provider" stay in the queue but decay to at most one attempt per day
	// instead of hot-looping at the base delay forever. A row self-heals by
	// being deleted on the first successful attempt.
	matchQueueRetryMaxDelay = 24 * time.Hour
	// matchQueueBackoffMaxExponent clamps the 2^attempt_count factor so the
	// float math stays finite for rows that accumulated very large attempt
	// counts before backoff existed.
	matchQueueBackoffMaxExponent = 16
)

// matchQueueBackoffExpr returns the SQL expression both match queues use to
// schedule the next retry after a failure: base_delay * 2^attempt_count,
// capped at matchQueueRetryMaxDelay. attempt_count is incremented when a row
// is claimed, so the first failure backs off to twice the base delay.
// basePlaceholder and maxPlaceholder name the interval bind parameters (e.g.
// "$3", "$4") in the caller's statement.
func matchQueueBackoffExpr(basePlaceholder, maxPlaceholder string) string {
	return fmt.Sprintf(
		"NOW() + LEAST(%s::interval * power(2::float8, LEAST(attempt_count, %d)), %s::interval)",
		basePlaceholder, matchQueueBackoffMaxExponent, maxPlaceholder,
	)
}

type MovieMatchQueueRepository struct {
	pool     *pgxpool.Pool
	fileRepo *scannerrepo.FileRepository
}

func NewMovieMatchQueueRepository(pool *pgxpool.Pool, fileRepo *scannerrepo.FileRepository) *MovieMatchQueueRepository {
	return &MovieMatchQueueRepository{pool: pool, fileRepo: fileRepo}
}

func (r *MovieMatchQueueRepository) requireConfigured() error {
	if r == nil || r.pool == nil {
		return errors.New("movie match queue repository is not configured")
	}
	return nil
}

func requirePositiveMovieQueueID(label string, value int) error {
	if value <= 0 {
		return fmt.Errorf("%s must be positive", label)
	}
	return nil
}

// movieQueueFileEligibleCond is the predicate deciding whether a media file
// belongs in the movie match queue. Queries embedding it must alias
// media_files as mf, media_folders as folders, and media_items as mi.
//
// Files beneath a root skipped as misplaced series are excluded durably:
// their content_id is never set, so without the exclusion every library sync
// would re-enqueue them only for the worker to skip them again.
const movieQueueFileEligibleCond = `folders.enabled = true
	  AND (
		lower(trim(folders.type)) IN ('movie', 'movies') OR
		(lower(trim(folders.type)) = 'mixed' AND lower(trim(mf.base_type)) = 'movie')
	  )
	  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
	  AND (
		mf.content_id IS NULL OR mf.content_id = '' OR
		lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM skipped_media_roots sr
		WHERE sr.media_folder_id = mf.media_folder_id
		  AND sr.reason = '` + skippedReasonSeriesInMovieLibrary + `'
		  AND strpos(mf.file_path, sr.root_path || '/') = 1
	  )`

func (r *MovieMatchQueueRepository) EnqueueMovieFile(ctx context.Context, fileID int) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveMovieQueueID("file id", fileID); err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin movie queue enqueue transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO movie_match_queue (
			media_file_id,
			media_folder_id,
			available_at,
			updated_at
		)
		SELECT
			mf.id,
			mf.media_folder_id,
			NOW(),
			NOW()
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.id = $1
		  AND `+movieQueueFileEligibleCond+`
		ON CONFLICT (media_file_id) DO UPDATE
		SET media_folder_id = EXCLUDED.media_folder_id,
			available_at = GREATEST(movie_match_queue.available_at, EXCLUDED.available_at),
			updated_at = NOW()
	`, fileID); err != nil {
		return fmt.Errorf("upserting movie queue row: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM movie_match_queue q
		WHERE q.media_file_id = $1
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.id = q.media_file_id
			  AND `+movieQueueFileEligibleCond+`
		  )
	`, fileID); err != nil {
		return fmt.Errorf("deleting stale movie queue row: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit movie queue enqueue transaction: %w", err)
	}
	return nil
}

func (r *MovieMatchQueueRepository) SyncForFolder(ctx context.Context, folderID int) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveMovieQueueID("folder id", folderID); err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin movie queue sync transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO movie_match_queue (
			media_file_id,
			media_folder_id,
			available_at,
			updated_at
		)
		SELECT
			mf.id,
			mf.media_folder_id,
			NOW(),
			NOW()
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND `+movieQueueFileEligibleCond+`
		ON CONFLICT (media_file_id) DO UPDATE
		SET media_folder_id = EXCLUDED.media_folder_id,
			available_at = GREATEST(movie_match_queue.available_at, EXCLUDED.available_at),
			updated_at = NOW()
	`, folderID); err != nil {
		return fmt.Errorf("upserting movie queue rows for folder: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM movie_match_queue q
		WHERE q.media_folder_id = $1
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.id = q.media_file_id
			  AND `+movieQueueFileEligibleCond+`
		  )
	`, folderID); err != nil {
		return fmt.Errorf("deleting stale movie queue rows for folder: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit movie queue sync transaction: %w", err)
	}
	return nil
}

func (r *MovieMatchQueueRepository) SyncInScope(ctx context.Context, folderID int, scopePath string) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveMovieQueueID("folder id", folderID); err != nil {
		return err
	}
	if strings.TrimSpace(scopePath) == "" {
		return errors.New("scope path is required")
	}
	scopePath = filepath.Clean(scopePath)
	scopeLike := pathPrefixLike(scopePath)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin movie queue scoped sync transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO movie_match_queue (
			media_file_id,
			media_folder_id,
			available_at,
			updated_at
		)
		SELECT
			mf.id,
			mf.media_folder_id,
			NOW(),
			NOW()
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND `+movieQueueFileEligibleCond+`
		  AND (
			mf.file_path = $2 OR
			mf.file_path LIKE $3 ESCAPE '\'
		  )
		ON CONFLICT (media_file_id) DO UPDATE
		SET media_folder_id = EXCLUDED.media_folder_id,
			available_at = GREATEST(movie_match_queue.available_at, EXCLUDED.available_at),
			updated_at = NOW()
	`, folderID, scopePath, scopeLike); err != nil {
		return fmt.Errorf("upserting movie queue rows in scope: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM movie_match_queue q
		WHERE q.media_folder_id = $1
		  AND (
			EXISTS (
				SELECT 1
				FROM media_files scoped
				WHERE scoped.id = q.media_file_id
				  AND (
					scoped.file_path = $2 OR
					scoped.file_path LIKE $3 ESCAPE '\'
				  )
			)
			OR NOT EXISTS (
				SELECT 1
				FROM media_files any_rows
				WHERE any_rows.id = q.media_file_id
			)
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.id = q.media_file_id
			  AND `+movieQueueFileEligibleCond+`
		  )
	`, folderID, scopePath, scopeLike); err != nil {
		return fmt.Errorf("deleting stale movie queue rows in scope: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit movie queue scoped sync transaction: %w", err)
	}
	return nil
}

func (r *MovieMatchQueueRepository) Claim(ctx context.Context, limit int) ([]*models.MediaFile, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 500
	}

	rows, err := r.pool.Query(ctx, `
		WITH candidates AS (
			SELECT q.media_file_id, q.available_at, q.last_attempted_at
			FROM movie_match_queue q
			JOIN media_files mf ON mf.id = q.media_file_id
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE q.available_at <= NOW()
			  AND `+movieQueueFileEligibleCond+`
			ORDER BY q.available_at ASC, q.last_attempted_at ASC NULLS FIRST, q.media_file_id ASC
			LIMIT $1
			FOR UPDATE OF q SKIP LOCKED
		),
		updated AS (
			UPDATE movie_match_queue q
			SET last_attempted_at = NOW(),
				attempt_count = q.attempt_count + 1,
				updated_at = NOW()
			FROM candidates c
			WHERE q.media_file_id = c.media_file_id
			RETURNING q.media_file_id
		)
		SELECT c.media_file_id
		FROM candidates c
		JOIN updated u ON u.media_file_id = c.media_file_id
		ORDER BY c.available_at ASC, c.last_attempted_at ASC NULLS FIRST, c.media_file_id ASC
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming movie queue rows: %w", err)
	}
	defer rows.Close()

	ids, err := scanClaimedMovieIDs(rows)
	if err != nil {
		return nil, err
	}
	return r.loadFilesByIDs(ctx, ids)
}

func (r *MovieMatchQueueRepository) ClaimByFolderAndPathPrefix(
	ctx context.Context,
	folderID int,
	pathPrefix string,
	limit int,
	attemptBefore time.Time,
) ([]*models.MediaFile, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, err
	}
	if err := requirePositiveMovieQueueID("folder id", folderID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(pathPrefix) == "" {
		return nil, errors.New("path prefix is required")
	}
	if limit <= 0 {
		limit = 500
	}
	pathPrefix = filepath.Clean(pathPrefix)
	scopeLike := pathPrefixLike(pathPrefix)

	rows, err := r.pool.Query(ctx, `
		WITH candidates AS (
			SELECT q.media_file_id, q.available_at, q.last_attempted_at
			FROM movie_match_queue q
			JOIN media_files mf ON mf.id = q.media_file_id
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE q.media_folder_id = $1
			  AND q.available_at <= NOW()
			  AND `+movieQueueFileEligibleCond+`
			  AND (
				mf.file_path = $2 OR
				mf.file_path LIKE $3 ESCAPE '\'
			  )
			  AND ($4::timestamptz IS NULL OR q.last_attempted_at IS NULL OR q.last_attempted_at < $4)
			ORDER BY q.available_at ASC, q.last_attempted_at ASC NULLS FIRST, q.media_file_id ASC
			LIMIT $5
			FOR UPDATE OF q SKIP LOCKED
		),
		updated AS (
			UPDATE movie_match_queue q
			SET last_attempted_at = NOW(),
				attempt_count = q.attempt_count + 1,
				updated_at = NOW()
			FROM candidates c
			WHERE q.media_file_id = c.media_file_id
			RETURNING q.media_file_id
		)
		SELECT c.media_file_id
		FROM candidates c
		JOIN updated u ON u.media_file_id = c.media_file_id
		ORDER BY c.available_at ASC, c.last_attempted_at ASC NULLS FIRST, c.media_file_id ASC
	`, folderID, pathPrefix, scopeLike, nullTime(attemptBefore), limit)
	if err != nil {
		return nil, fmt.Errorf("claiming movie queue rows by scope: %w", err)
	}
	defer rows.Close()

	ids, err := scanClaimedMovieIDs(rows)
	if err != nil {
		return nil, err
	}
	return r.loadFilesByIDs(ctx, ids)
}

func (r *MovieMatchQueueRepository) Delete(ctx context.Context, mediaFileID int) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveMovieQueueID("media file id", mediaFileID); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM movie_match_queue
		WHERE media_file_id = $1
	`, mediaFileID); err != nil {
		return fmt.Errorf("deleting movie queue row: %w", err)
	}
	return nil
}

func (r *MovieMatchQueueRepository) DeleteByFolder(ctx context.Context, folderID int) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	if err := requirePositiveMovieQueueID("folder id", folderID); err != nil {
		return 0, err
	}
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM movie_match_queue
		WHERE media_folder_id = $1
	`, folderID)
	if err != nil {
		return 0, fmt.Errorf("deleting movie queue rows for folder: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *MovieMatchQueueRepository) UpdateError(ctx context.Context, mediaFileID int, errText string) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveMovieQueueID("media file id", mediaFileID); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE movie_match_queue
		SET last_error = $2,
			available_at = `+matchQueueBackoffExpr("$3", "$4")+`,
			updated_at = NOW()
		WHERE media_file_id = $1
	`, mediaFileID, errText, intervalLiteral(movieQueueRetryDelay), intervalLiteral(matchQueueRetryMaxDelay)); err != nil {
		return fmt.Errorf("updating movie queue error: %w", err)
	}
	return nil
}

func (r *MovieMatchQueueRepository) ListByFolder(ctx context.Context, folderID int, limit int, offset int) ([]models.MovieMatchQueueEntry, int, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, 0, err
	}
	if err := requirePositiveMovieQueueID("folder id", folderID); err != nil {
		return nil, 0, err
	}

	var total int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM movie_match_queue WHERE media_folder_id = $1
	`, folderID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting movie queue rows: %w", err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT
			q.media_file_id,
			q.media_folder_id,
			COALESCE(mf.file_path, '') AS file_path,
			q.first_queued_at,
			q.available_at,
			q.last_attempted_at,
			q.attempt_count,
			q.last_error,
			q.updated_at
		FROM movie_match_queue q
		LEFT JOIN media_files mf ON mf.id = q.media_file_id
		WHERE q.media_folder_id = $1
		ORDER BY q.available_at ASC, q.last_attempted_at ASC NULLS FIRST, q.media_file_id ASC
		LIMIT $2 OFFSET $3
	`, folderID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing movie queue rows: %w", err)
	}
	defer rows.Close()

	out := make([]models.MovieMatchQueueEntry, 0)
	for rows.Next() {
		var entry models.MovieMatchQueueEntry
		if err := rows.Scan(
			&entry.MediaFileID,
			&entry.MediaFolderID,
			&entry.FilePath,
			&entry.FirstQueuedAt,
			&entry.AvailableAt,
			&entry.LastAttemptedAt,
			&entry.AttemptCount,
			&entry.LastError,
			&entry.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scanning movie queue row: %w", err)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating movie queue rows: %w", err)
	}
	return out, total, nil
}

func (r *MovieMatchQueueRepository) CountByFolder(ctx context.Context, folderID int) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	if err := requirePositiveMovieQueueID("folder id", folderID); err != nil {
		return 0, err
	}
	var total int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM movie_match_queue WHERE media_folder_id = $1
	`, folderID).Scan(&total); err != nil {
		return 0, fmt.Errorf("counting movie queue rows: %w", err)
	}
	return total, nil
}

func (r *MovieMatchQueueRepository) loadFilesByIDs(ctx context.Context, ids []int) ([]*models.MediaFile, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if r.fileRepo == nil {
		return nil, fmt.Errorf("movie queue file repo is not configured")
	}

	loaded, err := r.fileRepo.GetByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("loading queued movie files: %w", err)
	}
	filesByID := make(map[int]*models.MediaFile, len(loaded))
	for _, file := range loaded {
		filesByID[file.ID] = file
	}

	files := make([]*models.MediaFile, 0, len(ids))
	for _, id := range ids {
		file := filesByID[id]
		if file == nil {
			if err := r.Delete(ctx, id); err != nil {
				return nil, fmt.Errorf("deleting stale movie queue row %d: %w", id, err)
			}
			continue
		}
		files = append(files, file)
	}
	return files, nil
}

func scanClaimedMovieIDs(rows pgx.Rows) ([]int, error) {
	ids := make([]int, 0)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning claimed movie queue row: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating claimed movie queue rows: %w", err)
	}
	return ids, nil
}

func intervalLiteral(d time.Duration) string {
	return fmt.Sprintf("%.0f seconds", d.Seconds())
}
