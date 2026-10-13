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

// What a running job is doing.
const (
	// PhaseAnalyzing decodes the file's speech, or reads it from the cache.
	PhaseAnalyzing = "analyzing"
	// PhaseMatching aligns the subtitle's cues with the speech.
	PhaseMatching = "matching"
)

// Why a failed job failed, for clients to explain.
const (
	// FailureSubtitleChanged: the subtitle, its file on disk, or its timing
	// changed while the job ran; the newer state wins.
	FailureSubtitleChanged = "subtitle_changed"
	// FailureNoAudio: the media file has no audio the job can read.
	FailureNoAudio = "no_audio"
	// FailureUnavailable: no server or node could analyze the audio now, or
	// the job was interrupted; a later attempt can succeed.
	FailureUnavailable = "unavailable"
	// FailureError: anything else.
	FailureError = "error"
)

// Job is one sync attempt for a stored subtitle or a sidecar.
type Job struct {
	ID int64
	// SubtitleID is the stored subtitle the job syncs; 0 for a sidecar.
	SubtitleID int
	// ExternalTimingID is the sidecar correction row the job syncs; 0 for a
	// stored subtitle.
	ExternalTimingID int64
	MediaFileID      int
	RequestedBy      *int
	Trigger          string
	BaseRevision     int64
	Status           string
	// Phase and Progress (0..1) describe a running job.
	Phase    string
	Progress *float64
	// Failure says why a failed job failed.
	Failure    string
	Confidence *float64
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

// PhaseQueued is a pending job's phase as clients see it.
const PhaseQueued = "queued"

// PublicPhase is the job's phase as clients see it: queued while pending,
// the recorded phase while running, and none once finished.
func (j *Job) PublicPhase() string {
	switch j.Status {
	case JobPending:
		return PhaseQueued
	case JobRunning:
		if j.Phase == "" {
			return PhaseAnalyzing
		}
		return j.Phase
	}
	return ""
}

// PublicProgress is the job's progress (0..1) while it is active.
func (j *Job) PublicProgress() *float64 {
	switch j.Status {
	case JobPending:
		return new(0.0)
	case JobRunning:
		if j.Progress == nil {
			return new(0.0)
		}
		return j.Progress
	}
	return nil
}

// PublicFailure is why a failed job failed, as clients see it.
func (j *Job) PublicFailure() string {
	if j.Status != JobFailed {
		return ""
	}
	if j.Failure == "" {
		return FailureError
	}
	return j.Failure
}

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

const jobColumns = `id, COALESCE(subtitle_id, 0), COALESCE(external_timing_id, 0), media_file_id, requested_by,
	trigger, base_revision, status, phase, progress, failure, confidence, result_offset_ms, result_scale,
	executed_on, error, created_at, finished_at`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	var offset *int
	var scale *float64
	if err := row.Scan(&j.ID, &j.SubtitleID, &j.ExternalTimingID, &j.MediaFileID, &j.RequestedBy, &j.Trigger,
		&j.BaseRevision, &j.Status, &j.Phase, &j.Progress, &j.Failure, &j.Confidence, &offset, &scale,
		&j.ExecutedOn, &j.Error, &j.CreatedAt, &j.FinishedAt); err != nil {
		return nil, err
	}
	if offset != nil && scale != nil {
		j.Result = &subtitles.Timing{Scale: *scale, OffsetMS: *offset}
	}
	return &j, nil
}

// subjectColumn names the job column that holds a job's subject.
type subjectColumn string

const (
	subjectStored   subjectColumn = "subtitle_id"
	subjectExternal subjectColumn = "external_timing_id"
)

// Create inserts a pending job for sub, or returns the subtitle's active job
// and false when one exists.
func (s *Store) Create(ctx context.Context, sub *subtitles.DownloadedSubtitle, trigger string, requestedBy *int) (*Job, bool, error) {
	return s.create(ctx, subjectStored, int64(sub.ID), sub.MediaFileID, sub.Revision, trigger, requestedBy)
}

