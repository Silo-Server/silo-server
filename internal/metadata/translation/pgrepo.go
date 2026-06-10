package translation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
)

// PgRepository implements JobRepository and ContentReader on PostgreSQL.
type PgRepository struct {
	pool *pgxpool.Pool
}

// NewPgRepository creates a Postgres-backed repository.
func NewPgRepository(pool *pgxpool.Pool) *PgRepository {
	return &PgRepository{pool: pool}
}

const jobColumns = `id, target_kind, content_id, include_children, source_language, target_language,
	engine, model, status, progress, progress_message, fields_done, fields_total, force,
	error_message, idempotency_key, requested_by, created_at, updated_at, heartbeat_at`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(
		&j.ID, &j.TargetKind, &j.ContentID, &j.IncludeChildren, &j.SourceLanguage, &j.TargetLanguage,
		&j.Engine, &j.Model, &j.Status, &j.Progress, &j.ProgressMessage, &j.FieldsDone, &j.FieldsTotal, &j.Force,
		&j.ErrorMessage, &j.IdempotencyKey, &j.RequestedBy, &j.CreatedAt, &j.UpdatedAt, &j.HeartbeatAt,
	)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (r *PgRepository) InsertJob(ctx context.Context, job *Job) error {
	if job.Engine == "" {
		job.Engine = "openai"
	}
	if job.Status == "" {
		job.Status = jobrunner.StatusPending
	}
	return r.pool.QueryRow(ctx,
		`INSERT INTO metadata_translation_jobs
			(target_kind, content_id, include_children, source_language, target_language,
			 engine, model, status, progress, progress_message, force, idempotency_key, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, created_at, updated_at, heartbeat_at`,
		job.TargetKind, job.ContentID, job.IncludeChildren, job.SourceLanguage, job.TargetLanguage,
		job.Engine, job.Model, job.Status, job.Progress, job.ProgressMessage, job.Force, job.IdempotencyKey, job.RequestedBy,
	).Scan(&job.ID, &job.CreatedAt, &job.UpdatedAt, &job.HeartbeatAt)
}

func (r *PgRepository) GetJob(ctx context.Context, id int64) (*Job, error) {
	job, err := scanJob(r.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM metadata_translation_jobs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get metadata translation job: %w", err)
	}
	return job, nil
}

func (r *PgRepository) GetActiveJobByIdempotencyKey(ctx context.Context, key string) (*Job, error) {
	job, err := scanJob(r.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM metadata_translation_jobs
		WHERE idempotency_key = $1 AND status IN ('pending', 'running')
		ORDER BY created_at DESC LIMIT 1`, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active metadata translation job: %w", err)
	}
	return job, nil
}

func (r *PgRepository) ListJobsByContent(ctx context.Context, contentID string) ([]Job, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+jobColumns+` FROM metadata_translation_jobs
		WHERE content_id = $1 ORDER BY created_at DESC LIMIT 50`, contentID)
	if err != nil {
		return nil, fmt.Errorf("list metadata translation jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan metadata translation job: %w", err)
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

// UpdateProgress, CompleteJob, and FailJob only transition a job that is still
// active ("pending"/"running"). The guard makes them no-ops on an already
// terminal row, so a job that was cancelled or reaped as stale can never be
// resurrected by a late write from its own worker goroutine.
func (r *PgRepository) UpdateProgress(ctx context.Context, id int64, status JobStatus, progress float64, message string, fieldsDone, fieldsTotal int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE metadata_translation_jobs
		SET status = $2, progress = $3, progress_message = $4, fields_done = $5, fields_total = $6,
			updated_at = now(), heartbeat_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')`, id, status, progress, message, fieldsDone, fieldsTotal)
	if err != nil {
		return fmt.Errorf("update metadata translation job progress: %w", err)
	}
	return nil
}

func (r *PgRepository) CompleteJob(ctx context.Context, id int64, message string, fieldsDone, fieldsTotal int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE metadata_translation_jobs
		SET status = 'completed', progress = 1, progress_message = $2, fields_done = $3, fields_total = $4,
			error_message = '', updated_at = now(), heartbeat_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')`, id, message, fieldsDone, fieldsTotal)
	if err != nil {
		return fmt.Errorf("complete metadata translation job: %w", err)
	}
	return nil
}

func (r *PgRepository) FailJob(ctx context.Context, id int64, status JobStatus, message string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE metadata_translation_jobs
		SET status = $2, error_message = $3, updated_at = now(), heartbeat_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')`, id, status, message)
	if err != nil {
		return fmt.Errorf("fail metadata translation job: %w", err)
	}
	return nil
}

func (r *PgRepository) Heartbeat(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE metadata_translation_jobs SET heartbeat_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("heartbeat metadata translation job: %w", err)
	}
	return nil
}

