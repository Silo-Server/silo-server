package adminjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	JobTypeCatalogExport = "catalog_export"
	JobTypeCatalogImport = "catalog_import"

	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

var (
	ErrJobNotFound       = errors.New("admin job not found")
	ErrActiveJobConflict = errors.New("admin job already active for type")
	ErrJobNotCancellable = errors.New("admin job is not cancellable")
)

type ActiveJobConflictError struct {
	Job *models.AdminJob
}

func (e *ActiveJobConflictError) Error() string {
	if e.Job == nil {
		return ErrActiveJobConflict.Error()
	}
	return fmt.Sprintf("%s: %s", ErrActiveJobConflict.Error(), e.Job.ID)
}

func (e *ActiveJobConflictError) Unwrap() error {
	return ErrActiveJobConflict
}

type CreateJobInput struct {
	JobType         string
	CreatedByUserID int
	RequestPayload  any
	Message         string
}

type CompleteJobInput struct {
	ResultPayload     any
	Message           string
	ProgressCurrent   int
	ProgressTotal     int
	ArtifactBucket    string
	ArtifactKey       string
	ArtifactSizeBytes int64
	ExpiresAt         time.Time
}

type FailJobInput struct {
	Message         string
	ErrorMessage    string
	ProgressCurrent int
	ProgressTotal   int
	ExpiresAt       time.Time
}

type ListJobsOptions struct {
	JobType string
	Limit   int
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const adminJobColumns = `id, job_type, status, created_by_user_id, request_payload,
	result_payload, message, error_message, progress_current, progress_total,
	artifact_bucket, artifact_key, artifact_size_bytes,
	public_url, requested_at, started_at, completed_at, heartbeat_at, expires_at,
	published_at, updated_at`

func scanAdminJob(row pgx.Row) (*models.AdminJob, error) {
	var job models.AdminJob
	err := row.Scan(
		&job.ID,
		&job.JobType,
		&job.Status,
		&job.CreatedByUserID,
		&job.RequestPayload,
		&job.ResultPayload,
		&job.Message,
		&job.ErrorMessage,
		&job.ProgressCurrent,
		&job.ProgressTotal,
		&job.ArtifactBucket,
		&job.ArtifactKey,
		&job.ArtifactSizeBytes,
		&job.PublicURL,
		&job.RequestedAt,
		&job.StartedAt,
		&job.CompletedAt,
		&job.HeartbeatAt,
		&job.ExpiresAt,
		&job.PublishedAt,
		&job.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrJobNotFound
		}
		return nil, fmt.Errorf("scanning admin job: %w", err)
	}
	return &job, nil
}

func scanAdminJobs(rows pgx.Rows) ([]*models.AdminJob, error) {
	var jobs []*models.AdminJob
	for rows.Next() {
		job, err := scanAdminJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating admin jobs: %w", err)
	}
	return jobs, nil
}

