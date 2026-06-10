package ai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgJobRepository implements JobRepository on PostgreSQL.
type PgJobRepository struct {
	pool *pgxpool.Pool
}

// NewPgJobRepository creates a Postgres-backed job repository.
func NewPgJobRepository(pool *pgxpool.Pool) *PgJobRepository {
	return &PgJobRepository{pool: pool}
}

const jobColumns = `id, media_file_id, kind, source_index, source_language, target_language,
	engine, model, status, progress, progress_message, result_subtitle_id,
	error_message, idempotency_key, requested_by, created_at, updated_at, heartbeat_at`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(
		&j.ID, &j.MediaFileID, &j.Kind, &j.SourceIndex, &j.SourceLanguage, &j.TargetLanguage,
		&j.Engine, &j.Model, &j.Status, &j.Progress, &j.ProgressMessage, &j.ResultSubtitleID,
		&j.ErrorMessage, &j.IdempotencyKey, &j.RequestedBy, &j.CreatedAt, &j.UpdatedAt, &j.HeartbeatAt,
	)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// transcribeQuotaCountSQL counts a user's quota-consuming transcription jobs.
// Failed/cancelled rows with zero progress never did ASR work (engine
// unreachable, bad media file, cancelled before start) and are excluded so
// server-side faults don't lock the user out; terminal rows with progress > 0
// consumed compute and still count. Shared by the quota status endpoint and
// the insert-time guard so the number shown always matches the one enforced.
const transcribeQuotaCountSQL = `SELECT count(*) FROM subtitle_ai_jobs
	WHERE requested_by = $1 AND created_at >= $2
	AND kind IN ('transcribe', 'transcribe_translate')
	AND NOT (status IN ('failed', 'cancelled') AND progress = 0)`

// transcribeQuotaLockNamespace partitions advisory locks so the transcription
// quota's per-user lock cannot collide with advisory locks held elsewhere in
// the database (media requests use 139, autoscan 900173). The value is
// arbitrary; what matters is that it is stable.
const transcribeQuotaLockNamespace = 412

func (r *PgJobRepository) InsertJob(ctx context.Context, job *Job, quota *JobQuota) error {
	if job.Engine == "" {
		job.Engine = "openai"
	}
	if job.Status == "" {
		job.Status = JobStatusPending
	}
	if quota == nil {
		return r.insertJob(ctx, r.pool, job)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin insert subtitle ai job: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	// Serialize check-and-insert per user so concurrent requests cannot race
	// past the limit (same pattern as the media-request quota).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int4, $2::int4)`,
		transcribeQuotaLockNamespace, quota.UserID); err != nil {
		return fmt.Errorf("acquire transcription quota lock: %w", err)
	}
	var used int
	if err := tx.QueryRow(ctx, transcribeQuotaCountSQL, quota.UserID, quota.Since).Scan(&used); err != nil {
		return fmt.Errorf("count transcribe jobs for quota: %w", err)
	}
	if used >= quota.Limit {
		return &QuotaExceededError{Limit: quota.Limit, Used: used, Period: quota.Period}
	}
	if err := r.insertJob(ctx, tx, job); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit insert subtitle ai job: %w", err)
	}
	return nil
}

// rowQuerier abstracts pool vs transaction for insertJob.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (r *PgJobRepository) insertJob(ctx context.Context, q rowQuerier, job *Job) error {
	return q.QueryRow(ctx,
		`INSERT INTO subtitle_ai_jobs
			(media_file_id, kind, source_index, source_language, target_language,
			 engine, model, status, progress, progress_message, idempotency_key, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, created_at, updated_at, heartbeat_at`,
		job.MediaFileID, job.Kind, job.SourceIndex, job.SourceLanguage, job.TargetLanguage,
		job.Engine, job.Model, job.Status, job.Progress, job.ProgressMessage, job.IdempotencyKey, job.RequestedBy,
	).Scan(&job.ID, &job.CreatedAt, &job.UpdatedAt, &job.HeartbeatAt)
}

func (r *PgJobRepository) GetJob(ctx context.Context, id int64) (*Job, error) {
	job, err := scanJob(r.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM subtitle_ai_jobs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subtitle ai job: %w", err)
	}
	return job, nil
}

func (r *PgJobRepository) GetActiveJobByIdempotencyKey(ctx context.Context, key string) (*Job, error) {
	job, err := scanJob(r.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM subtitle_ai_jobs
		WHERE idempotency_key = $1 AND status IN ('pending', 'running')
		ORDER BY created_at DESC LIMIT 1`, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active subtitle ai job: %w", err)
	}
	return job, nil
}

func (r *PgJobRepository) ListJobsByMediaFile(ctx context.Context, mediaFileID int) ([]Job, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+jobColumns+` FROM subtitle_ai_jobs
		WHERE media_file_id = $1 ORDER BY created_at DESC LIMIT 50`, mediaFileID)
	if err != nil {
		return nil, fmt.Errorf("list subtitle ai jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subtitle ai job: %w", err)
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

func (r *PgJobRepository) CountTranscribeJobsByUserSince(ctx context.Context, userID int, since time.Time) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, transcribeQuotaCountSQL, userID, since).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count transcribe jobs by user: %w", err)
	}
	return count, nil
}

// UpdateProgress, CompleteJob, and FailJob only transition a job that is still
// active ("pending"/"running"). The guard makes them no-ops on an already
// terminal row, so a job that was cancelled or reaped as stale can never be
// resurrected by a late write from its own worker goroutine.
func (r *PgJobRepository) UpdateProgress(ctx context.Context, id int64, status JobStatus, progress float64, message string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE subtitle_ai_jobs
		SET status = $2, progress = $3, progress_message = $4, updated_at = now(), heartbeat_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')`, id, status, progress, message)
	if err != nil {
		return fmt.Errorf("update subtitle ai job progress: %w", err)
	}
	return nil
}

func (r *PgJobRepository) CompleteJob(ctx context.Context, id int64, subtitleID int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE subtitle_ai_jobs
		SET status = 'completed', progress = 1, result_subtitle_id = $2,
			error_message = '', updated_at = now(), heartbeat_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')`, id, subtitleID)
	if err != nil {
		return fmt.Errorf("complete subtitle ai job: %w", err)
	}
	return nil
}

func (r *PgJobRepository) FailJob(ctx context.Context, id int64, status JobStatus, message string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE subtitle_ai_jobs
		SET status = $2, error_message = $3, updated_at = now(), heartbeat_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')`, id, status, message)
	if err != nil {
		return fmt.Errorf("fail subtitle ai job: %w", err)
	}
	return nil
}

func (r *PgJobRepository) Heartbeat(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE subtitle_ai_jobs SET heartbeat_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("heartbeat subtitle ai job: %w", err)
	}
	return nil
}

func (r *PgJobRepository) ResetStaleJobs(ctx context.Context, before time.Time, message string) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE subtitle_ai_jobs
		SET status = 'failed', error_message = $1, updated_at = now()
		WHERE status IN ('pending', 'running') AND heartbeat_at < $2`, message, before)
	if err != nil {
		return 0, fmt.Errorf("reset stale subtitle ai jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}
