package subsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// Job triggers.
const (
	TriggerAuto   = "auto"
	TriggerManual = "manual"
)

// Job statuses beyond the alignment outcomes.
const (
	JobPending = "pending"
	JobRunning = "running"
	JobFailed  = "failed"
)

// Job is one sync attempt for a stored subtitle.
type Job struct {
	ID           int64
	SubtitleID   int
	MediaFileID  int
	RequestedBy  *int
	Trigger      string
	BaseRevision int64
	Status       string
	Confidence   *float64
	// Result is the correction the alignment found; nil until it finished
	// with one.
	Result     *subtitles.Timing
	ExecutedOn string
	Error      string
	CreatedAt  time.Time
	FinishedAt *time.Time
}

// Active reports whether the job is still queued or running.
func (j *Job) Active() bool { return j.Status == JobPending || j.Status == JobRunning }

// ErrSubtitleChanged reports that the subtitle row changed after the job
// captured its revision; the newer edit wins.
var ErrSubtitleChanged = errors.New("subtitle changed during sync")

// Store persists sync jobs in subtitle_sync_jobs.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store over pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ jobrunner.Store = (*Store)(nil)

const jobColumns = `id, subtitle_id, media_file_id, requested_by, trigger, base_revision, status,
	confidence, result_offset_ms, result_scale, executed_on, error, created_at, finished_at`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	var offset *int
	var scale *float64
	if err := row.Scan(&j.ID, &j.SubtitleID, &j.MediaFileID, &j.RequestedBy, &j.Trigger, &j.BaseRevision, &j.Status,
		&j.Confidence, &offset, &scale, &j.ExecutedOn, &j.Error, &j.CreatedAt, &j.FinishedAt); err != nil {
		return nil, err
	}
	if offset != nil && scale != nil {
		j.Result = &subtitles.Timing{Scale: *scale, OffsetMS: *offset}
	}
	return &j, nil
}

// Create inserts a pending job for sub, or returns the subtitle's active job
// and false when one exists.
func (s *Store) Create(ctx context.Context, sub *subtitles.DownloadedSubtitle, trigger string, requestedBy *int) (*Job, bool, error) {
	for range 2 {
		job, err := scanJob(s.pool.QueryRow(ctx, `
			INSERT INTO subtitle_sync_jobs (subtitle_id, media_file_id, requested_by, trigger, base_revision)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (subtitle_id) WHERE status IN ('pending', 'running') DO NOTHING
			RETURNING `+jobColumns,
			sub.ID, sub.MediaFileID, requestedBy, trigger, sub.Revision))
		if err == nil {
			return job, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("create subtitle sync job: %w", err)
		}
		active, err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+`
			FROM subtitle_sync_jobs WHERE subtitle_id = $1 AND status IN ('pending', 'running')`, sub.ID))
		if err == nil {
			return active, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("load active subtitle sync job: %w", err)
		}
		// The active job finished between the two statements; insert again.
	}
	return nil, false, errors.New("create subtitle sync job: active job kept changing")
}

// Latest returns the subtitle's most recent job, or nil.
func (s *Store) Latest(ctx context.Context, subtitleID int) (*Job, error) {
	job, err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+`
		FROM subtitle_sync_jobs WHERE subtitle_id = $1 ORDER BY id DESC LIMIT 1`, subtitleID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load subtitle sync job: %w", err)
	}
	return job, nil
}

// LatestForSubtitles returns the most recent job of each subtitle that has
// one, keyed by subtitle ID.
func (s *Store) LatestForSubtitles(ctx context.Context, subtitleIDs []int) (map[int]*Job, error) {
	jobs := make(map[int]*Job, len(subtitleIDs))
	if len(subtitleIDs) == 0 {
		return jobs, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (subtitle_id) `+jobColumns+`
		FROM subtitle_sync_jobs WHERE subtitle_id = ANY($1::int[])
		ORDER BY subtitle_id, id DESC`, subtitleIDs)
	if err != nil {
		return nil, fmt.Errorf("list subtitle sync jobs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subtitle sync job: %w", err)
		}
		jobs[job.SubtitleID] = job
	}
	return jobs, rows.Err()
}

// HasJob reports whether the subtitle has any sync job, finished or not.
func (s *Store) HasJob(ctx context.Context, subtitleID int) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subtitle_sync_jobs
		WHERE subtitle_id = $1)`, subtitleID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check subtitle sync jobs: %w", err)
	}
	return exists, nil
}

// MarkRunning moves a pending job to running. It returns
// jobrunner.ErrJobTerminal when the job is gone or already finished.
func (s *Store) MarkRunning(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE subtitle_sync_jobs SET status = 'running', heartbeat_at = now()
		WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return fmt.Errorf("start subtitle sync job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return jobrunner.ErrJobTerminal
	}
	return nil
}

// Heartbeat refreshes an active job. A job that is gone (its subtitle was
// deleted) or finished returns jobrunner.ErrJobTerminal.
func (s *Store) Heartbeat(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE subtitle_sync_jobs SET heartbeat_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return jobrunner.ErrJobTerminal
	}
	return nil
}

// ResetStaleJobs fails active jobs whose heartbeat predates before.
func (s *Store) ResetStaleJobs(ctx context.Context, before time.Time, message string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE subtitle_sync_jobs
		SET status = 'failed', error = $2, finished_at = now()
		WHERE status IN ('pending', 'running') AND heartbeat_at < $1`, before, message)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Outcome is how a job finished.
type Outcome struct {
	Status     string
	Confidence *float64
	Result     *subtitles.Timing
	ExecutedOn string
	Error      string
}

// Finish records an outcome that leaves the subtitle as it is.
func (s *Store) Finish(ctx context.Context, id int64, o Outcome) error {
	_, err := s.pool.Exec(ctx, finishJobSQL, append([]any{id}, outcomeArgs(o)...)...)
	if err != nil {
		return fmt.Errorf("finish subtitle sync job: %w", err)
	}
	return nil
}

const finishJobSQL = `UPDATE subtitle_sync_jobs
	SET status = $2, confidence = $3, result_offset_ms = $4, result_scale = $5,
	    executed_on = $6, error = $7, finished_at = now()
	WHERE id = $1 AND status IN ('pending', 'running')`

func outcomeArgs(o Outcome) []any {
	var offset *int
	var scale *float64
	if o.Result != nil {
		n := o.Result.Normalized()
		offset, scale = &n.OffsetMS, &n.Scale
	}
	return []any{o.Status, o.Confidence, offset, scale, o.ExecutedOn, o.Error}
}

// Apply stores timing on the job's subtitle and finishes the job in one
// transaction. The subtitle must still carry the job's base revision;
// otherwise nothing changes and the result is ErrSubtitleChanged. It returns
// the subtitle's new revision.
func (s *Store) Apply(ctx context.Context, job *Job, timing subtitles.Timing, o Outcome) (int64, error) {
	timing = timing.Normalized()
	var revision int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE downloaded_subtitles
			SET timing_offset_ms = $3, timing_scale = $4
			WHERE id = $1 AND revision = $2
			RETURNING revision`, job.SubtitleID, job.BaseRevision, timing.OffsetMS, timing.Scale).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSubtitleChanged
		}
		if err != nil {
			return fmt.Errorf("apply subtitle timing: %w", err)
		}
		tag, err := tx.Exec(ctx, finishJobSQL, append([]any{job.ID}, outcomeArgs(o)...)...)
		if err != nil {
			return fmt.Errorf("finish subtitle sync job: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Reaped or otherwise finished meanwhile: do not apply.
			return jobrunner.ErrJobTerminal
		}
		return nil
	})
	return revision, err
}
