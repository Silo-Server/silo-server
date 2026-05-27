package metadata

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pathscope"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SeriesRootMatchQueueRepository struct {
	pool *pgxpool.Pool
}

func NewSeriesRootMatchQueueRepository(pool *pgxpool.Pool) *SeriesRootMatchQueueRepository {
	return &SeriesRootMatchQueueRepository{pool: pool}
}

func (r *SeriesRootMatchQueueRepository) requireConfigured() error {
	if r == nil || r.pool == nil {
		return errors.New("series root match queue repository is not configured")
	}
	return nil
}

func requirePositiveSeriesQueueID(label string, value int) error {
	if value <= 0 {
		return fmt.Errorf("%s must be positive", label)
	}
	return nil
}

func (r *SeriesRootMatchQueueRepository) CleanupLegacySeriesGroupQueue(ctx context.Context) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM series_match_queue q
		USING media_folders folders
		WHERE folders.id = q.media_folder_id
		  AND lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows')
	`)
	if err != nil {
		return 0, fmt.Errorf("cleaning legacy series group queue rows: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *SeriesRootMatchQueueRepository) EnqueueSeriesRoot(ctx context.Context, folderID int, observedRootPath string) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return err
	}
	if strings.TrimSpace(observedRootPath) == "" {
		return errors.New("observed root path is required")
	}
	observedRootPath = filepath.Clean(observedRootPath)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin series root queue enqueue transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO series_root_match_queue (
			media_folder_id,
			observed_root_path,
			available_at,
			updated_at
		)
		SELECT DISTINCT
			mf.media_folder_id,
			mf.observed_root_path,
			NOW() + $3::interval,
			NOW()
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND mf.observed_root_path = $2
		  AND folders.enabled = true
		  AND lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows')
		  AND mf.missing_since IS NULL
		  AND mf.observed_root_path <> ''
		  AND (
			mf.content_id IS NULL OR mf.content_id = '' OR
			lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
		  )
		ON CONFLICT (media_folder_id, observed_root_path)
		DO UPDATE SET
			available_at = GREATEST(series_root_match_queue.available_at, EXCLUDED.available_at),
			updated_at = NOW()
	`, folderID, observedRootPath, intervalLiteral(seriesRootQueueQuietWindow)); err != nil {
		return fmt.Errorf("upserting series root queue row: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM series_root_match_queue q
		WHERE q.media_folder_id = $1
		  AND q.observed_root_path = $2
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.media_folder_id = q.media_folder_id
			  AND mf.observed_root_path = q.observed_root_path
			  AND folders.enabled = true
			  AND (
				lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows') OR
				(lower(trim(folders.type)) = 'mixed' AND lower(trim(mf.base_type)) = 'series')
			  )
			  AND mf.missing_since IS NULL
			  AND mf.observed_root_path <> ''
			  AND (
				mf.content_id IS NULL OR mf.content_id = '' OR
				lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
			  )
		  )
	`, folderID, observedRootPath); err != nil {
		return fmt.Errorf("deleting stale series root queue row: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit series root queue enqueue transaction: %w", err)
	}
	return nil
}

func (r *SeriesRootMatchQueueRepository) SyncForFolder(ctx context.Context, folderID int) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin series root queue sync transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO series_root_match_queue (
			media_folder_id,
			observed_root_path,
			available_at,
			updated_at
		)
		SELECT DISTINCT
			mf.media_folder_id,
			mf.observed_root_path,
			NOW() + $2::interval,
			NOW()
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND folders.enabled = true
		  AND (
			lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows') OR
			(lower(trim(folders.type)) = 'mixed' AND lower(trim(mf.base_type)) = 'series')
		  )
		  AND mf.missing_since IS NULL
		  AND mf.observed_root_path IS NOT NULL
		  AND mf.observed_root_path <> ''
		  AND (
			mf.content_id IS NULL OR mf.content_id = '' OR
			lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
		  )
		ON CONFLICT (media_folder_id, observed_root_path)
		DO UPDATE SET
			available_at = GREATEST(series_root_match_queue.available_at, EXCLUDED.available_at),
			updated_at = NOW()
	`, folderID, intervalLiteral(seriesRootQueueQuietWindow)); err != nil {
		return fmt.Errorf("upserting series root queue rows for folder: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM series_root_match_queue q
		WHERE q.media_folder_id = $1
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.media_folder_id = q.media_folder_id
			  AND mf.observed_root_path = q.observed_root_path
			  AND folders.enabled = true
			  AND (
				lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows') OR
				(lower(trim(folders.type)) = 'mixed' AND lower(trim(mf.base_type)) = 'series')
			  )
			  AND mf.missing_since IS NULL
			  AND mf.observed_root_path <> ''
			  AND (
				mf.content_id IS NULL OR mf.content_id = '' OR
				lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
			  )
		  )
	`, folderID); err != nil {
		return fmt.Errorf("deleting stale series root queue rows for folder: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit series root queue sync transaction: %w", err)
	}
	return nil
}

func (r *SeriesRootMatchQueueRepository) SyncInScope(ctx context.Context, folderID int, scopePath string) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return err
	}
	if strings.TrimSpace(scopePath) == "" {
		return errors.New("scope path is required")
	}
	scopePath = filepath.Clean(scopePath)
	scopeLike := pathPrefixLike(scopePath)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin series root queue scoped sync transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		WITH in_scope_roots AS (
			SELECT DISTINCT
				mf.media_folder_id,
				mf.observed_root_path
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.media_folder_id = $1
			  AND folders.enabled = true
			  AND (
				lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows') OR
				(lower(trim(folders.type)) = 'mixed' AND lower(trim(mf.base_type)) = 'series')
			  )
			  AND mf.missing_since IS NULL
			  AND mf.observed_root_path IS NOT NULL
			  AND mf.observed_root_path <> ''
			  AND (
				mf.file_path = $2 OR mf.file_path LIKE $3 ESCAPE '\' OR
				mf.observed_root_path = $2 OR mf.observed_root_path LIKE $3 ESCAPE '\'
			  )
			  AND (
				mf.content_id IS NULL OR mf.content_id = '' OR
				lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
			  )
		)
		INSERT INTO series_root_match_queue (
			media_folder_id,
			observed_root_path,
			available_at,
			updated_at
		)
		SELECT media_folder_id, observed_root_path, NOW() + $4::interval, NOW()
		FROM in_scope_roots
		ON CONFLICT (media_folder_id, observed_root_path)
		DO UPDATE SET
			available_at = GREATEST(series_root_match_queue.available_at, EXCLUDED.available_at),
			updated_at = NOW()
	`, folderID, scopePath, scopeLike, intervalLiteral(seriesRootQueueQuietWindow)); err != nil {
		return fmt.Errorf("upserting series root queue rows in scope: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM series_root_match_queue q
		WHERE q.media_folder_id = $1
		  AND (
			EXISTS (
				SELECT 1
				FROM media_files touched
				WHERE touched.media_folder_id = q.media_folder_id
				  AND touched.observed_root_path = q.observed_root_path
				  AND (
					touched.file_path = $2 OR touched.file_path LIKE $3 ESCAPE '\' OR
					touched.observed_root_path = $2 OR touched.observed_root_path LIKE $3 ESCAPE '\'
				  )
			)
			OR NOT EXISTS (
				SELECT 1
				FROM media_files any_rows
				WHERE any_rows.media_folder_id = q.media_folder_id
				  AND any_rows.observed_root_path = q.observed_root_path
			)
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.media_folder_id = q.media_folder_id
			  AND mf.observed_root_path = q.observed_root_path
			  AND folders.enabled = true
			  AND (
				lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows') OR
				(lower(trim(folders.type)) = 'mixed' AND lower(trim(mf.base_type)) = 'series')
			  )
			  AND mf.missing_since IS NULL
			  AND mf.observed_root_path <> ''
			  AND (
				mf.content_id IS NULL OR mf.content_id = '' OR
				lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
			  )
		  )
	`, folderID, scopePath, scopeLike); err != nil {
		return fmt.Errorf("deleting stale series root queue rows in scope: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit series root queue scoped sync transaction: %w", err)
	}
	return nil
}

func (r *SeriesRootMatchQueueRepository) Claim(ctx context.Context, limit int) ([]models.SeriesRootMatchJob, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 500
	}

	rows, err := r.pool.Query(ctx, `
		WITH candidates AS (
			SELECT q.media_folder_id, q.observed_root_path
			FROM series_root_match_queue q
			JOIN media_folders folders ON folders.id = q.media_folder_id
			WHERE q.available_at <= NOW()
			  AND folders.enabled = true
			  AND lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows', 'mixed')
			  AND EXISTS (
				SELECT 1
				FROM media_files mf
				LEFT JOIN media_items mi ON mi.content_id = mf.content_id
				WHERE mf.media_folder_id = q.media_folder_id
				  AND mf.observed_root_path = q.observed_root_path
				  AND (
					lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows') OR
					lower(trim(mf.base_type)) = 'series'
				  )
				  AND mf.missing_since IS NULL
				  AND mf.observed_root_path <> ''
				  AND (
					mf.content_id IS NULL OR mf.content_id = '' OR
					lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
				  )
			  )
			ORDER BY q.available_at ASC, q.last_attempted_at ASC NULLS FIRST, q.media_folder_id ASC, q.observed_root_path ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		),
		updated AS (
			UPDATE series_root_match_queue q
			SET last_attempted_at = NOW(),
				attempt_count = q.attempt_count + 1,
				updated_at = NOW()
			FROM candidates c
			WHERE q.media_folder_id = c.media_folder_id
			  AND q.observed_root_path = c.observed_root_path
			RETURNING q.media_folder_id, q.observed_root_path
		)
		SELECT
			u.media_folder_id,
			u.observed_root_path,
			COALESCE(loc.sample_file_path, '') AS sample_file_path,
			COALESCE(loc.observed_file_count, (
				SELECT COUNT(*)
				FROM media_files mf
				WHERE mf.media_folder_id = u.media_folder_id
				  AND mf.observed_root_path = u.observed_root_path
				  AND mf.missing_since IS NULL
			)) AS observed_file_count
		FROM updated u
		LEFT JOIN observed_media_locations loc
		  ON loc.media_folder_id = u.media_folder_id
		 AND loc.observed_root_path = u.observed_root_path
		ORDER BY u.media_folder_id ASC, u.observed_root_path ASC
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming series root queue rows: %w", err)
	}
	defer rows.Close()

	return scanSeriesRootJobs(rows)
}

