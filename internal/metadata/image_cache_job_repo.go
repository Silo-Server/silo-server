package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	ImageCacheTargetItem               = "item"
	ImageCacheTargetItemLocalization   = "item_localization"
	ImageCacheTargetSeason             = "season"
	ImageCacheTargetSeasonLocalization = "season_localization"
	ImageCacheTargetEpisode            = "episode"
	ImageCacheTargetPerson             = "person"

	ImageCacheImagePoster   = "poster"
	ImageCacheImageBackdrop = "backdrop"
	ImageCacheImageLogo     = "logo"
	ImageCacheImageStill    = "still"
	ImageCacheImageProfile  = "profile"

	ImageCacheStatusQueued    = "queued"
	ImageCacheStatusRunning   = "running"
	ImageCacheStatusSucceeded = "succeeded"
	ImageCacheStatusFailed    = "failed"

	imageCacheLeaseDuration = 15 * time.Minute
	imageCacheMaxAttempts   = 8
	imageCacheDeferredRetry = 7 * 24 * time.Hour
)

type EnqueueImageCacheJobInput struct {
	TargetType        string
	TargetContentID   string
	TargetLanguage    string
	SeriesID          string
	SourcePath        string
	ProviderID        string
	ProviderContentID string
	ContentType       string
	ImageType         string
	SeasonNumber      *int
	EpisodeNumber     *int
}

type ImageCacheJobRepository struct {
	pool *pgxpool.Pool
}

func NewImageCacheJobRepository(pool *pgxpool.Pool) *ImageCacheJobRepository {
	return &ImageCacheJobRepository{pool: pool}
}

func imageCacheRetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return time.Minute
	}
	delay := time.Minute << min(attempt-1, 7)
	if delay > 2*time.Hour {
		return 2 * time.Hour
	}
	return delay
}

func imageCacheFailureRetryDelay(attempt int, errText string) time.Duration {
	if isStableProviderImageFailure(errText) {
		return imageCacheDeferredRetry
	}
	return imageCacheRetryDelay(attempt)
}

func isStableProviderImageFailure(errText string) bool {
	return strings.Contains(errText, "unexpected status 403") ||
		strings.Contains(errText, "unexpected status 404") ||
		strings.Contains(errText, "unexpected status 410") ||
		strings.Contains(errText, "unexpected status 418") ||
		// Local sidecar failures that will not heal on the normal backoff:
		// deleted files (ENOENT), unreadable files (EPERM), paths outside the
		// owning library's roots, and files that are structurally unusable
		// (non-regular or over the size cap) — these do not self-heal without an
		// on-disk change, so a hot retry loop is pointless; recovery is
		// refresh-driven. Other local read errors keep the standard retry
		// schedule. Texts match the processor's local branch.
		strings.Contains(errText, "local image missing") ||
		strings.Contains(errText, "local image forbidden") ||
		strings.Contains(errText, "local image path outside library roots") ||
		strings.Contains(errText, "local image is not a regular file") ||
		strings.Contains(errText, "local image exceeds")
}

func (r *ImageCacheJobRepository) Enqueue(ctx context.Context, in EnqueueImageCacheJobInput) error {
	_, err := r.EnqueueBatch(ctx, []EnqueueImageCacheJobInput{in})
	return err
}

func (r *ImageCacheJobRepository) EnqueueBatch(ctx context.Context, inputs []EnqueueImageCacheJobInput) (int, error) {
	return r.enqueueBatch(ctx, inputs, false)
}

func (r *ImageCacheJobRepository) enqueueBatch(ctx context.Context, inputs []EnqueueImageCacheJobInput, requeueSucceeded bool) (int, error) {
	if r == nil || r.pool == nil {
		return 0, nil
	}
	valid := make([]EnqueueImageCacheJobInput, 0, len(inputs))
	for _, in := range inputs {
		normalized, ok := normalizeImageCacheJobInput(in)
		if !ok {
			continue
		}
		valid = append(valid, normalized)
	}
	if len(valid) == 0 {
		return 0, nil
	}

	total := 0
	for start := 0; start < len(valid); start += 250 {
		end := start + 250
		if end > len(valid) {
			end = len(valid)
		}
		affected, err := r.enqueueBatchChunk(ctx, valid[start:end], requeueSucceeded)
		if err != nil {
			return total, err
		}
		total += affected
	}
	return total, nil
}