// CreateExternal inserts a pending job for a sidecar's correction row, or
// returns its active job and false when one exists.
func (s *Store) CreateExternal(ctx context.Context, timing *subtitles.ExternalTiming, trigger string, requestedBy *int) (*Job, bool, error) {
	return s.create(ctx, subjectExternal, timing.ID, timing.MediaFileID, timing.Revision, trigger, requestedBy)
}

func (s *Store) create(ctx context.Context, column subjectColumn, subjectID int64, mediaFileID int, revision int64, trigger string, requestedBy *int) (*Job, bool, error) {
	for range 2 {
		job, err := scanJob(s.pool.QueryRow(ctx, `
			INSERT INTO subtitle_sync_jobs (`+string(column)+`, media_file_id, requested_by, trigger, base_revision)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (`+string(column)+`) WHERE status IN ('pending', 'running') DO NOTHING
			RETURNING `+jobColumns,
			subjectID, mediaFileID, requestedBy, trigger, revision))
		if err == nil {
			return job, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("create subtitle sync job: %w", err)
		}
		active, err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+`
			FROM subtitle_sync_jobs WHERE `+string(column)+` = $1 AND status IN ('pending', 'running')`, subjectID))
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
	return s.latest(ctx, subjectStored, int64(subtitleID))
}

// LatestExternal returns the most recent job of a sidecar's correction row,
// or nil.
func (s *Store) LatestExternal(ctx context.Context, timingID int64) (*Job, error) {
	return s.latest(ctx, subjectExternal, timingID)
}

func (s *Store) latest(ctx context.Context, column subjectColumn, subjectID int64) (*Job, error) {
	job, err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+`
		FROM subtitle_sync_jobs WHERE `+string(column)+` = $1 ORDER BY id DESC LIMIT 1`, subjectID))
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
	ids := make([]int64, len(subtitleIDs))
	for i, id := range subtitleIDs {
		ids[i] = int64(id)
	}
	byID, err := s.latestFor(ctx, subjectStored, ids)
	jobs := make(map[int]*Job, len(byID))
	for id, job := range byID {
		jobs[int(id)] = job
	}
	return jobs, err
}

// LatestForExternal returns the most recent job of each sidecar correction
// row that has one, keyed by row ID.
func (s *Store) LatestForExternal(ctx context.Context, timingIDs []int64) (map[int64]*Job, error) {
	return s.latestFor(ctx, subjectExternal, timingIDs)
}

func (s *Store) latestFor(ctx context.Context, column subjectColumn, subjectIDs []int64) (map[int64]*Job, error) {
	jobs := make(map[int64]*Job, len(subjectIDs))
	if len(subjectIDs) == 0 {
		return jobs, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (`+string(column)+`) `+jobColumns+`
		FROM subtitle_sync_jobs WHERE `+string(column)+` = ANY($1::bigint[])
		ORDER BY `+string(column)+`, id DESC`, subjectIDs)
	if err != nil {
		return nil, fmt.Errorf("list subtitle sync jobs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subtitle sync job: %w", err)
		}
		if column == subjectStored {
			jobs[int64(job.SubtitleID)] = job
		} else {
			jobs[job.ExternalTimingID] = job
		}
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

// HasExternalJob reports whether a sidecar's bytes ever had a sync job.
func (s *Store) HasExternalJob(ctx context.Context, timingID int64) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subtitle_sync_jobs
		WHERE external_timing_id = $1)`, timingID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check sidecar subtitle sync jobs: %w", err)
	}
	return exists, nil
}

// MarkRunning moves a pending job to running, analyzing from 0. It returns
// jobrunner.ErrJobTerminal when the job is gone or already finished.
func (s *Store) MarkRunning(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE subtitle_sync_jobs
		SET status = 'running', phase = 'analyzing', progress = 0, heartbeat_at = now()
		WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return fmt.Errorf("start subtitle sync job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return jobrunner.ErrJobTerminal
	}
	return nil
}

