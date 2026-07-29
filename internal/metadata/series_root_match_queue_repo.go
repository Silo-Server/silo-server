package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pathscope"
	"github.com/google/uuid"
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
		WITH eligible_roots AS (
			SELECT DISTINCT
				mf.media_folder_id,
				mf.observed_root_path,
				COALESCE(folders.metadata_language, '') AS metadata_language
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.media_folder_id = $1
			  AND mf.observed_root_path = $2
			  AND folders.enabled = true
			  AND (
				lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows') OR
				(lower(trim(folders.type)) = 'mixed' AND lower(trim(mf.base_type)) = 'series')
			  )
			  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
			  AND mf.observed_root_path <> ''
			  AND (
				mf.content_id IS NULL OR mf.content_id = '' OR
				lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
			  )
		)
		INSERT INTO series_root_match_queue (
			media_folder_id,
			observed_root_path,
			input_fingerprint,
			matcher_revision,
			available_at,
			updated_at
		)
		SELECT
			roots.media_folder_id,
			roots.observed_root_path,
			`+seriesMatchQueueInputFingerprintSQL("roots.observed_root_path", "roots.media_folder_id", "roots.metadata_language")+`,
			`+fmt.Sprintf("%d", seriesMatcherRevision)+`,
			NOW() + $3::interval,
			NOW()
		FROM eligible_roots roots
		ON CONFLICT (media_folder_id, observed_root_path)
		DO UPDATE SET
			available_at = CASE
				WHEN (series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision) AND series_root_match_queue.lease_token = '' THEN LEAST(series_root_match_queue.available_at, EXCLUDED.available_at)
				WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN series_root_match_queue.available_at
				ELSE GREATEST(series_root_match_queue.available_at, EXCLUDED.available_at)
			END,
			state = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 'pending' ELSE series_root_match_queue.state END,
			deterministic_attempt_count = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 0 ELSE series_root_match_queue.deterministic_attempt_count END,
			failure_kind = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE series_root_match_queue.failure_kind END,
			failure_detail = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '{}'::jsonb ELSE series_root_match_queue.failure_detail END,
			last_error = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE series_root_match_queue.last_error END,
			parked_at = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN NULL ELSE series_root_match_queue.parked_at END,
			rerun_requested = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN true ELSE series_root_match_queue.rerun_requested END,
			input_fingerprint = EXCLUDED.input_fingerprint,
			matcher_revision = EXCLUDED.matcher_revision,
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
			  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
			  AND mf.observed_root_path <> ''
			  AND (q.rerun_requested OR q.lease_forced_rerun OR (
				mf.content_id IS NULL OR mf.content_id = '' OR
				lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
			  ))
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
		WITH eligible_roots AS (
			SELECT DISTINCT
				mf.media_folder_id,
				mf.observed_root_path,
				COALESCE(folders.metadata_language, '') AS metadata_language
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.media_folder_id = $1
			  AND folders.enabled = true
			  AND (
				lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows') OR
				(lower(trim(folders.type)) = 'mixed' AND lower(trim(mf.base_type)) = 'series')
			  )
			  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
			  AND mf.observed_root_path IS NOT NULL
			  AND mf.observed_root_path <> ''
			  AND (
				mf.content_id IS NULL OR mf.content_id = '' OR
				lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
			  )
		)
		INSERT INTO series_root_match_queue (
			media_folder_id,
			observed_root_path,
			input_fingerprint,
			matcher_revision,
			available_at,
			updated_at
		)
		SELECT
			roots.media_folder_id,
			roots.observed_root_path,
			`+seriesMatchQueueInputFingerprintSQL("roots.observed_root_path", "roots.media_folder_id", "roots.metadata_language")+`,
			`+fmt.Sprintf("%d", seriesMatcherRevision)+`,
			NOW() + $2::interval,
			NOW()
		FROM eligible_roots roots
		ON CONFLICT (media_folder_id, observed_root_path)
		DO UPDATE SET
			available_at = CASE
				WHEN (series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision) AND series_root_match_queue.lease_token = '' THEN LEAST(series_root_match_queue.available_at, EXCLUDED.available_at)
				WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN series_root_match_queue.available_at
				ELSE GREATEST(series_root_match_queue.available_at, EXCLUDED.available_at)
			END,
			state = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 'pending' ELSE series_root_match_queue.state END,
			deterministic_attempt_count = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 0 ELSE series_root_match_queue.deterministic_attempt_count END,
			failure_kind = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE series_root_match_queue.failure_kind END,
			failure_detail = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '{}'::jsonb ELSE series_root_match_queue.failure_detail END,
			last_error = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE series_root_match_queue.last_error END,
			parked_at = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN NULL ELSE series_root_match_queue.parked_at END,
			rerun_requested = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN true ELSE series_root_match_queue.rerun_requested END,
			input_fingerprint = EXCLUDED.input_fingerprint,
			matcher_revision = EXCLUDED.matcher_revision,
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
			  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
			  AND mf.observed_root_path <> ''
			  AND (q.rerun_requested OR q.lease_forced_rerun OR (
				mf.content_id IS NULL OR mf.content_id = '' OR
				lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
			  ))
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
				mf.observed_root_path,
				COALESCE(folders.metadata_language, '') AS metadata_language
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.media_folder_id = $1
			  AND folders.enabled = true
			  AND (
				lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows') OR
				(lower(trim(folders.type)) = 'mixed' AND lower(trim(mf.base_type)) = 'series')
			  )
			  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
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
			input_fingerprint,
			matcher_revision,
			available_at,
			updated_at
		)
		SELECT roots.media_folder_id, roots.observed_root_path,
			`+seriesMatchQueueInputFingerprintSQL("roots.observed_root_path", "roots.media_folder_id", "roots.metadata_language")+`,
			`+fmt.Sprintf("%d", seriesMatcherRevision)+`, NOW() + $4::interval, NOW()
		FROM in_scope_roots roots
		ON CONFLICT (media_folder_id, observed_root_path)
		DO UPDATE SET
			available_at = CASE
				WHEN (series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision) AND series_root_match_queue.lease_token = '' THEN LEAST(series_root_match_queue.available_at, EXCLUDED.available_at)
				WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN series_root_match_queue.available_at
				ELSE GREATEST(series_root_match_queue.available_at, EXCLUDED.available_at)
			END,
			state = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 'pending' ELSE series_root_match_queue.state END,
			deterministic_attempt_count = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 0 ELSE series_root_match_queue.deterministic_attempt_count END,
			failure_kind = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE series_root_match_queue.failure_kind END,
			failure_detail = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '{}'::jsonb ELSE series_root_match_queue.failure_detail END,
			last_error = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE series_root_match_queue.last_error END,
			parked_at = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN NULL ELSE series_root_match_queue.parked_at END,
			rerun_requested = CASE WHEN series_root_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR series_root_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN true ELSE series_root_match_queue.rerun_requested END,
			input_fingerprint = EXCLUDED.input_fingerprint,
			matcher_revision = EXCLUDED.matcher_revision,
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
			  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
			  AND mf.observed_root_path <> ''
			  AND (q.rerun_requested OR q.lease_forced_rerun OR (
				mf.content_id IS NULL OR mf.content_id = '' OR
				lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
			  ))
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
	leaseToken := uuid.NewString()

	rows, err := r.pool.Query(ctx, `
		WITH candidates AS (
			SELECT q.media_folder_id, q.observed_root_path,
				(q.rerun_requested OR q.lease_forced_rerun) AS rerun_requested
			FROM series_root_match_queue q
			JOIN media_folders folders ON folders.id = q.media_folder_id
			WHERE q.state = 'pending'
			  AND q.available_at <= NOW()
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
				  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
				  AND mf.observed_root_path <> ''
				  AND (q.rerun_requested OR q.lease_forced_rerun OR (
					mf.content_id IS NULL OR mf.content_id = '' OR
					lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
				  ))
			  )
			ORDER BY q.available_at ASC, q.last_attempted_at ASC NULLS FIRST, q.media_folder_id ASC, q.observed_root_path ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		),
		updated AS (
			UPDATE series_root_match_queue q
			SET last_attempted_at = NOW(),
				attempt_count = q.attempt_count + 1,
				available_at = NOW() + $2::interval,
				lease_token = $3,
				lease_forced_rerun = c.rerun_requested,
				rerun_requested = false,
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
				  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
			)) AS observed_file_count,
			c.rerun_requested
		FROM updated u
		JOIN candidates c
		  ON c.media_folder_id = u.media_folder_id
		 AND c.observed_root_path = u.observed_root_path
		LEFT JOIN observed_media_locations loc
		  ON loc.media_folder_id = u.media_folder_id
		 AND loc.observed_root_path = u.observed_root_path
		ORDER BY u.media_folder_id ASC, u.observed_root_path ASC
	`, limit, intervalLiteral(matchQueueClaimLease), leaseToken)
	if err != nil {
		return nil, fmt.Errorf("claiming series root queue rows: %w", err)
	}
	defer rows.Close()

	jobs, err := scanSeriesRootJobs(rows)
	if err != nil {
		rows.Close()
		return nil, r.releaseClaimAfterError(ctx, leaseToken, err)
	}
	for i := range jobs {
		jobs[i].LeaseToken = leaseToken
	}
	return jobs, nil
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
	leaseToken := uuid.NewString()
	pathPrefix = filepath.Clean(pathPrefix)
	scopeLike := pathPrefixLike(pathPrefix)

	rows, err := r.pool.Query(ctx, `
		WITH candidates AS (
			SELECT q.media_folder_id, q.observed_root_path,
				(q.rerun_requested OR q.lease_forced_rerun) AS rerun_requested
			FROM series_root_match_queue q
			JOIN media_folders folders ON folders.id = q.media_folder_id
			WHERE q.media_folder_id = $1
			  AND q.state = 'pending'
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
					  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
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
				  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
				  AND mf.observed_root_path <> ''
				  AND (q.rerun_requested OR q.lease_forced_rerun OR (
					mf.content_id IS NULL OR mf.content_id = '' OR
					lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
				  ))
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
				available_at = NOW() + $6::interval,
				lease_token = $7,
				lease_forced_rerun = c.rerun_requested,
				rerun_requested = false,
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
				  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
			)) AS observed_file_count,
			c.rerun_requested
		FROM updated u
		JOIN candidates c
		  ON c.media_folder_id = u.media_folder_id
		 AND c.observed_root_path = u.observed_root_path
		LEFT JOIN observed_media_locations loc
		  ON loc.media_folder_id = u.media_folder_id
		 AND loc.observed_root_path = u.observed_root_path
		ORDER BY u.observed_root_path ASC
	`, folderID, pathPrefix, scopeLike, nullTime(attemptBefore), limit, intervalLiteral(matchQueueClaimLease), leaseToken)
	if err != nil {
		return nil, fmt.Errorf("claiming series root queue rows by scope: %w", err)
	}
	defer rows.Close()

	jobs, err := scanSeriesRootJobs(rows)
	if err != nil {
		rows.Close()
		return nil, r.releaseClaimAfterError(ctx, leaseToken, err)
	}
	for i := range jobs {
		jobs[i].LeaseToken = leaseToken
	}
	return jobs, nil
}