func normalizeImageCacheJobInput(in EnqueueImageCacheJobInput) (EnqueueImageCacheJobInput, bool) {
	in.SourcePath = strings.TrimSpace(in.SourcePath)
	in.TargetLanguage = strings.TrimSpace(in.TargetLanguage)
	if !isCacheableImageSourcePath(in.SourcePath) {
		return EnqueueImageCacheJobInput{}, false
	}
	if in.ContentType == "" {
		in.ContentType = "series"
	}
	if in.ProviderID == "" {
		in.ProviderID = imageCacheProviderIDFromSource(in.SourcePath, "")
	}
	if in.ProviderContentID == "" {
		in.ProviderContentID = firstNonEmpty(in.SeriesID, in.TargetContentID)
	}
	return in, true
}

func (r *ImageCacheJobRepository) enqueueBatchChunk(ctx context.Context, inputs []EnqueueImageCacheJobInput, requeueSucceeded bool) (int, error) {
	var sql strings.Builder
	args := make([]any, 0, len(inputs)*11+1)
	sql.WriteString(`
		INSERT INTO metadata_image_cache_jobs (
			target_type, target_content_id, target_language, series_id, source_path,
			provider_id, provider_content_id, content_type, image_type,
			season_number, episode_number, status, attempt_count,
			next_attempt_at, locked_at, locked_by, last_error,
			created_at, updated_at, completed_at
		) VALUES `)
	for i, in := range inputs {
		if i > 0 {
			sql.WriteString(", ")
		}
		base := len(args)
		fmt.Fprintf(&sql, `($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, 'queued', 0, NOW(), NULL, '', '', NOW(), NOW(), NULL)`,
			base+1, base+2, base+3, base+4, base+5,
			base+6, base+7, base+8, base+9, base+10, base+11)
		args = append(args,
			in.TargetType, in.TargetContentID, strings.TrimSpace(in.TargetLanguage), in.SeriesID, in.SourcePath,
			in.ProviderID, in.ProviderContentID, in.ContentType, in.ImageType,
			in.SeasonNumber, in.EpisodeNumber,
		)
	}
	requeueSucceededArg := len(args) + 1
	args = append(args, requeueSucceeded)
	fmt.Fprintf(&sql, `
	ON CONFLICT (target_type, target_content_id, image_type, target_language) DO UPDATE SET
		series_id = EXCLUDED.series_id,
			source_path = EXCLUDED.source_path,
			provider_id = EXCLUDED.provider_id,
			provider_content_id = EXCLUDED.provider_content_id,
			content_type = EXCLUDED.content_type,
			season_number = EXCLUDED.season_number,
			episode_number = EXCLUDED.episode_number,
			status = CASE
				WHEN metadata_image_cache_jobs.source_path IS DISTINCT FROM EXCLUDED.source_path
					THEN 'queued'
				WHEN metadata_image_cache_jobs.status = 'failed'
					AND metadata_image_cache_jobs.next_attempt_at <= NOW()
					THEN 'queued'
				WHEN $%d::boolean
					AND metadata_image_cache_jobs.status = 'succeeded'
					THEN 'queued'
				WHEN metadata_image_cache_jobs.status = 'succeeded'
					THEN 'succeeded'
				ELSE metadata_image_cache_jobs.status
			END,
			attempt_count = CASE
				WHEN metadata_image_cache_jobs.source_path IS DISTINCT FROM EXCLUDED.source_path
					THEN 0
				WHEN metadata_image_cache_jobs.status = 'failed'
					AND metadata_image_cache_jobs.next_attempt_at <= NOW()
					THEN 0
				WHEN $%d::boolean
					AND metadata_image_cache_jobs.status = 'succeeded'
					THEN 0
				ELSE metadata_image_cache_jobs.attempt_count
			END,
			next_attempt_at = CASE
				WHEN metadata_image_cache_jobs.source_path IS DISTINCT FROM EXCLUDED.source_path
					THEN NOW()
				WHEN metadata_image_cache_jobs.status = 'failed'
					AND metadata_image_cache_jobs.next_attempt_at <= NOW()
					THEN NOW()
				WHEN $%d::boolean
					AND metadata_image_cache_jobs.status = 'succeeded'
					THEN NOW()
				ELSE metadata_image_cache_jobs.next_attempt_at
			END,
			locked_at = CASE
				WHEN metadata_image_cache_jobs.source_path IS DISTINCT FROM EXCLUDED.source_path
					THEN NULL
				WHEN metadata_image_cache_jobs.status = 'failed'
					AND metadata_image_cache_jobs.next_attempt_at <= NOW()
					THEN NULL
				WHEN $%d::boolean
					AND metadata_image_cache_jobs.status = 'succeeded'
					THEN NULL
				ELSE metadata_image_cache_jobs.locked_at
			END,
			locked_by = CASE
				WHEN metadata_image_cache_jobs.source_path IS DISTINCT FROM EXCLUDED.source_path
					THEN ''
				WHEN metadata_image_cache_jobs.status = 'failed'
					AND metadata_image_cache_jobs.next_attempt_at <= NOW()
					THEN ''
				WHEN $%d::boolean
					AND metadata_image_cache_jobs.status = 'succeeded'
					THEN ''
				ELSE metadata_image_cache_jobs.locked_by
			END,
			last_error = CASE
				WHEN metadata_image_cache_jobs.source_path IS DISTINCT FROM EXCLUDED.source_path
					THEN ''
				WHEN metadata_image_cache_jobs.status = 'failed'
					AND metadata_image_cache_jobs.next_attempt_at <= NOW()
					THEN ''
				WHEN $%d::boolean
					AND metadata_image_cache_jobs.status = 'succeeded'
					THEN ''
				ELSE metadata_image_cache_jobs.last_error
			END,
			completed_at = CASE
				WHEN metadata_image_cache_jobs.source_path IS DISTINCT FROM EXCLUDED.source_path
					THEN NULL
				WHEN metadata_image_cache_jobs.status = 'failed'
					AND metadata_image_cache_jobs.next_attempt_at <= NOW()
					THEN NULL
				WHEN $%d::boolean
					AND metadata_image_cache_jobs.status = 'succeeded'
					THEN NULL
				ELSE metadata_image_cache_jobs.completed_at
			END,
			updated_at = NOW()
		WHERE metadata_image_cache_jobs.source_path IS DISTINCT FROM EXCLUDED.source_path
		   OR metadata_image_cache_jobs.status IN ('queued', 'running')
		   OR (
			   metadata_image_cache_jobs.status = 'failed'
			   AND metadata_image_cache_jobs.next_attempt_at <= NOW()
		   )
		   OR (
			   $%d::boolean
			   AND metadata_image_cache_jobs.status = 'succeeded'
		   )`,
		requeueSucceededArg,
		requeueSucceededArg,
		requeueSucceededArg,
		requeueSucceededArg,
		requeueSucceededArg,
		requeueSucceededArg,
		requeueSucceededArg,
		requeueSucceededArg)

	tag, err := r.pool.Exec(ctx, sql.String(), args...)
	if err != nil {
		return 0, fmt.Errorf("enqueuing metadata image cache jobs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *ImageCacheJobRepository) recoverExpiredRunning(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE metadata_image_cache_jobs
		SET status = CASE
				WHEN attempt_count + 1 >= $2 THEN 'failed'
				ELSE 'queued'
			END,
			attempt_count = attempt_count + 1,
			next_attempt_at = CASE
				WHEN attempt_count + 1 >= $2 THEN next_attempt_at
				ELSE NOW()
			END,
			locked_at = NULL,
			locked_by = '',
			last_error = CASE
				WHEN attempt_count + 1 >= $2 THEN left('worker lease expired too many times', 2000)
				ELSE last_error
			END,
			updated_at = NOW()
		WHERE status = 'running'
		  AND locked_at < NOW() - $1::interval
	`, intervalLiteral(imageCacheLeaseDuration), imageCacheMaxAttempts)
	if err != nil {
		return fmt.Errorf("recovering expired metadata image cache jobs: %w", err)
	}
	return nil
}

func (r *ImageCacheJobRepository) ClaimDue(ctx context.Context, workerID string, limit int) ([]*models.MetadataImageCacheJob, error) {
	if r == nil || r.pool == nil || limit <= 0 {
		return nil, nil
	}
	if err := r.recoverExpiredRunning(ctx); err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `
		WITH due AS (
			SELECT id
			FROM metadata_image_cache_jobs
			WHERE status = 'queued'
			  AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at ASC, id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE metadata_image_cache_jobs j
		SET status = 'running',
			locked_at = NOW(),
			locked_by = $2,
			updated_at = NOW()
		FROM due
		WHERE j.id = due.id
		RETURNING
			j.id, j.target_type, j.target_content_id, j.target_language, j.series_id,
			j.source_path, j.provider_id, j.provider_content_id,
			j.content_type, j.image_type, j.season_number, j.episode_number,
			j.status, j.attempt_count, j.next_attempt_at, j.locked_at,
			j.locked_by, j.last_error, j.created_at, j.updated_at, j.completed_at
	`, limit, workerID)
	if err != nil {
		return nil, fmt.Errorf("claiming metadata image cache jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]*models.MetadataImageCacheJob, 0, limit)
	for rows.Next() {
		job := new(models.MetadataImageCacheJob)
		if err := rows.Scan(
			&job.ID, &job.TargetType, &job.TargetContentID, &job.TargetLanguage, &job.SeriesID,
			&job.SourcePath, &job.ProviderID, &job.ProviderContentID,
			&job.ContentType, &job.ImageType, &job.SeasonNumber, &job.EpisodeNumber,
			&job.Status, &job.AttemptCount, &job.NextAttemptAt, &job.LockedAt,
			&job.LockedBy, &job.LastError, &job.CreatedAt, &job.UpdatedAt, &job.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning metadata image cache job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating metadata image cache jobs: %w", err)
	}
	return jobs, nil
}

// MarkSucceeded finalizes a job only if the caller still owns the lease
// (status running, locked_by matches). EnqueueBatch can repurpose a running
// row with a new source and a cleared lease; the ownership guard stops a
// stale worker from marking that replacement job complete and dropping the
// new artwork.
func (r *ImageCacheJobRepository) MarkSucceeded(ctx context.Context, id int64, lockedBy string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE metadata_image_cache_jobs
		SET status = 'succeeded',
			completed_at = NOW(),
			locked_at = NULL,
			locked_by = '',
			last_error = '',
			updated_at = NOW()
		WHERE id = $1
		  AND status = 'running'
		  AND locked_by = $2
	`, id, lockedBy)
	if err != nil {
		return fmt.Errorf("marking metadata image cache job succeeded: %w", err)
	}
	return nil
}

// MarkFailed records a failed attempt with backoff, guarded by lease ownership
// for the same reason as MarkSucceeded.
func (r *ImageCacheJobRepository) MarkFailed(ctx context.Context, id int64, attemptCount int, lockedBy string, errText string) error {
	nextAttempt := attemptCount + 1
	status := ImageCacheStatusQueued
	if nextAttempt >= imageCacheMaxAttempts {
		status = ImageCacheStatusFailed
	}
	delay := imageCacheFailureRetryDelay(nextAttempt, errText)

	_, err := r.pool.Exec(ctx, `
		UPDATE metadata_image_cache_jobs
		SET status = $2,
			attempt_count = $3,
			next_attempt_at = NOW() + $4::interval,
			locked_at = NULL,
			locked_by = '',
			last_error = left($5, 2000),
			updated_at = NOW()
		WHERE id = $1
		  AND status = 'running'
		  AND locked_by = $6
	`, id, status, nextAttempt, intervalLiteral(delay), errText, lockedBy)
	if err != nil {
		return fmt.Errorf("marking metadata image cache job failed: %w", err)
	}
	return nil
}

// RequeueClaimed returns claimed-but-unprocessed jobs to the queue without
// burning a retry attempt. Used when a run is cancelled before its workers
// start, so the jobs do not sit locked until the lease expires.
func (r *ImageCacheJobRepository) RequeueClaimed(ctx context.Context, ids []int64, workerID string) error {
	if r == nil || r.pool == nil || len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE metadata_image_cache_jobs
		SET status = 'queued',
			next_attempt_at = NOW(),
			locked_at = NULL,
			locked_by = '',
			updated_at = NOW()
		WHERE id = ANY($1)
		  AND status = 'running'
		  AND locked_by = $2
	`, ids, workerID)
	if err != nil {
		return fmt.Errorf("requeueing claimed metadata image cache jobs: %w", err)
	}
	return nil
}

// CurrentTargetSourcePath reports the source path currently stored on the
// job's target row so the processor can confirm it still owns the artwork
// before uploading to the deterministic storage key. Returns ("", nil) when
// the row no longer exists or the target type is unknown.
func (r *ImageCacheJobRepository) CurrentTargetSourcePath(ctx context.Context, job *models.MetadataImageCacheJob) (string, error) {
	if r == nil || r.pool == nil || job == nil {
		return "", nil
	}
	query, args, ok := currentTargetSourceQuery(job)
	if !ok {
		return "", nil
	}
	var current string
	err := r.pool.QueryRow(ctx, query, args...).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading current target source path: %w", err)
	}
	return current, nil
}

// CurrentTargetCachedPath reports the cached image path currently stored on
// the job's target row. The processor's local-artwork branch uses it to skip
// unchanged art and to delete the stale hashed local/ prefix after a
// successful re-cache. Returns ("", nil) when the row no longer exists or the
// target type is unknown.
func (r *ImageCacheJobRepository) CurrentTargetCachedPath(ctx context.Context, job *models.MetadataImageCacheJob) (string, error) {
	if r == nil || r.pool == nil || job == nil {
		return "", nil
	}
	query, args, ok := currentTargetSourceQuery(job)
	if !ok {
		return "", nil
	}
	query = strings.Replace(query, "_source_path", "_path", 1)
	var current string
	err := r.pool.QueryRow(ctx, query, args...).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading current target cached path: %w", err)
	}
	return current, nil
}

func currentTargetSourceQuery(job *models.MetadataImageCacheJob) (string, []any, bool) {
	switch job.TargetType {
	case ImageCacheTargetItem:
		col, ok := itemArtworkSourceColumn(job.ImageType)
		if !ok {
			return "", nil, false
		}
		return fmt.Sprintf("SELECT %s FROM media_items WHERE content_id = $1", col),
			[]any{job.TargetContentID}, true
	case ImageCacheTargetItemLocalization:
		col, ok := itemArtworkSourceColumn(job.ImageType)
		if !ok {
			return "", nil, false
		}
		return fmt.Sprintf("SELECT %s FROM media_item_localizations WHERE content_id = $1 AND language = $2", col),
			[]any{job.TargetContentID, job.TargetLanguage}, true
	case ImageCacheTargetSeason:
		return "SELECT poster_source_path FROM seasons WHERE content_id = $1",
			[]any{job.TargetContentID}, true
	case ImageCacheTargetSeasonLocalization:
		return "SELECT poster_source_path FROM season_localizations WHERE season_content_id = $1 AND language = $2",
			[]any{job.TargetContentID, job.TargetLanguage}, true
	case ImageCacheTargetEpisode:
		return "SELECT still_source_path FROM episodes WHERE content_id = $1",
			[]any{job.TargetContentID}, true
	case ImageCacheTargetPerson:
		return "SELECT photo_source_path FROM people WHERE id = $1::bigint",
			[]any{job.TargetContentID}, true
	default:
		return "", nil, false
	}
}

func itemArtworkSourceColumn(imageType string) (string, bool) {
	switch imageType {
	case ImageCacheImagePoster:
		return "poster_source_path", true
	case ImageCacheImageBackdrop:
		return "backdrop_source_path", true
	case ImageCacheImageLogo:
		return "logo_source_path", true
	default:
		return "", false
	}
}

func (r *ImageCacheJobRepository) DeleteSucceededBefore(ctx context.Context, before time.Time, limit int) (int, error) {
	if r == nil || r.pool == nil || limit <= 0 {
		return 0, nil
	}
	tag, err := r.pool.Exec(ctx, `
		WITH doomed AS (
			SELECT id
			FROM metadata_image_cache_jobs
			WHERE status = 'succeeded'
			  AND completed_at < $1
			ORDER BY completed_at ASC, id ASC
			LIMIT $2
		)
		DELETE FROM metadata_image_cache_jobs j
		USING doomed
		WHERE j.id = doomed.id
	`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("deleting old succeeded metadata image cache jobs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *ImageCacheJobRepository) EnqueueExistingProviderArtwork(ctx context.Context, limit int) (int, error) {
	if r == nil || r.pool == nil || limit <= 0 {
		return 0, nil
	}
	// Each branch is restricted to provider-origin sources (LIKE '%://%' minus
	// cached/system schemes). Local file:// sidecar sources are deliberately
	// excluded from this sweep: their recovery is refresh-driven (a metadata
	// refresh re-discovers the sidecar and enqueues a fresh job), so a stable
	// local failure cannot be resurrected here every cycle.
	// Each branch is also restricted to targets whose stored *_path is not already a
	// cached relative path. The destination check makes the cached row itself the
	// durable dedup marker, so pruning succeeded job rows does not cause the whole
	// catalog to be re-downloaded once the rows age out.
	query := strings.ReplaceAll(`
		WITH all_candidates AS (
			SELECT
				'poster'::text AS image_type,
				'item'::text AS target_type,
				mi.content_id AS target_content_id,
				''::text AS target_language,
				mi.content_id AS series_id,
				mi.poster_source_path AS source_path,
				mi.type AS content_type,
				NULL::integer AS season_number,
				NULL::integer AS episode_number,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_items mi
			WHERE mi.poster_source_path LIKE '%://%'
			  AND lower(mi.poster_source_path) NOT LIKE ALL (@nonProviderSchemes)
			  AND (mi.poster_path LIKE '%://%' OR coalesce(mi.poster_path, '') = '')
			UNION ALL
			SELECT
				'backdrop'::text,
				'item'::text,
				mi.content_id,
				''::text,
				mi.content_id,
				mi.backdrop_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_items mi
			WHERE mi.backdrop_source_path LIKE '%://%'
			  AND lower(mi.backdrop_source_path) NOT LIKE ALL (@nonProviderSchemes)
			  AND (mi.backdrop_path LIKE '%://%' OR coalesce(mi.backdrop_path, '') = '')
			UNION ALL
			SELECT
				'logo'::text,
				'item'::text,
				mi.content_id,
				''::text,
				mi.content_id,
				mi.logo_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_items mi
			WHERE mi.logo_source_path LIKE '%://%'
			  AND lower(mi.logo_source_path) NOT LIKE ALL (@nonProviderSchemes)
			  AND (mi.logo_path LIKE '%://%' OR coalesce(mi.logo_path, '') = '')
			UNION ALL
			SELECT
				'poster'::text,
				'item_localization'::text,
				loc.content_id,
				loc.language,
				loc.content_id,
				loc.poster_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_item_localizations loc
			JOIN media_items mi ON mi.content_id = loc.content_id
			WHERE loc.poster_source_path LIKE '%://%'
			  AND lower(loc.poster_source_path) NOT LIKE ALL (@nonProviderSchemes)
			  AND (loc.poster_path LIKE '%://%' OR coalesce(loc.poster_path, '') = '')
			UNION ALL
			SELECT
				'backdrop'::text,
				'item_localization'::text,
				loc.content_id,
				loc.language,
				loc.content_id,
				loc.backdrop_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_item_localizations loc
			JOIN media_items mi ON mi.content_id = loc.content_id
			WHERE loc.backdrop_source_path LIKE '%://%'
			  AND lower(loc.backdrop_source_path) NOT LIKE ALL (@nonProviderSchemes)
			  AND (loc.backdrop_path LIKE '%://%' OR coalesce(loc.backdrop_path, '') = '')
			UNION ALL
			SELECT
				'logo'::text,
				'item_localization'::text,
				loc.content_id,
				loc.language,
				loc.content_id,
				loc.logo_source_path,
				mi.type,
				NULL::integer,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM media_item_localizations loc
			JOIN media_items mi ON mi.content_id = loc.content_id
			WHERE loc.logo_source_path LIKE '%://%'
			  AND lower(loc.logo_source_path) NOT LIKE ALL (@nonProviderSchemes)
			  AND (loc.logo_path LIKE '%://%' OR coalesce(loc.logo_path, '') = '')
			UNION ALL
			SELECT
				'poster'::text,
				'season'::text,
				s.content_id AS target_content_id,
				''::text AS target_language,
				s.series_id,
				s.poster_source_path AS source_path,
				'series'::text AS content_type,
				s.season_number,
				NULL::integer AS episode_number,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM seasons s
			JOIN media_items mi ON mi.content_id = s.series_id
			WHERE s.poster_source_path LIKE '%://%'
			  AND lower(s.poster_source_path) NOT LIKE ALL (@nonProviderSchemes)
			  AND (s.poster_path LIKE '%://%' OR coalesce(s.poster_path, '') = '')
			UNION ALL
			SELECT
				'poster'::text,
				'season_localization'::text,
				s.content_id,
				loc.language,
				s.series_id,
				loc.poster_source_path,
				'series'::text,
				s.season_number,
				NULL::integer,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM season_localizations loc
			JOIN seasons s ON s.content_id = loc.season_content_id
			JOIN media_items mi ON mi.content_id = s.series_id
			WHERE loc.poster_source_path LIKE '%://%'
			  AND lower(loc.poster_source_path) NOT LIKE ALL (@nonProviderSchemes)
			  AND (loc.poster_path LIKE '%://%' OR coalesce(loc.poster_path, '') = '')
			UNION ALL
			SELECT
				'still'::text,
				'episode'::text,
				e.content_id,
				''::text,
				e.series_id,
				e.still_source_path,
				'series'::text,
				e.season_number,
				e.episode_number,
				mi.tmdb_id,
				mi.tvdb_id,
				mi.imdb_id
			FROM episodes e
			JOIN media_items mi ON mi.content_id = e.series_id
			WHERE e.still_source_path LIKE '%://%'
			  AND lower(e.still_source_path) NOT LIKE ALL (@nonProviderSchemes)
			  AND (e.still_path LIKE '%://%' OR coalesce(e.still_path, '') = '')
			UNION ALL
			SELECT
				'profile'::text,
				'person'::text,
				p.id::text,
				''::text,
				''::text,
				p.photo_source_path,
				'people'::text,
				NULL::integer,
				NULL::integer,
				p.tmdb_id,
				p.tvdb_id,
				p.imdb_id
			FROM people p
			WHERE p.photo_source_path LIKE '%://%'
			  AND lower(p.photo_source_path) NOT LIKE ALL (@nonProviderSchemes)
			  AND (p.photo_path LIKE '%://%' OR coalesce(p.photo_path, '') = '')
		),
		candidates AS (
			SELECT ac.*
			FROM all_candidates ac
			LEFT JOIN metadata_image_cache_jobs j
			  ON j.target_type = ac.target_type
			 AND j.target_content_id = ac.target_content_id
			 AND j.image_type = ac.image_type
			 AND j.target_language = ac.target_language
			WHERE j.id IS NULL
			   OR j.source_path IS DISTINCT FROM ac.source_path
			   OR j.status = 'succeeded'
			   OR (
				   j.status = 'failed'
				   AND j.next_attempt_at <= NOW()
			   )
			ORDER BY ac.target_type, ac.target_content_id, ac.target_language, ac.image_type
			LIMIT $1
		)
		SELECT image_type, target_type, target_content_id, target_language, series_id, source_path,
		       content_type, season_number, episode_number,
		       COALESCE(tmdb_id, '') AS tmdb_id,
		       COALESCE(tvdb_id, '') AS tvdb_id,
		       COALESCE(imdb_id, '') AS imdb_id
		FROM candidates
	`, "@nonProviderSchemes", nonProviderImageSchemesSQL)
	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return 0, fmt.Errorf("enqueueing existing provider artwork: %w", err)
	}
	defer rows.Close()

	inputs := make([]EnqueueImageCacheJobInput, 0, limit)
	for rows.Next() {
		var in EnqueueImageCacheJobInput
		var tmdbID, tvdbID, imdbID string
		if err := rows.Scan(
			&in.ImageType,
			&in.TargetType,
			&in.TargetContentID,
			&in.TargetLanguage,
			&in.SeriesID,
			&in.SourcePath,
			&in.ContentType,
			&in.SeasonNumber,
			&in.EpisodeNumber,
			&tmdbID,
			&tvdbID,
			&imdbID,
		); err != nil {
			return 0, fmt.Errorf("scanning existing provider artwork: %w", err)
		}
		fallbackProvider := imageCachePrimaryProvider(tmdbID, tvdbID, imdbID)
		in.ProviderID = imageCacheProviderIDFromSource(in.SourcePath, fallbackProvider)
		in.ProviderContentID = imageCacheProviderContentID(in.ProviderID, tmdbID, tvdbID, imdbID, firstNonEmpty(in.SeriesID, in.TargetContentID))
		in.ContentType = imageCacheContentType(in.ContentType)
		inputs = append(inputs, in)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterating existing provider artwork: %w", err)
	}
	return r.enqueueBatch(ctx, inputs, true)
}

// imageCacheLocalProviderID is the synthetic provider slug for local sidecar
// artwork; cached keys live under "local/..." like audiobook/ebook covers.
const imageCacheLocalProviderID = "local"

func imageCacheProviderIDFromSource(sourcePath, fallback string) string {
	if isLocalImageSourcePath(sourcePath) {
		return imageCacheLocalProviderID
	}
	if provider := providerIDFromPluginURL(sourcePath); provider != "" {
		return provider
	}
	if fallback != "" {
		return fallback
	}
	return "remote"
}

func imageCachePrimaryProvider(tmdbID, tvdbID, imdbID string) string {
	switch {
	case strings.TrimSpace(tmdbID) != "":
		return "tmdb"
	case strings.TrimSpace(tvdbID) != "":
		return "tvdb"
	case strings.TrimSpace(imdbID) != "":
		return "imdb"
	default:
		return ""
	}
}

func imageCacheProviderContentID(providerID, tmdbID, tvdbID, imdbID, fallback string) string {
	switch providerID {
	case "tmdb":
		return firstNonEmpty(tmdbID, tvdbID, imdbID, fallback)
	case "tvdb":
		return firstNonEmpty(tvdbID, tmdbID, imdbID, fallback)
	case "imdb":
		return firstNonEmpty(imdbID, tmdbID, tvdbID, fallback)
	default:
		return firstNonEmpty(tmdbID, tvdbID, imdbID, fallback)
	}
}

func imageCacheContentType(contentType string) string {
	switch strings.TrimSpace(contentType) {
	case "movie":
		return "movies"
	case "audiobook":
		return "audiobooks"
	case "ebook":
		return "ebooks"
	default:
		return strings.TrimSpace(contentType)
	}
}