func (r *SeriesRootMatchQueueRepository) ClaimByFolderAndPathPrefix(
	ctx context.Context,
	folderID int,
	pathPrefix string,
	limit int,
	attemptBefore time.Time,
) ([]models.SeriesRootMatchJob, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
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
			SELECT q.media_folder_id, q.observed_root_path
			FROM series_root_match_queue q
			JOIN media_folders folders ON folders.id = q.media_folder_id
			WHERE q.media_folder_id = $1
			  AND q.available_at <= NOW()
			  AND folders.enabled = true
			  AND lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows', 'mixed')
			  AND (
				q.observed_root_path = $2 OR q.observed_root_path LIKE $3 ESCAPE '\' OR
				EXISTS (
					SELECT 1
					FROM media_files mf
					WHERE mf.media_folder_id = q.media_folder_id
					  AND mf.observed_root_path = q.observed_root_path
					  AND mf.missing_since IS NULL
					  AND (mf.file_path = $2 OR mf.file_path LIKE $3 ESCAPE '\')
				)
			  )
			  AND EXISTS (
				SELECT 1
				FROM media_files mf
				LEFT JOIN media_items mi ON mi.content_id = mf.content_id
				WHERE mf.media_folder_id = q.media_folder_id
				  AND mf.observed_root_path = q.observed_root_path
				  AND (
					lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows') OR
					lower(trim(mf.base_type)) = 'series'
				  )
				  AND mf.missing_since IS NULL
				  AND mf.observed_root_path <> ''
				  AND (
					mf.content_id IS NULL OR mf.content_id = '' OR
					lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
				  )
			  )
			  AND ($4::timestamptz IS NULL OR q.last_attempted_at IS NULL OR q.last_attempted_at < $4)
			ORDER BY q.available_at ASC, q.last_attempted_at ASC NULLS FIRST, q.observed_root_path ASC
			LIMIT $5
			FOR UPDATE SKIP LOCKED
		),
		updated AS (
			UPDATE series_root_match_queue q
			SET last_attempted_at = NOW(),
				attempt_count = q.attempt_count + 1,
				updated_at = NOW()
			FROM candidates c
			WHERE q.media_folder_id = c.media_folder_id
			  AND q.observed_root_path = c.observed_root_path
			RETURNING q.media_folder_id, q.observed_root_path
		)
		SELECT
			u.media_folder_id,
			u.observed_root_path,
			COALESCE(loc.sample_file_path, '') AS sample_file_path,
			COALESCE(loc.observed_file_count, (
				SELECT COUNT(*)
				FROM media_files mf
				WHERE mf.media_folder_id = u.media_folder_id
				  AND mf.observed_root_path = u.observed_root_path
				  AND mf.missing_since IS NULL
			)) AS observed_file_count
		FROM updated u
		LEFT JOIN observed_media_locations loc
		  ON loc.media_folder_id = u.media_folder_id
		 AND loc.observed_root_path = u.observed_root_path
		ORDER BY u.observed_root_path ASC
	`, folderID, pathPrefix, scopeLike, nullTime(attemptBefore), limit)
	if err != nil {
		return nil, fmt.Errorf("claiming series root queue rows by scope: %w", err)
	}
	defer rows.Close()

	return scanSeriesRootJobs(rows)
}

func (r *SeriesRootMatchQueueRepository) Delete(ctx context.Context, folderID int, observedRootPath string) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return err
	}
	if strings.TrimSpace(observedRootPath) == "" {
		return errors.New("observed root path is required")
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM series_root_match_queue
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, folderID, filepath.Clean(observedRootPath))
	if err != nil {
		return fmt.Errorf("deleting series root queue row: %w", err)
	}
	return nil
}

func (r *SeriesRootMatchQueueRepository) UpdateError(ctx context.Context, folderID int, observedRootPath string, errText string) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return err
	}
	if strings.TrimSpace(observedRootPath) == "" {
		return errors.New("observed root path is required")
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE series_root_match_queue
		SET last_error = $3,
			available_at = NOW() + $4::interval,
			updated_at = NOW()
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, folderID, filepath.Clean(observedRootPath), errText, intervalLiteral(seriesRootQueueRetryDelay))
	if err != nil {
		return fmt.Errorf("updating series root queue error: %w", err)
	}
	return nil
}

func (r *SeriesRootMatchQueueRepository) ListByFolder(ctx context.Context, folderID int, limit int, offset int) ([]models.SeriesRootMatchQueueEntry, int, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, 0, err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return nil, 0, err
	}

	var total int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM series_root_match_queue WHERE media_folder_id = $1
	`, folderID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting series root queue rows: %w", err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT media_folder_id, observed_root_path, first_queued_at, available_at, last_attempted_at, attempt_count, last_error, updated_at
		FROM series_root_match_queue
		WHERE media_folder_id = $1
		ORDER BY available_at ASC, last_attempted_at ASC NULLS FIRST, observed_root_path ASC
		LIMIT $2 OFFSET $3
	`, folderID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing series root queue rows: %w", err)
	}
	defer rows.Close()

	out := make([]models.SeriesRootMatchQueueEntry, 0)
	for rows.Next() {
		var entry models.SeriesRootMatchQueueEntry
		if err := rows.Scan(
			&entry.MediaFolderID,
			&entry.ObservedRootPath,
			&entry.FirstQueuedAt,
			&entry.AvailableAt,
			&entry.LastAttemptedAt,
			&entry.AttemptCount,
			&entry.LastError,
			&entry.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scanning series root queue row: %w", err)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating series root queue rows: %w", err)
	}
	return out, total, nil
}

func (r *SeriesRootMatchQueueRepository) CountByFolder(ctx context.Context, folderID int) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return 0, err
	}
	var total int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM series_root_match_queue WHERE media_folder_id = $1
	`, folderID).Scan(&total); err != nil {
		return 0, fmt.Errorf("counting series root queue rows: %w", err)
	}
	return total, nil
}

func scanSeriesRootJobs(rows pgx.Rows) ([]models.SeriesRootMatchJob, error) {
	jobs := make([]models.SeriesRootMatchJob, 0)
	for rows.Next() {
		var job models.SeriesRootMatchJob
		if err := rows.Scan(
			&job.MediaFolderID,
			&job.ObservedRootPath,
			&job.SampleFilePath,
			&job.ObservedFileCount,
		); err != nil {
			return nil, fmt.Errorf("scanning series root job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating series root jobs: %w", err)
	}
	return jobs, nil
}

func nullTime(ts time.Time) *time.Time {
	if ts.IsZero() {
		return nil
	}
	return &ts
}

func pathPrefixLike(pathPrefix string) string {
	return pathscope.PrefixLike(pathPrefix)
}