// Progress records what a running job is doing and how far it got; it also
// counts as a heartbeat. A job no longer running is left as it is and
// returns jobrunner.ErrJobTerminal.
func (s *Store) Progress(ctx context.Context, id int64, phase string, progress float64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE subtitle_sync_jobs SET phase = $2, progress = $3, heartbeat_at = now()
		WHERE id = $1 AND status = 'running'`, id, phase, progress)
	if err != nil {
		return fmt.Errorf("record subtitle sync progress: %w", err)
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
		SET status = 'failed', error = $2, failure = 'unavailable', phase = '', progress = NULL, finished_at = now()
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
	// Failure is set on a failed outcome.
	Failure string
}

// FinishUnchanged records a verdict that leaves the subject as it is
// (already synced, no match) only while the subject still carries the job's
// base revision, checked under a row lock in the same transaction: the
// verdict is about that revision. Otherwise nothing is recorded and the
// result is ErrSubtitleChanged; a job already finished is
// jobrunner.ErrJobTerminal.
func (s *Store) FinishUnchanged(ctx context.Context, job *Job, o Outcome) error {
	query, subjectID := `SELECT revision FROM downloaded_subtitles WHERE id = $1 AND media_file_id = $2 FOR SHARE`, int64(job.SubtitleID)
	if job.ExternalTimingID != 0 {
		query, subjectID = `SELECT revision FROM external_subtitle_timings WHERE id = $1 AND media_file_id = $2 FOR SHARE`, job.ExternalTimingID
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var revision int64
		err := tx.QueryRow(ctx, query, subjectID, job.MediaFileID).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && revision != job.BaseRevision) {
			return ErrSubtitleChanged
		}
		if err != nil {
			return fmt.Errorf("read subtitle revision: %w", err)
		}
		tag, err := tx.Exec(ctx, finishJobSQL, append([]any{job.ID}, outcomeArgs(o)...)...)
		if err != nil {
			return fmt.Errorf("finish subtitle sync job: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return jobrunner.ErrJobTerminal
		}
		return nil
	})
}

// Finish records an outcome that leaves the subtitle as it is.
func (s *Store) Finish(ctx context.Context, id int64, o Outcome) error {
	tag, err := s.pool.Exec(ctx, finishJobSQL, append([]any{id}, outcomeArgs(o)...)...)
	if err != nil {
		return fmt.Errorf("finish subtitle sync job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Reaped, or deleted with a replaced file: the outcome is not news.
		return jobrunner.ErrJobTerminal
	}
	return nil
}

const finishJobSQL = `UPDATE subtitle_sync_jobs
	SET status = $2, confidence = $3, result_offset_ms = $4, result_scale = $5,
	    executed_on = $6, error = $7, failure = $8, phase = '', progress = NULL, finished_at = now()
	WHERE id = $1 AND status IN ('pending', 'running')`

func outcomeArgs(o Outcome) []any {
	var offset *int
	var scale *float64
	if o.Result != nil {
		n := o.Result.Normalized()
		offset, scale = &n.OffsetMS, &n.Scale
	}
	failure := o.Failure
	if o.Status == JobFailed && failure == "" {
		failure = FailureError
	}
	return []any{o.Status, o.Confidence, offset, scale, o.ExecutedOn, o.Error, failure}
}

// Apply stores timing on the job's subject (a stored subtitle or a sidecar's
// correction row) and finishes the job in one transaction. The subject must
// still carry the job's base revision; otherwise nothing changes and the
// result is ErrSubtitleChanged. It returns the subject's new revision.
func (s *Store) Apply(ctx context.Context, job *Job, timing subtitles.Timing, o Outcome) (int64, error) {
	timing = timing.Normalized()
	update, subjectID := `UPDATE downloaded_subtitles`, int64(job.SubtitleID)
	if job.ExternalTimingID != 0 {
		update, subjectID = `UPDATE external_subtitle_timings`, job.ExternalTimingID
	}
	var revision int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, update+`
			SET timing_offset_ms = $3, timing_scale = $4
			WHERE id = $1 AND revision = $2 AND media_file_id = $5
			RETURNING revision`, subjectID, job.BaseRevision, timing.OffsetMS, timing.Scale, job.MediaFileID).Scan(&revision)
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