func (r *Repository) Create(ctx context.Context, input CreateJobInput) (*models.AdminJob, error) {
	payload, err := marshalPayload(input.RequestPayload)
	if err != nil {
		return nil, fmt.Errorf("marshaling admin job request payload: %w", err)
	}

	id, err := idgen.NextID()
	if err != nil {
		return nil, fmt.Errorf("generate job id: %w", err)
	}
	job, err := scanAdminJob(r.pool.QueryRow(ctx, `
		INSERT INTO admin_jobs (
			id, job_type, status, created_by_user_id, request_payload, message
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+adminJobColumns,
		id,
		input.JobType,
		StatusQueued,
		input.CreatedByUserID,
		payload,
		input.Message,
	))
	if err == nil {
		return job, nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		activeJob, lookupErr := r.GetActiveByType(ctx, input.JobType)
		if lookupErr != nil && !errors.Is(lookupErr, ErrJobNotFound) {
			return nil, lookupErr
		}
		return nil, &ActiveJobConflictError{Job: activeJob}
	}

	return nil, fmt.Errorf("creating admin job: %w", err)
}

func (r *Repository) CreateLibraryRefresh(
	ctx context.Context,
	createdByUserID int,
	req LibraryRefreshRequest,
	message string,
) (*models.AdminJob, error) {
	payload, err := marshalPayload(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling library refresh request payload: %w", err)
	}

	id, err := idgen.NextID()
	if err != nil {
		return nil, fmt.Errorf("generate job id: %w", err)
	}
	job, err := scanAdminJob(r.pool.QueryRow(ctx, `
		INSERT INTO admin_jobs (
			id, job_type, status, created_by_user_id, request_payload, message
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+adminJobColumns,
		id,
		JobTypeLibraryRefresh,
		StatusQueued,
		createdByUserID,
		payload,
		message,
	))
	if err == nil {
		return job, nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		activeJob, lookupErr := r.GetActiveLibraryRefreshByLibraryID(ctx, req.LibraryID)
		if lookupErr != nil && !errors.Is(lookupErr, ErrJobNotFound) {
			return nil, lookupErr
		}
		return nil, &ActiveJobConflictError{Job: activeJob}
	}

	return nil, fmt.Errorf("creating library refresh job: %w", err)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*models.AdminJob, error) {
	return scanAdminJob(r.pool.QueryRow(ctx,
		`SELECT `+adminJobColumns+` FROM admin_jobs WHERE id = $1`,
		id,
	))
}

func (r *Repository) GetActiveByType(ctx context.Context, jobType string) (*models.AdminJob, error) {
	return scanAdminJob(r.pool.QueryRow(ctx, `
		SELECT `+adminJobColumns+`
		FROM admin_jobs
		WHERE job_type = $1 AND status IN ($2, $3)
		ORDER BY requested_at ASC
		LIMIT 1`,
		jobType, StatusQueued, StatusRunning,
	))
}

func (r *Repository) GetActiveLibraryRefreshByLibraryID(ctx context.Context, libraryID int) (*models.AdminJob, error) {
	return scanAdminJob(r.pool.QueryRow(ctx, `
		SELECT `+adminJobColumns+`
		FROM admin_jobs
		WHERE job_type = $1
		  AND status IN ($2, $3)
		  AND request_payload->>'library_id' = $4
		ORDER BY requested_at ASC
		LIMIT 1`,
		JobTypeLibraryRefresh,
		StatusQueued,
		StatusRunning,
		strconv.Itoa(libraryID),
	))
}

func (r *Repository) List(ctx context.Context, opts ListJobsOptions) ([]*models.AdminJob, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}

	args := []any{opts.Limit}
	query := `SELECT ` + adminJobColumns + ` FROM admin_jobs`
	if opts.JobType != "" {
		query += ` WHERE job_type = $2`
		args = append(args, opts.JobType)
		query += ` ORDER BY requested_at DESC LIMIT $1`
	} else {
		query += ` ORDER BY requested_at DESC LIMIT $1`
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing admin jobs: %w", err)
	}
	defer rows.Close()

	return scanAdminJobs(rows)
}

func (r *Repository) ClaimNextQueued(ctx context.Context, jobType string) (*models.AdminJob, error) {
	return r.claimNextQueued(ctx, jobType)
}

func (r *Repository) ClaimNextQueuedByTypes(ctx context.Context, jobTypes []string) (*models.AdminJob, error) {
	if len(jobTypes) == 0 {
		return nil, nil
	}
	return r.claimNextQueued(ctx, jobTypes)
}