func (r *SeriesRootMatchQueueRepository) Delete(ctx context.Context, folderID int, observedRootPath, leaseToken string) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return err
	}
	if strings.TrimSpace(observedRootPath) == "" {
		return errors.New("observed root path is required")
	}
	if strings.TrimSpace(leaseToken) == "" {
		return errors.New("lease token is required")
	}
	_, err := r.pool.Exec(ctx, `
		WITH rerun AS (
			UPDATE series_root_match_queue
			SET available_at = NOW(),
				lease_token = '',
				lease_forced_rerun = false,
				updated_at = NOW()
			WHERE media_folder_id = $1
			  AND observed_root_path = $2
			  AND lease_token = $3
			  AND rerun_requested
			RETURNING media_folder_id
		)
		DELETE FROM series_root_match_queue
		WHERE media_folder_id = $1
		  AND observed_root_path = $2
		  AND lease_token = $3
		  AND NOT rerun_requested
	`, folderID, filepath.Clean(observedRootPath), leaseToken)
	if err != nil {
		return fmt.Errorf("deleting series root queue row: %w", err)
	}
	return nil
}

func (r *SeriesRootMatchQueueRepository) DeleteByFolder(ctx context.Context, folderID int) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return 0, err
	}
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM series_root_match_queue
		WHERE media_folder_id = $1
	`, folderID)
	if err != nil {
		return 0, fmt.Errorf("deleting series root queue rows for folder: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *SeriesRootMatchQueueRepository) UpdateError(ctx context.Context, folderID int, observedRootPath, leaseToken, errText string) error {
	return r.UpdateFailure(ctx, folderID, observedRootPath, leaseToken, MatchFailure{Kind: MatchOutcomeProviderTransient, Message: errText})
}

func (r *SeriesRootMatchQueueRepository) UpdateFailure(ctx context.Context, folderID int, observedRootPath, leaseToken string, failure MatchFailure) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return err
	}
	if strings.TrimSpace(observedRootPath) == "" {
		return errors.New("observed root path is required")
	}
	if strings.TrimSpace(leaseToken) == "" {
		return errors.New("lease token is required")
	}
	kind := normalizeMatchFailureKind(failure.Kind)
	message := boundedMatchFailureMessage(failure.Message)
	detail, err := json.Marshal(map[string]any{"message": message, "decision": boundedMatchDecision(failure.Decision)})
	if err != nil {
		return fmt.Errorf("encoding series root queue failure: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		UPDATE series_root_match_queue
		SET last_error = CASE WHEN rerun_requested THEN last_error ELSE left($3, 2000) END,
			failure_kind = CASE WHEN rerun_requested THEN failure_kind ELSE $4 END,
			failure_detail = CASE WHEN rerun_requested THEN failure_detail ELSE $5::jsonb END,
			deterministic_attempt_count = CASE
				WHEN rerun_requested THEN deterministic_attempt_count
				ELSE deterministic_attempt_count + CASE WHEN $4 = 'provider_transient' THEN 0 ELSE 1 END
			END,
			state = CASE
				WHEN rerun_requested THEN 'pending'
				WHEN $4 <> 'provider_transient' AND deterministic_attempt_count + 1 >= 3 THEN 'parked'
				ELSE 'pending'
			END,
			parked_at = CASE
				WHEN rerun_requested THEN NULL
				WHEN $4 <> 'provider_transient' AND deterministic_attempt_count + 1 >= 3 THEN NOW()
				ELSE NULL
			END,
			available_at = CASE
				WHEN rerun_requested THEN NOW()
				WHEN $4 = 'provider_transient' THEN `+matchQueueBackoffExpr("$6", "$7")+`
				WHEN deterministic_attempt_count + 1 = 1 THEN NOW() + interval '1 hour'
				ELSE NOW() + interval '24 hours'
			END,
			lease_token = '',
			rerun_requested = rerun_requested OR lease_forced_rerun,
			lease_forced_rerun = false,
			updated_at = NOW()
		WHERE media_folder_id = $1 AND observed_root_path = $2 AND lease_token = $8
	`, folderID, filepath.Clean(observedRootPath), message, kind, detail, intervalLiteral(seriesRootQueueRetryDelay), intervalLiteral(matchQueueRetryMaxDelay), leaseToken)
	if err != nil {
		return fmt.Errorf("updating series root queue error: %w", err)
	}
	return nil
}

