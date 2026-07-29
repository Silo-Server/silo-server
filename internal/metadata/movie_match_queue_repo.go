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
	scannerrepo "github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
const movieQueueFileBaseEligibleCond = `folders.enabled = true
	  AND (
		lower(trim(folders.type)) IN ('movie', 'movies') OR
		(lower(trim(folders.type)) = 'mixed' AND lower(trim(mf.base_type)) = 'movie')
	  )
	  AND mf.missing_since IS NULL AND mf.extra_id IS NULL
	  AND NOT EXISTS (
		SELECT 1
		FROM skipped_media_roots sr
		WHERE sr.media_folder_id = mf.media_folder_id
		  AND sr.reason = '` + skippedReasonSeriesInMovieLibrary + `'
		  AND strpos(mf.file_path, sr.root_path || '/') = 1
	  )`

const movieQueueFileNeedsMatchCond = `(
		mf.content_id IS NULL OR mf.content_id = '' OR
		lower(trim(COALESCE(mi.status, ''))) IN ('pending', 'unmatched', 'ambiguous')
	  )`

const movieQueueFileEligibleCond = movieQueueFileBaseEligibleCond + `
	  AND ` + movieQueueFileNeedsMatchCond

const movieQueueClaimEligibleCond = movieQueueFileBaseEligibleCond + `
	  AND (q.rerun_requested OR q.lease_forced_rerun OR ` + movieQueueFileNeedsMatchCond + `)`

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
			input_fingerprint,
			matcher_revision,
			available_at,
			updated_at
		)
		SELECT
			mf.id,
			mf.media_folder_id,
			`+matchQueueInputFingerprintSQL("mf.file_path", "'movie'", "mf.media_folder_id", "folders.metadata_language", movieMatcherRevision)+`,
			`+fmt.Sprintf("%d", movieMatcherRevision)+`,
			NOW(),
			NOW()
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.id = $1
		  AND `+movieQueueFileEligibleCond+`
		ON CONFLICT (media_file_id) DO UPDATE
		SET media_folder_id = EXCLUDED.media_folder_id,
			available_at = CASE
				WHEN (movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision) AND movie_match_queue.lease_token = '' THEN LEAST(movie_match_queue.available_at, EXCLUDED.available_at)
				WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN movie_match_queue.available_at
				ELSE GREATEST(movie_match_queue.available_at, EXCLUDED.available_at)
			END,
			state = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 'pending' ELSE movie_match_queue.state END,
			deterministic_attempt_count = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 0 ELSE movie_match_queue.deterministic_attempt_count END,
			failure_kind = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE movie_match_queue.failure_kind END,
			failure_detail = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '{}'::jsonb ELSE movie_match_queue.failure_detail END,
			last_error = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE movie_match_queue.last_error END,
			parked_at = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN NULL ELSE movie_match_queue.parked_at END,
			rerun_requested = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN true ELSE movie_match_queue.rerun_requested END,
			input_fingerprint = EXCLUDED.input_fingerprint,
			matcher_revision = EXCLUDED.matcher_revision,
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
			  AND `+movieQueueClaimEligibleCond+`
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
			input_fingerprint,
			matcher_revision,
			available_at,
			updated_at
		)
		SELECT
			mf.id,
			mf.media_folder_id,
			`+matchQueueInputFingerprintSQL("mf.file_path", "'movie'", "mf.media_folder_id", "folders.metadata_language", movieMatcherRevision)+`,
			`+fmt.Sprintf("%d", movieMatcherRevision)+`,
			NOW(),
			NOW()
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND `+movieQueueFileEligibleCond+`
		ON CONFLICT (media_file_id) DO UPDATE
		SET media_folder_id = EXCLUDED.media_folder_id,
			available_at = CASE
				WHEN (movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision) AND movie_match_queue.lease_token = '' THEN LEAST(movie_match_queue.available_at, EXCLUDED.available_at)
				WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN movie_match_queue.available_at
				ELSE GREATEST(movie_match_queue.available_at, EXCLUDED.available_at)
			END,
			state = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 'pending' ELSE movie_match_queue.state END,
			deterministic_attempt_count = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 0 ELSE movie_match_queue.deterministic_attempt_count END,
			failure_kind = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE movie_match_queue.failure_kind END,
			failure_detail = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '{}'::jsonb ELSE movie_match_queue.failure_detail END,
			last_error = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE movie_match_queue.last_error END,
			parked_at = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN NULL ELSE movie_match_queue.parked_at END,
			rerun_requested = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN true ELSE movie_match_queue.rerun_requested END,
			input_fingerprint = EXCLUDED.input_fingerprint,
			matcher_revision = EXCLUDED.matcher_revision,
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
			  AND `+movieQueueClaimEligibleCond+`
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
			input_fingerprint,
			matcher_revision,
			available_at,
			updated_at
		)
		SELECT
			mf.id,
			mf.media_folder_id,
			`+matchQueueInputFingerprintSQL("mf.file_path", "'movie'", "mf.media_folder_id", "folders.metadata_language", movieMatcherRevision)+`,
			`+fmt.Sprintf("%d", movieMatcherRevision)+`,
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
			available_at = CASE
				WHEN (movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision) AND movie_match_queue.lease_token = '' THEN LEAST(movie_match_queue.available_at, EXCLUDED.available_at)
				WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN movie_match_queue.available_at
				ELSE GREATEST(movie_match_queue.available_at, EXCLUDED.available_at)
			END,
			state = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 'pending' ELSE movie_match_queue.state END,
			deterministic_attempt_count = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN 0 ELSE movie_match_queue.deterministic_attempt_count END,
			failure_kind = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE movie_match_queue.failure_kind END,
			failure_detail = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '{}'::jsonb ELSE movie_match_queue.failure_detail END,
			last_error = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN '' ELSE movie_match_queue.last_error END,
			parked_at = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN NULL ELSE movie_match_queue.parked_at END,
			rerun_requested = CASE WHEN movie_match_queue.input_fingerprint <> EXCLUDED.input_fingerprint OR movie_match_queue.matcher_revision <> EXCLUDED.matcher_revision THEN true ELSE movie_match_queue.rerun_requested END,
			input_fingerprint = EXCLUDED.input_fingerprint,
			matcher_revision = EXCLUDED.matcher_revision,
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
			  AND `+movieQueueClaimEligibleCond+`
		  )
	`, folderID, scopePath, scopeLike); err != nil {
		return fmt.Errorf("deleting stale movie queue rows in scope: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit movie queue scoped sync transaction: %w", err)
	}
	return nil
}

func (r *MovieMatchQueueRepository) Claim(ctx context.Context, limit int) ([]models.MovieMatchJob, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 500
	}
	leaseToken := uuid.NewString()

	rows, err := r.pool.Query(ctx, `
		WITH candidates AS (
			SELECT q.media_file_id, q.available_at, q.last_attempted_at,
				(q.rerun_requested OR q.lease_forced_rerun) AS rerun_requested
			FROM movie_match_queue q
			JOIN media_files mf ON mf.id = q.media_file_id
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE q.state = 'pending'
			  AND q.available_at <= NOW()
			  AND `+movieQueueClaimEligibleCond+`
			ORDER BY q.available_at ASC, q.last_attempted_at ASC NULLS FIRST, q.media_file_id ASC
			LIMIT $1
			FOR UPDATE OF q SKIP LOCKED
		),
		updated AS (
			UPDATE movie_match_queue q
			SET last_attempted_at = NOW(),
				attempt_count = q.attempt_count + 1,
				available_at = NOW() + $2::interval,
				lease_token = $3,
				lease_forced_rerun = c.rerun_requested,
				rerun_requested = false,
				updated_at = NOW()
			FROM candidates c
			WHERE q.media_file_id = c.media_file_id
			RETURNING q.media_file_id
		)
		SELECT c.media_file_id, c.rerun_requested
		FROM candidates c
		JOIN updated u ON u.media_file_id = c.media_file_id
		ORDER BY c.available_at ASC, c.last_attempted_at ASC NULLS FIRST, c.media_file_id ASC
	`, limit, intervalLiteral(matchQueueClaimLease), leaseToken)
	if err != nil {
		return nil, fmt.Errorf("claiming movie queue rows: %w", err)
	}
	defer rows.Close()

	claimed, err := scanClaimedMovies(rows)
	if err != nil {
		rows.Close()
		return nil, r.releaseClaimAfterError(ctx, leaseToken, err)
	}
	ids := claimedMovieIDs(claimed)
	files, err := r.loadFilesByIDs(ctx, ids, leaseToken)
	if err != nil {
		return nil, r.releaseClaimAfterError(ctx, leaseToken, err)
	}
	return movieMatchJobs(files, leaseToken, claimed), nil
}

func (r *MovieMatchQueueRepository) ClaimByFolderAndPathPrefix(
	ctx context.Context,
	folderID int,
	pathPrefix string,
	limit int,
	attemptBefore time.Time,
) ([]models.MovieMatchJob, error) {
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
	leaseToken := uuid.NewString()
	pathPrefix = filepath.Clean(pathPrefix)
	scopeLike := pathPrefixLike(pathPrefix)

	rows, err := r.pool.Query(ctx, `
		WITH candidates AS (
			SELECT q.media_file_id, q.available_at, q.last_attempted_at,
				(q.rerun_requested OR q.lease_forced_rerun) AS rerun_requested
			FROM movie_match_queue q
			JOIN media_files mf ON mf.id = q.media_file_id
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			LEFT JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE q.media_folder_id = $1
			  AND q.state = 'pending'
			  AND q.available_at <= NOW()
			  AND `+movieQueueClaimEligibleCond+`
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
				available_at = NOW() + $6::interval,
				lease_token = $7,
				lease_forced_rerun = c.rerun_requested,
				rerun_requested = false,
				updated_at = NOW()
			FROM candidates c
			WHERE q.media_file_id = c.media_file_id
			RETURNING q.media_file_id
		)
		SELECT c.media_file_id, c.rerun_requested
		FROM candidates c
		JOIN updated u ON u.media_file_id = c.media_file_id
		ORDER BY c.available_at ASC, c.last_attempted_at ASC NULLS FIRST, c.media_file_id ASC
	`, folderID, pathPrefix, scopeLike, nullTime(attemptBefore), limit, intervalLiteral(matchQueueClaimLease), leaseToken)
	if err != nil {
		return nil, fmt.Errorf("claiming movie queue rows by scope: %w", err)
	}
	defer rows.Close()

	claimed, err := scanClaimedMovies(rows)
	if err != nil {
		rows.Close()
		return nil, r.releaseClaimAfterError(ctx, leaseToken, err)
	}
	ids := claimedMovieIDs(claimed)
	files, err := r.loadFilesByIDs(ctx, ids, leaseToken)
	if err != nil {
		return nil, r.releaseClaimAfterError(ctx, leaseToken, err)
	}
	return movieMatchJobs(files, leaseToken, claimed), nil
}

func (r *MovieMatchQueueRepository) Delete(ctx context.Context, mediaFileID int, leaseToken string) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveMovieQueueID("media file id", mediaFileID); err != nil {
		return err
	}
	if strings.TrimSpace(leaseToken) == "" {
		return errors.New("lease token is required")
	}
	if _, err := r.pool.Exec(ctx, `
		WITH rerun AS (
			UPDATE movie_match_queue
			SET available_at = NOW(),
				lease_token = '',
				lease_forced_rerun = false,
				updated_at = NOW()
			WHERE media_file_id = $1
			  AND lease_token = $2
			  AND rerun_requested
			RETURNING media_file_id
		)
		DELETE FROM movie_match_queue
		WHERE media_file_id = $1
		  AND lease_token = $2
		  AND NOT rerun_requested
	`, mediaFileID, leaseToken); err != nil {
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

func (r *MovieMatchQueueRepository) UpdateError(ctx context.Context, mediaFileID int, leaseToken, errText string) error {
	// Generic processing failures (database errors, interrupted work, provider
	// transport errors) are operationally retryable. Deterministic matcher
	// outcomes use UpdateFailure with an explicit kind and are the only failures
	// allowed to consume the parking budget.
	return r.UpdateFailure(ctx, mediaFileID, leaseToken, MatchFailure{Kind: MatchOutcomeProviderTransient, Message: errText})
}

func (r *MovieMatchQueueRepository) UpdateFailure(ctx context.Context, mediaFileID int, leaseToken string, failure MatchFailure) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	if err := requirePositiveMovieQueueID("media file id", mediaFileID); err != nil {
		return err
	}
	if strings.TrimSpace(leaseToken) == "" {
		return errors.New("lease token is required")
	}
	kind := normalizeMatchFailureKind(failure.Kind)
	message := boundedMatchFailureMessage(failure.Message)
	detail, err := json.Marshal(map[string]any{"message": message, "decision": boundedMatchDecision(failure.Decision)})
	if err != nil {
		return fmt.Errorf("encoding movie queue failure: %w", err)
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE movie_match_queue
		SET last_error = CASE WHEN rerun_requested THEN last_error ELSE left($2, 2000) END,
			failure_kind = CASE WHEN rerun_requested THEN failure_kind ELSE $3 END,
			failure_detail = CASE WHEN rerun_requested THEN failure_detail ELSE $4::jsonb END,
			deterministic_attempt_count = CASE
				WHEN rerun_requested THEN deterministic_attempt_count
				ELSE deterministic_attempt_count + CASE WHEN $3 = 'provider_transient' THEN 0 ELSE 1 END
			END,
			state = CASE
				WHEN rerun_requested THEN 'pending'
				WHEN $3 <> 'provider_transient' AND deterministic_attempt_count + 1 >= 3 THEN 'parked'
				ELSE 'pending'
			END,
			parked_at = CASE
				WHEN rerun_requested THEN NULL
				WHEN $3 <> 'provider_transient' AND deterministic_attempt_count + 1 >= 3 THEN NOW()
				ELSE NULL
			END,
			available_at = CASE
				WHEN rerun_requested THEN NOW()
				WHEN $3 = 'provider_transient' THEN `+matchQueueBackoffExpr("$5", "$6")+`
				WHEN deterministic_attempt_count + 1 = 1 THEN NOW() + interval '1 hour'
				ELSE NOW() + interval '24 hours'
			END,
			lease_token = '',
			rerun_requested = rerun_requested OR lease_forced_rerun,
			lease_forced_rerun = false,
			updated_at = NOW()
		WHERE media_file_id = $1 AND lease_token = $7
	`, mediaFileID, message, kind, detail, intervalLiteral(movieQueueRetryDelay), intervalLiteral(matchQueueRetryMaxDelay), leaseToken); err != nil {
		return fmt.Errorf("updating movie queue error: %w", err)
	}
	return nil
}