func (r *PgRepository) ResetStaleJobs(ctx context.Context, before time.Time, message string) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE metadata_translation_jobs
		SET status = 'failed', error_message = $1, updated_at = now()
		WHERE status IN ('pending', 'running') AND heartbeat_at < $2`, message, before)
	if err != nil {
		return 0, fmt.Errorf("reset stale metadata translation jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *PgRepository) ItemText(ctx context.Context, contentID string) (*ItemText, error) {
	var item ItemText
	err := r.pool.QueryRow(ctx,
		`SELECT content_id, type, title, COALESCE(year, 0), COALESCE(overview, ''), COALESCE(tagline, ''),
			COALESCE(default_metadata_language, '')
		FROM media_items WHERE content_id = $1`, contentID).
		Scan(&item.ContentID, &item.Type, &item.Title, &item.Year, &item.Overview, &item.Tagline, &item.DefaultLanguage)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load item text: %w", err)
	}
	return &item, nil
}

func (r *PgRepository) SeasonTexts(ctx context.Context, seriesID string) ([]ChildText, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT content_id, season_number, COALESCE(overview, '')
		FROM seasons WHERE series_id = $1 ORDER BY season_number`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("load season texts: %w", err)
	}
	defer rows.Close()
	var out []ChildText
	for rows.Next() {
		var c ChildText
		if err := rows.Scan(&c.ContentID, &c.SeasonNumber, &c.Overview); err != nil {
			return nil, fmt.Errorf("scan season text: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PgRepository) EpisodeTexts(ctx context.Context, seriesID string) ([]ChildText, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT content_id, season_number, episode_number, COALESCE(overview, '')
		FROM episodes WHERE series_id = $1 ORDER BY season_number, episode_number`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("load episode texts: %w", err)
	}
	defer rows.Close()
	var out []ChildText
	for rows.Next() {
		var c ChildText
		if err := rows.Scan(&c.ContentID, &c.SeasonNumber, &c.EpisodeNumber, &c.Overview); err != nil {
			return nil, fmt.Errorf("scan episode text: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PgRepository) SeasonByID(ctx context.Context, contentID string) (*ChildText, string, error) {
	var c ChildText
	var seriesID string
	err := r.pool.QueryRow(ctx,
		`SELECT content_id, series_id, season_number, COALESCE(overview, '')
		FROM seasons WHERE content_id = $1`, contentID).
		Scan(&c.ContentID, &seriesID, &c.SeasonNumber, &c.Overview)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("load season: %w", err)
	}
	return &c, seriesID, nil
}

func (r *PgRepository) EpisodeByID(ctx context.Context, contentID string) (*ChildText, string, error) {
	var c ChildText
	var seriesID string
	err := r.pool.QueryRow(ctx,
		`SELECT content_id, series_id, season_number, episode_number, COALESCE(overview, '')
		FROM episodes WHERE content_id = $1`, contentID).
		Scan(&c.ContentID, &seriesID, &c.SeasonNumber, &c.EpisodeNumber, &c.Overview)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("load episode: %w", err)
	}
	return &c, seriesID, nil
}

// CountMissingFields counts base fields with source text but no localized
// value for the language. One round-trip; used to gate the auto-translate
// enqueue so repeat refreshes of fully translated items are free.
func (r *PgRepository) CountMissingFields(ctx context.Context, itemContentID, language string) (int, error) {
	var missing int
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM media_items mi
				LEFT JOIN media_item_localizations l ON l.content_id = mi.content_id AND l.language = $2
			 WHERE mi.content_id = $1
			   AND COALESCE(mi.overview, '') <> '' AND COALESCE(l.overview, '') = '')
		+	(SELECT count(*) FROM media_items mi
				LEFT JOIN media_item_localizations l ON l.content_id = mi.content_id AND l.language = $2
			 WHERE mi.content_id = $1
			   AND COALESCE(mi.tagline, '') <> '' AND COALESCE(l.tagline, '') = '')
		+	(SELECT count(*) FROM seasons s
				LEFT JOIN season_localizations sl ON sl.season_content_id = s.content_id AND sl.language = $2
			 WHERE s.series_id = $1
			   AND COALESCE(s.overview, '') <> '' AND COALESCE(sl.overview, '') = '')
		+	(SELECT count(*) FROM episodes e
				LEFT JOIN episode_localizations el ON el.episode_content_id = e.content_id AND el.language = $2
			 WHERE e.series_id = $1
			   AND COALESCE(e.overview, '') <> '' AND COALESCE(el.overview, '') = '')
	`, itemContentID, language).Scan(&missing)
	if err != nil {
		return 0, fmt.Errorf("count missing localization fields: %w", err)
	}
	return missing, nil
}