func (r *SeriesRootMatchQueueRepository) RetryNowByFolder(ctx context.Context, folderID int) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return 0, err
	}
	tag, err := r.pool.Exec(ctx, `
		WITH current_inputs AS (
			SELECT q.media_folder_id, q.observed_root_path,
				`+seriesMatchQueueInputFingerprintSQL("q.observed_root_path", "q.media_folder_id", "folders.metadata_language")+` AS input_fingerprint
			FROM series_root_match_queue q
			JOIN media_folders folders ON folders.id = q.media_folder_id
			WHERE q.media_folder_id = $1
		)
		UPDATE series_root_match_queue
		SET state = 'pending',
			available_at = CASE WHEN series_root_match_queue.lease_token = '' THEN NOW() ELSE series_root_match_queue.available_at END,
			deterministic_attempt_count = 0,
			failure_kind = '', failure_detail = '{}'::jsonb, last_error = '', parked_at = NULL,
			rerun_requested = true,
			input_fingerprint = current_inputs.input_fingerprint, matcher_revision = `+fmt.Sprintf("%d", seriesMatcherRevision)+`, updated_at = NOW()
		FROM current_inputs
		WHERE series_root_match_queue.media_folder_id = current_inputs.media_folder_id
		  AND series_root_match_queue.observed_root_path = current_inputs.observed_root_path
	`, folderID)
	if err != nil {
		return 0, fmt.Errorf("retrying series root queue now: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *SeriesRootMatchQueueRepository) WakeForChangedInputs(ctx context.Context) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	tag, err := r.pool.Exec(ctx, `
		WITH changed AS (
			SELECT q.media_folder_id, q.observed_root_path,
				`+seriesMatchQueueInputFingerprintSQL("q.observed_root_path", "q.media_folder_id", "folders.metadata_language")+` AS input_fingerprint
			FROM series_root_match_queue q
			JOIN media_folders folders ON folders.id = q.media_folder_id
		)
		UPDATE series_root_match_queue q
		SET state = 'pending',
			available_at = CASE WHEN q.lease_token = '' THEN NOW() ELSE q.available_at END,
			deterministic_attempt_count = 0,
			failure_kind = '', failure_detail = '{}'::jsonb, last_error = '', parked_at = NULL,
			rerun_requested = true,
			input_fingerprint = changed.input_fingerprint, matcher_revision = `+fmt.Sprintf("%d", seriesMatcherRevision)+`, updated_at = NOW()
		FROM changed
		WHERE changed.media_folder_id = q.media_folder_id
		  AND changed.observed_root_path = q.observed_root_path
		  AND (q.input_fingerprint <> changed.input_fingerprint OR q.matcher_revision <> `+fmt.Sprintf("%d", seriesMatcherRevision)+`)
	`)
	if err != nil {
		return 0, fmt.Errorf("waking series matches with changed inputs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ReleaseLease makes unfinished rows from a stopped batch immediately
// claimable without affecting rows whose ownership was revoked or replaced.
func (r *SeriesRootMatchQueueRepository) ReleaseLease(ctx context.Context, leaseToken string) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(leaseToken) == "" {
		return 0, errors.New("lease token is required")
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE series_root_match_queue
		SET available_at = NOW(), lease_token = '',
			rerun_requested = rerun_requested OR lease_forced_rerun,
			lease_forced_rerun = false,
			attempt_count = GREATEST(attempt_count - 1, 0), updated_at = NOW()
		WHERE lease_token = $1 AND state = 'pending'
	`, leaseToken)
	if err != nil {
		return 0, fmt.Errorf("releasing series root match lease: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *SeriesRootMatchQueueRepository) releaseClaimAfterError(ctx context.Context, leaseToken string, claimErr error) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, releaseErr := r.ReleaseLease(releaseCtx, leaseToken); releaseErr != nil {
		return errors.Join(claimErr, releaseErr)
	}
	return claimErr
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
		SELECT media_folder_id, observed_root_path, first_queued_at, available_at, last_attempted_at, attempt_count, last_error,
			state, failure_kind, failure_detail, deterministic_attempt_count, input_fingerprint, matcher_revision, parked_at, updated_at
		FROM series_root_match_queue
		WHERE media_folder_id = $1
		ORDER BY (state = 'parked') DESC, available_at ASC, last_attempted_at ASC NULLS FIRST, observed_root_path ASC
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
			&entry.State,
			&entry.FailureKind,
			&entry.FailureDetail,
			&entry.DeterministicAttemptCount,
			&entry.InputFingerprint,
			&entry.MatcherRevision,
			&entry.ParkedAt,
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

func (r *SeriesRootMatchQueueRepository) CountStatesByFolder(ctx context.Context, folderID int) (int, int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, 0, err
	}
	if err := requirePositiveSeriesQueueID("folder id", folderID); err != nil {
		return 0, 0, err
	}
	var pending, parked int
	if err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE state = 'pending'),
			COUNT(*) FILTER (WHERE state = 'parked')
		FROM series_root_match_queue
		WHERE media_folder_id = $1
	`, folderID).Scan(&pending, &parked); err != nil {
		return 0, 0, fmt.Errorf("counting series queue states: %w", err)
	}
	return pending, parked, nil
}

// CountStatesByFolders returns queue aggregates for every requested library in
// one query. Libraries without rows are omitted from the result map.
func (r *SeriesRootMatchQueueRepository) CountStatesByFolders(ctx context.Context, folderIDs []int) (map[int]MatchQueueStateCounts, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, err
	}
	counts := make(map[int]MatchQueueStateCounts, len(folderIDs))
	if len(folderIDs) == 0 {
		return counts, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT
			media_folder_id,
			COUNT(*) FILTER (WHERE state = 'pending'),
			COUNT(*) FILTER (WHERE state = 'parked')
		FROM series_root_match_queue
		WHERE media_folder_id = ANY($1)
		GROUP BY media_folder_id
	`, folderIDs)
	if err != nil {
		return nil, fmt.Errorf("counting series queue states by folders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var folderID int
		var count MatchQueueStateCounts
		if err := rows.Scan(&folderID, &count.Pending, &count.Parked); err != nil {
			return nil, fmt.Errorf("scanning series queue state counts: %w", err)
		}
		counts[folderID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating series queue state counts: %w", err)
	}
	return counts, nil
}

func (r *SeriesRootMatchQueueRepository) CountByFolderAndState(ctx context.Context, folderID int, state string) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM series_root_match_queue WHERE media_folder_id = $1 AND state = $2`, folderID, state).Scan(&total); err != nil {
		return 0, fmt.Errorf("counting series root queue state: %w", err)
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
			&job.RerunRequested,
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