// RetryNowByFolder explicitly wakes every queued row, including parked and
// future-backed-off work. Historical attempt_count remains available for audit.
func (r *MovieMatchQueueRepository) RetryNowByFolder(ctx context.Context, folderID int) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	if err := requirePositiveMovieQueueID("folder id", folderID); err != nil {
		return 0, err
	}
	tag, err := r.pool.Exec(ctx, `
		WITH current_inputs AS (
			SELECT q.media_file_id, mf.media_folder_id,
				`+matchQueueInputFingerprintSQL("mf.file_path", "'movie'", "mf.media_folder_id", "folders.metadata_language", movieMatcherRevision)+` AS input_fingerprint
			FROM movie_match_queue q
			JOIN media_files mf ON mf.id = q.media_file_id
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			WHERE q.media_folder_id = $1
		)
		UPDATE movie_match_queue
		SET state = 'pending',
			available_at = CASE WHEN movie_match_queue.lease_token = '' THEN NOW() ELSE movie_match_queue.available_at END,
			deterministic_attempt_count = 0,
			failure_kind = '', failure_detail = '{}'::jsonb, last_error = '', parked_at = NULL,
			rerun_requested = true,
			media_folder_id = current_inputs.media_folder_id,
			input_fingerprint = current_inputs.input_fingerprint, matcher_revision = `+fmt.Sprintf("%d", movieMatcherRevision)+`, updated_at = NOW()
		FROM current_inputs
		WHERE movie_match_queue.media_file_id = current_inputs.media_file_id
	`, folderID)
	if err != nil {
		return 0, fmt.Errorf("retrying movie queue now: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *MovieMatchQueueRepository) WakeForChangedInputs(ctx context.Context) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	tag, err := r.pool.Exec(ctx, `
		WITH changed AS (
			SELECT q.media_file_id, mf.media_folder_id,
				`+matchQueueInputFingerprintSQL("mf.file_path", "'movie'", "mf.media_folder_id", "folders.metadata_language", movieMatcherRevision)+` AS input_fingerprint
			FROM movie_match_queue q
			JOIN media_files mf ON mf.id = q.media_file_id
			JOIN media_folders folders ON folders.id = mf.media_folder_id
		)
		UPDATE movie_match_queue q
		SET state = 'pending',
			available_at = CASE WHEN q.lease_token = '' THEN NOW() ELSE q.available_at END,
			deterministic_attempt_count = 0,
			failure_kind = '', failure_detail = '{}'::jsonb, last_error = '', parked_at = NULL,
			rerun_requested = true,
			media_folder_id = changed.media_folder_id,
			input_fingerprint = changed.input_fingerprint, matcher_revision = `+fmt.Sprintf("%d", movieMatcherRevision)+`, updated_at = NOW()
		FROM changed
		WHERE changed.media_file_id = q.media_file_id
		  AND (q.input_fingerprint <> changed.input_fingerprint OR q.matcher_revision <> `+fmt.Sprintf("%d", movieMatcherRevision)+` OR q.media_folder_id <> changed.media_folder_id)
	`)
	if err != nil {
		return 0, fmt.Errorf("waking movie matches with changed inputs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ReleaseLease makes any unfinished rows from one batch immediately
// claimable. Rows completed, failed, expired, or explicitly retried no longer
// carry this token and are therefore untouched.
func (r *MovieMatchQueueRepository) ReleaseLease(ctx context.Context, leaseToken string) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(leaseToken) == "" {
		return 0, errors.New("lease token is required")
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE movie_match_queue
		SET available_at = NOW(), lease_token = '',
			rerun_requested = rerun_requested OR lease_forced_rerun,
			lease_forced_rerun = false,
			attempt_count = GREATEST(attempt_count - 1, 0), updated_at = NOW()
		WHERE lease_token = $1 AND state = 'pending'
	`, leaseToken)
	if err != nil {
		return 0, fmt.Errorf("releasing movie match lease: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *MovieMatchQueueRepository) releaseClaimAfterError(ctx context.Context, leaseToken string, claimErr error) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, releaseErr := r.ReleaseLease(releaseCtx, leaseToken); releaseErr != nil {
		return errors.Join(claimErr, releaseErr)
	}
	return claimErr
}

type claimedMovie struct {
	mediaFileID    int
	rerunRequested bool
}

func claimedMovieIDs(claimed []claimedMovie) []int {
	ids := make([]int, 0, len(claimed))
	for _, row := range claimed {
		ids = append(ids, row.mediaFileID)
	}
	return ids
}

func movieMatchJobs(files []*models.MediaFile, leaseToken string, claimed []claimedMovie) []models.MovieMatchJob {
	rerunByID := make(map[int]bool, len(claimed))
	for _, row := range claimed {
		rerunByID[row.mediaFileID] = row.rerunRequested
	}
	jobs := make([]models.MovieMatchJob, 0, len(files))
	for _, file := range files {
		if file != nil {
			jobs = append(jobs, models.MovieMatchJob{
				File:           file,
				LeaseToken:     leaseToken,
				RerunRequested: rerunByID[file.ID],
			})
		}
	}
	return jobs
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
			q.state,
			q.failure_kind,
			q.failure_detail,
			q.deterministic_attempt_count,
			q.input_fingerprint,
			q.matcher_revision,
			q.parked_at,
			q.updated_at
		FROM movie_match_queue q
		LEFT JOIN media_files mf ON mf.id = q.media_file_id
		WHERE q.media_folder_id = $1
		ORDER BY (q.state = 'parked') DESC, q.available_at ASC, q.last_attempted_at ASC NULLS FIRST, q.media_file_id ASC
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
			&entry.State,
			&entry.FailureKind,
			&entry.FailureDetail,
			&entry.DeterministicAttemptCount,
			&entry.InputFingerprint,
			&entry.MatcherRevision,
			&entry.ParkedAt,
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

func (r *MovieMatchQueueRepository) CountStatesByFolder(ctx context.Context, folderID int) (int, int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, 0, err
	}
	if err := requirePositiveMovieQueueID("folder id", folderID); err != nil {
		return 0, 0, err
	}
	var pending, parked int
	if err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE state = 'pending'),
			COUNT(*) FILTER (WHERE state = 'parked')
		FROM movie_match_queue
		WHERE media_folder_id = $1
	`, folderID).Scan(&pending, &parked); err != nil {
		return 0, 0, fmt.Errorf("counting movie queue states: %w", err)
	}
	return pending, parked, nil
}

// CountStatesByFolders returns queue aggregates for every requested library in
// one query. Libraries without rows are omitted from the result map.
func (r *MovieMatchQueueRepository) CountStatesByFolders(ctx context.Context, folderIDs []int) (map[int]MatchQueueStateCounts, error) {
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
		FROM movie_match_queue
		WHERE media_folder_id = ANY($1)
		GROUP BY media_folder_id
	`, folderIDs)
	if err != nil {
		return nil, fmt.Errorf("counting movie queue states by folders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var folderID int
		var count MatchQueueStateCounts
		if err := rows.Scan(&folderID, &count.Pending, &count.Parked); err != nil {
			return nil, fmt.Errorf("scanning movie queue state counts: %w", err)
		}
		counts[folderID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating movie queue state counts: %w", err)
	}
	return counts, nil
}

func (r *MovieMatchQueueRepository) CountByFolderAndState(ctx context.Context, folderID int, state string) (int, error) {
	if err := r.requireConfigured(); err != nil {
		return 0, err
	}
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM movie_match_queue WHERE media_folder_id = $1 AND state = $2`, folderID, state).Scan(&total); err != nil {
		return 0, fmt.Errorf("counting movie queue state: %w", err)
	}
	return total, nil
}

func (r *MovieMatchQueueRepository) loadFilesByIDs(ctx context.Context, ids []int, leaseToken string) ([]*models.MediaFile, error) {
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
			if err := r.Delete(ctx, id, leaseToken); err != nil {
				return nil, fmt.Errorf("deleting stale movie queue row %d: %w", id, err)
			}
			continue
		}
		files = append(files, file)
	}
	return files, nil
}

func scanClaimedMovies(rows pgx.Rows) ([]claimedMovie, error) {
	claimed := make([]claimedMovie, 0)
	for rows.Next() {
		var row claimedMovie
		if err := rows.Scan(&row.mediaFileID, &row.rerunRequested); err != nil {
			return nil, fmt.Errorf("scanning claimed movie queue row: %w", err)
		}
		claimed = append(claimed, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating claimed movie queue rows: %w", err)
	}
	return claimed, nil
}

func intervalLiteral(d time.Duration) string {
	return fmt.Sprintf("%.0f seconds", d.Seconds())
}
