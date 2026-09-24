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
	JobTypeCatalogExport     = "catalog_export"
	JobTypeCatalogImport     = "catalog_import"
	JobTypeStorageTransition = "storage_transition"

	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

var (
	ErrJobNotFound                       = errors.New("admin job not found")
	ErrActiveJobConflict                 = errors.New("admin job already active for type")
	ErrJobNotCancellable                 = errors.New("admin job is not cancellable")
	ErrStorageTransitionAlreadyCommitted = errors.New("storage transition already committed")
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
	ResultPayload   any
	ProgressCurrent int
	ProgressTotal   int
	ExpiresAt       time.Time
}

type ListJobsOptions struct {
	JobType string
	Limit   int
}

type Repository struct {
	pool  *pgxpool.Pool
	claim *int64
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const adminJobColumns = `id, job_type, status, created_by_user_id, request_payload,
	result_payload, message, error_message, progress_current, progress_total,
	artifact_bucket, artifact_key, artifact_size_bytes,
	public_url, requested_at, started_at, completed_at, heartbeat_at, expires_at,
	published_at, updated_at, cancel_requested, claim_generation`

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
		&job.CancelRequested,
		&job.ClaimGeneration,
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

// ListPage reads one bounded page in descending creation order. The ID breaks
// timestamp ties; callers bind the cursor to the administrator and kind filter.
func (r *Repository) ListPage(ctx context.Context, kind string, before time.Time, beforeID string, limit int) ([]*models.AdminJob, error) {
	if limit < 1 || limit > 201 {
		return nil, fmt.Errorf("invalid job page limit")
	}
	args := []any{limit}
	query := `SELECT ` + adminJobColumns + ` FROM admin_jobs WHERE true`
	if kind != "" {
		args = append(args, kind)
		query += fmt.Sprintf(" AND job_type=$%d", len(args))
	}
	if beforeID != "" {
		args = append(args, before, beforeID)
		query += fmt.Sprintf(" AND (requested_at,id)<($%d,$%d)", len(args)-1, len(args))
	}
	query += ` ORDER BY requested_at DESC,id DESC LIMIT $1`
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
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
			claim_generation = claim_generation + 1,
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
		WHERE id = $1 AND status = 'running' AND ($5::bigint IS NULL OR claim_generation = $5)`,
		id, current, total, message, r.claim,
	)
	if err != nil {
		return fmt.Errorf("updating admin job progress: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

func (r *Repository) UpdateProgressResult(ctx context.Context, id string, current, total int, message string, result any) error {
	payload, err := marshalPayload(result)
	if err != nil {
		return fmt.Errorf("marshaling admin job result payload: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_jobs
		SET progress_current = $2,
			progress_total = $3,
			message = $4,
			result_payload = $5,
			heartbeat_at = NOW(),
			updated_at = NOW()
		WHERE id = $1 AND status = 'running' AND ($6::bigint IS NULL OR claim_generation = $6)`,
		id, current, total, message, payload, r.claim,
	)
	if err != nil {
		return fmt.Errorf("updating admin job progress and result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

// HoldStorageTransitionForRestart keeps a committed or possibly committed
// transition's receipt running until the process restarts. A cancellation
// requested now cannot undo the commit, so it is cleared instead of leaving the
// job reported as canceling.
func (r *Repository) HoldStorageTransitionForRestart(ctx context.Context, id string, current, total int, message string, result any) error {
	payload, err := marshalPayload(result)
	if err != nil {
		return fmt.Errorf("marshaling storage transition receipt: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_jobs
		SET progress_current = $2,
			progress_total = $3,
			message = $4,
			result_payload = $5,
			cancel_requested = false,
			heartbeat_at = NOW(),
			updated_at = NOW()
		WHERE id = $1 AND job_type = $7 AND status = 'running'
			AND ($6::bigint IS NULL OR claim_generation = $6)`,
		id, current, total, message, payload, r.claim, JobTypeStorageTransition,
	)
	if err != nil {
		return fmt.Errorf("holding storage transition receipt: %w", err)
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
		WHERE id = $1 AND status = 'running' AND ($2::bigint IS NULL OR claim_generation = $2)`,
		id, r.claim,
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
		SET status = CASE WHEN cancel_requested THEN 'cancelled' ELSE $2 END,
			result_payload = CASE WHEN cancel_requested THEN '{}'::jsonb ELSE $3 END,
			message = $4,
			error_message = '',
			progress_current = $5,
			progress_total = $6,
			artifact_bucket = $7,
			artifact_key = $8,
			artifact_size_bytes = $9,
			completed_at = NOW(),
			heartbeat_at = NOW(),
			expires_at = GREATEST($10, NOW() + INTERVAL '24 hours'),
			updated_at = NOW()
		WHERE id = $1 AND status = 'running' AND ($11::bigint IS NULL OR claim_generation = $11)`,
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
		r.claim,
	)
	if err != nil {
		return fmt.Errorf("completing admin job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

// CompleteCommittedStorageTransition records an irreversible storage commit.
// A cancellation requested after the settings commit cannot undo it, so the
// committed result takes precedence over that late request.
func (r *Repository) CompleteCommittedStorageTransition(ctx context.Context, id string, input CompleteJobInput) error {
	resultPayload, err := marshalPayload(input.ResultPayload)
	if err != nil {
		return fmt.Errorf("marshaling storage transition result payload: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_jobs
		SET status = 'completed',
			result_payload = $2,
			message = $3,
			error_message = '',
			progress_current = $4,
			progress_total = $5,
			cancel_requested = false,
			completed_at = NOW(),
			heartbeat_at = NOW(),
			expires_at = GREATEST($6, NOW() + INTERVAL '24 hours'),
			updated_at = NOW()
		WHERE id = $1 AND job_type = $7 AND status = 'running'
			AND ($8::bigint IS NULL OR claim_generation = $8)`,
		id, resultPayload, input.Message, input.ProgressCurrent, input.ProgressTotal,
		input.ExpiresAt, JobTypeStorageTransition, r.claim,
	)
	if err != nil {
		return fmt.Errorf("completing committed storage transition: %w", err)
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
	var resultPayload []byte
	if input.ResultPayload != nil {
		var err error
		resultPayload, err = marshalPayload(input.ResultPayload)
		if err != nil {
			return fmt.Errorf("marshaling failed admin job result payload: %w", err)
		}
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_jobs
		SET status = CASE WHEN cancel_requested THEN 'cancelled' ELSE $2 END,
			message = $3,
			error_message = $4,
			result_payload = COALESCE($5::jsonb, result_payload),
			progress_current = $6,
			progress_total = $7,
			completed_at = NOW(),
			heartbeat_at = NOW(),
			expires_at = GREATEST($8, NOW() + INTERVAL '24 hours'),
			updated_at = NOW()
		WHERE id = $1 AND status = 'running' AND ($9::bigint IS NULL OR claim_generation = $9)`,
		id,
		StatusFailed,
		input.Message,
		input.ErrorMessage,
		resultPayload,
		input.ProgressCurrent,
		input.ProgressTotal,
		input.ExpiresAt,
		r.claim,
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
			expires_at = GREATEST($4, NOW() + INTERVAL '24 hours'),
			updated_at = NOW()
		WHERE id = $1
		  AND status IN ($5, $6)
 AND ($7::bigint IS NULL OR claim_generation = $7)
		RETURNING `+adminJobColumns,
		id,
		StatusCancelled,
		message,
		expiresAt,
		StatusQueued,
		StatusRunning, r.claim,
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
			expires_at = GREATEST($4, NOW() + INTERVAL '24 hours'),
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
			message = CASE
				WHEN job_type = 'storage_transition' THEN 'Resuming storage transition after an interrupted or unconfirmed commit'
				ELSE 'Requeued after stale worker heartbeat'
			END,
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

// withClaim fences worker writes against a later recovery claim. API readers and
// cancellation commands use the unscoped repository.
func (r *Repository) withClaim(job *models.AdminJob) *Repository {
	return &Repository{pool: r.pool, claim: new(job.ClaimGeneration)}
}

// RequestCancellation durably coalesces intent. A queued job is still claimed by
// the ordinary runner, which acknowledges cancellation without executing it.
func (r *Repository) RequestCancellation(ctx context.Context, id string) (*models.AdminJob, error) {
	job, err := scanAdminJob(r.pool.QueryRow(ctx, `UPDATE admin_jobs
 SET cancel_requested = true, updated_at = CASE WHEN cancel_requested THEN updated_at ELSE NOW() END
 WHERE id = $1 AND job_type = ANY($2) AND status IN ('queued', 'running')
	 RETURNING `+adminJobColumns, id, []string{JobTypeLibraryRefresh, JobTypeStorageTransition}))
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, ErrJobNotFound) {
		return nil, err
	}
	job, err = r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if (job.JobType == JobTypeLibraryRefresh || job.JobType == JobTypeStorageTransition) && job.Status == StatusCancelled {
		return job, nil
	}
	return nil, ErrJobNotCancellable
}

// CreateLibraryDeletion commits disabling the target with its durable work
// intent. Failure rolls both changes back, including an active-job conflict.
func (r *Repository) CreateLibraryDeletion(ctx context.Context, userID int, req DeleteLibraryRequest) (*models.AdminJob, error) {
	payload, err := marshalPayload(req)
	if err != nil {
		return nil, err
	}
	id, err := idgen.NextID()
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE media_folders SET enabled = false WHERE id = $1`, req.LibraryID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrJobNotFound
	}
	// The target row lock serializes duplicate deletion acceptance without
	// preventing independent libraries from being deleted concurrently.
	active, lookupErr := scanAdminJob(tx.QueryRow(ctx, `SELECT `+adminJobColumns+` FROM admin_jobs
 WHERE job_type = $1 AND status IN ('queued','running') AND request_payload->>'library_id' = $2 LIMIT 1`, JobTypeDeleteLibrary, strconv.Itoa(req.LibraryID)))
	if lookupErr == nil {
		return nil, &ActiveJobConflictError{Job: active}
	}
	if !errors.Is(lookupErr, ErrJobNotFound) {
		return nil, lookupErr
	}
	job, err := scanAdminJob(tx.QueryRow(ctx, `INSERT INTO admin_jobs
 (id,job_type,status,created_by_user_id,request_payload,message)
 VALUES ($1,$2,$3,$4,$5,'Queued library deletion') RETURNING `+adminJobColumns,
		id, JobTypeDeleteLibrary, StatusQueued, userID, payload))
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return job, nil
}