func (r *Repository) claimNextQueued(ctx context.Context, jobTypeFilter any) (*models.AdminJob, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("beginning admin job claim transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	if err := tx.QueryRow(ctx, `
		SELECT id
		FROM admin_jobs
		WHERE job_type = ANY($1) AND status = $2
		ORDER BY requested_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1`,
		normalizeJobTypeFilter(jobTypeFilter), StatusQueued,
	).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, tx.Commit(ctx)
		}
		return nil, fmt.Errorf("claiming admin job: %w", err)
	}

	job, err := scanAdminJob(tx.QueryRow(ctx, `
		UPDATE admin_jobs
		SET status = $2,
			started_at = NOW(),
			heartbeat_at = NOW(),
			updated_at = NOW()
		WHERE id = $1
		RETURNING `+adminJobColumns,
		id, StatusRunning,
	))
	if err != nil {
		return nil, fmt.Errorf("marking admin job running: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing admin job claim: %w", err)
	}
	return job, nil
}

func normalizeJobTypeFilter(jobTypeFilter any) []string {
	switch value := jobTypeFilter.(type) {
	case string:
		return []string{value}
	case []string:
		return value
	default:
		return nil
	}
}

func (r *Repository) UpdateProgress(ctx context.Context, id string, current, total int, message string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_jobs
		SET progress_current = $2,
			progress_total = $3,
			message = $4,
			heartbeat_at = NOW(),
			updated_at = NOW()
		WHERE id = $1`,
		id, current, total, message,
	)
	if err != nil {
		return fmt.Errorf("updating admin job progress: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

func (r *Repository) TouchHeartbeat(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_jobs
		SET heartbeat_at = NOW(),
			updated_at = NOW()
		WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("touching admin job heartbeat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

func (r *Repository) Complete(ctx context.Context, id string, input CompleteJobInput) error {
	resultPayload, err := marshalPayload(input.ResultPayload)
	if err != nil {
		return fmt.Errorf("marshaling admin job result payload: %w", err)
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_jobs
		SET status = $2,
			result_payload = $3,
			message = $4,
			error_message = '',
			progress_current = $5,
			progress_total = $6,
			artifact_bucket = $7,
			artifact_key = $8,
			artifact_size_bytes = $9,
			completed_at = NOW(),
			heartbeat_at = NOW(),
			expires_at = $10,
			updated_at = NOW()
		WHERE id = $1`,
		id,
		StatusCompleted,
		resultPayload,
		input.Message,
		input.ProgressCurrent,
		input.ProgressTotal,
		input.ArtifactBucket,
		input.ArtifactKey,
		input.ArtifactSizeBytes,
		input.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("completing admin job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

func (r *Repository) MarkPublic(ctx context.Context, id, publicURL string, publishedAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_jobs
		SET public_url = $2,
			published_at = $3,
			updated_at = NOW()
		WHERE id = $1`,
		id, publicURL, publishedAt,
	)
	if err != nil {
		return fmt.Errorf("marking admin job public: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

func (r *Repository) Fail(ctx context.Context, id string, input FailJobInput) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_jobs
		SET status = $2,
			message = $3,
			error_message = $4,
			progress_current = $5,
			progress_total = $6,
			completed_at = NOW(),
			heartbeat_at = NOW(),
			expires_at = $7,
			updated_at = NOW()
		WHERE id = $1`,
		id,
		StatusFailed,
		input.Message,
		input.ErrorMessage,
		input.ProgressCurrent,
		input.ProgressTotal,
		input.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("failing admin job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

func (r *Repository) Cancel(ctx context.Context, id, message string, expiresAt time.Time) (*models.AdminJob, error) {
	if message == "" {
		message = "Admin job cancelled"
	}
	job, err := scanAdminJob(r.pool.QueryRow(ctx, `
		UPDATE admin_jobs
		SET status = $2,
			message = $3,
			error_message = '',
			completed_at = NOW(),
			heartbeat_at = NOW(),
			expires_at = $4,
			updated_at = NOW()
		WHERE id = $1
		  AND status IN ($5, $6)
		RETURNING `+adminJobColumns,
		id,
		StatusCancelled,
		message,
		expiresAt,
		StatusQueued,
		StatusRunning,
	))
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, ErrJobNotFound) {
		return nil, err
	}
	existing, lookupErr := r.GetByID(ctx, id)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if existing.Status != StatusQueued && existing.Status != StatusRunning {
		return nil, ErrJobNotCancellable
	}
	return nil, ErrJobNotFound
}

func (r *Repository) CancelQueued(ctx context.Context, id, message string, expiresAt time.Time) (*models.AdminJob, error) {
	if message == "" {
		message = "Admin job cancelled"
	}
	job, err := scanAdminJob(r.pool.QueryRow(ctx, `
		UPDATE admin_jobs
		SET status = $2,
			message = $3,
			error_message = '',
			completed_at = NOW(),
			heartbeat_at = NOW(),
			expires_at = $4,
			updated_at = NOW()
		WHERE id = $1
		  AND status = $5
		RETURNING `+adminJobColumns,
		id,
		StatusCancelled,
		message,
		expiresAt,
		StatusQueued,
	))
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, ErrJobNotFound) {
		return nil, err
	}
	existing, lookupErr := r.GetByID(ctx, id)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if existing.Status != StatusQueued {
		return nil, ErrJobNotCancellable
	}
	return nil, ErrJobNotFound
}

func (r *Repository) RequeueStaleRunning(ctx context.Context, before time.Time) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_jobs
		SET status = $2,
			message = 'Requeued after stale worker heartbeat',
			error_message = '',
			started_at = NULL,
			completed_at = NULL,
			heartbeat_at = NULL,
			updated_at = NOW()
		WHERE status = $1
		  AND COALESCE(heartbeat_at, started_at, requested_at) < $3`,
		StatusRunning, StatusQueued, before,
	)
	if err != nil {
		return 0, fmt.Errorf("requeueing stale admin jobs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *Repository) ListExpired(ctx context.Context, now time.Time, limit int) ([]*models.AdminJob, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+adminJobColumns+`
		FROM admin_jobs
		WHERE expires_at IS NOT NULL AND expires_at < $1
		ORDER BY expires_at ASC
		LIMIT $2`,
		now, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("listing expired admin jobs: %w", err)
	}
	defer rows.Close()
	return scanAdminJobs(rows)
}

func (r *Repository) DeleteByID(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM admin_jobs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting admin job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

func marshalPayload(v any) ([]byte, error) {
	if v == nil {
		return []byte(`{}`), nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "null" {
		return []byte(`{}`), nil
	}
	return data, nil
}
