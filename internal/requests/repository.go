package requests

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) GetSettings(ctx context.Context) (Settings, error) {
	var s Settings
	err := r.pool.QueryRow(ctx, `
		SELECT requests_enabled, global_max_requests, global_window_days,
		       global_auto_approval_enabled, force_dual_quality, updated_at
		FROM request_settings
		WHERE id = true
	`).Scan(&s.RequestsEnabled, &s.GlobalMaxRequests, &s.GlobalWindowDays, &s.GlobalAutoApprovalEnabled, &s.ForceDualQuality, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Settings{
				RequestsEnabled:           false,
				GlobalMaxRequests:         5,
				GlobalWindowDays:          7,
				GlobalAutoApprovalEnabled: false,
			}, nil
		}
		return Settings{}, fmt.Errorf("get request settings: %w", err)
	}
	return s, nil
}

func (r *Repository) UpdateSettings(ctx context.Context, settings Settings) (Settings, error) {
	if settings.GlobalWindowDays <= 0 {
		settings.GlobalWindowDays = 7
	}
	if settings.GlobalMaxRequests < 0 {
		settings.GlobalMaxRequests = 0
	}

	var s Settings
	err := r.pool.QueryRow(ctx, `
		INSERT INTO request_settings (
			id, requests_enabled, global_max_requests, global_window_days,
			global_auto_approval_enabled, force_dual_quality, updated_at
		)
		VALUES (true, $1, $2, $3, $4, $5, now())
		ON CONFLICT (id) DO UPDATE SET
			requests_enabled = EXCLUDED.requests_enabled,
			global_max_requests = EXCLUDED.global_max_requests,
			global_window_days = EXCLUDED.global_window_days,
			global_auto_approval_enabled = EXCLUDED.global_auto_approval_enabled,
			force_dual_quality = EXCLUDED.force_dual_quality,
			updated_at = now()
		RETURNING requests_enabled, global_max_requests, global_window_days,
		          global_auto_approval_enabled, force_dual_quality, updated_at
	`, settings.RequestsEnabled, settings.GlobalMaxRequests, settings.GlobalWindowDays, settings.GlobalAutoApprovalEnabled, settings.ForceDualQuality).
		Scan(&s.RequestsEnabled, &s.GlobalMaxRequests, &s.GlobalWindowDays, &s.GlobalAutoApprovalEnabled, &s.ForceDualQuality, &s.UpdatedAt)
	if err != nil {
		return Settings{}, fmt.Errorf("update request settings: %w", err)
	}
	return s, nil
}

func (r *Repository) GetUserLimit(ctx context.Context, userID int) (*UserLimit, error) {
	var row UserLimit
	var max, window sql.NullInt64
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, limit_mode, max_requests, window_days, approval_mode, updated_at
		FROM request_user_limits
		WHERE user_id = $1
	`, userID).Scan(&row.UserID, &row.LimitMode, &max, &window, &row.ApprovalMode, &row.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get request user limit: %w", err)
	}
	if max.Valid {
		v := int(max.Int64)
		row.MaxRequests = &v
	}
	if window.Valid {
		v := int(window.Int64)
		row.WindowDays = &v
	}
	return &row, nil
}

func (r *Repository) UpsertUserLimit(ctx context.Context, limit UserLimit) (*UserLimit, error) {
	var max, window any
	if limit.MaxRequests != nil {
		max = *limit.MaxRequests
	}
	if limit.WindowDays != nil {
		window = *limit.WindowDays
	}
	var row UserLimit
	var scannedMax, scannedWindow sql.NullInt64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO request_user_limits (
			user_id, limit_mode, max_requests, window_days, approval_mode, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (user_id) DO UPDATE SET
			limit_mode = EXCLUDED.limit_mode,
			max_requests = EXCLUDED.max_requests,
			window_days = EXCLUDED.window_days,
			approval_mode = EXCLUDED.approval_mode,
			updated_at = now()
		RETURNING user_id, limit_mode, max_requests, window_days, approval_mode, updated_at
	`, limit.UserID, limit.LimitMode, max, window, limit.ApprovalMode).
		Scan(&row.UserID, &row.LimitMode, &scannedMax, &scannedWindow, &row.ApprovalMode, &row.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upsert request user limit: %w", err)
	}
	if scannedMax.Valid {
		v := int(scannedMax.Int64)
		row.MaxRequests = &v
	}
	if scannedWindow.Valid {
		v := int(scannedWindow.Int64)
		row.WindowDays = &v
	}
	return &row, nil
}

func (r *Repository) CountUserRequestsSince(ctx context.Context, userID int, since time.Time) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_requests
		WHERE requested_by_user_id = $1
		  AND created_at >= $2
	`, userID, since).Scan(&count); err != nil {
		return 0, fmt.Errorf("count user requests: %w", err)
	}
	return count, nil
}

func (r *Repository) ListActiveByTMDB(ctx context.Context, mediaType MediaType, tmdbIDs []int) (map[int]*Request, error) {
	if len(tmdbIDs) == 0 {
		return map[int]*Request{}, nil
	}
	rows, err := r.pool.Query(ctx, requestSelectSQL()+`
		WHERE media_type = $1
		  AND provider = 'tmdb'
		  AND tmdb_id = ANY($2)
		  AND outcome = 'active'
		  AND status <> 'completed'
	`, mediaType, tmdbIDs)
	if err != nil {
		return nil, fmt.Errorf("list active requests by tmdb: %w", err)
	}
	defer rows.Close()

	out := map[int]*Request{}
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out[req.TMDBID] = req
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active requests by tmdb: %w", err)
	}
	return out, nil
}

func (r *Repository) DeleteFailedByTMDB(ctx context.Context, mediaType MediaType, tmdbID int) (int, error) {
	if tmdbID <= 0 {
		return 0, nil
	}
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM media_requests
		WHERE media_type = $1
		  AND provider = 'tmdb'
		  AND tmdb_id = $2
		  AND outcome = 'failed'
	`, mediaType, tmdbID)
	if err != nil {
		return 0, fmt.Errorf("delete failed requests by tmdb: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// quotaLockNamespace partitions advisory locks so request-quota locks do not
// collide with advisory locks held elsewhere in the database. The value is
// arbitrary; what matters is that it is stable.
const quotaLockNamespace = 139

func (r *Repository) CreateRequest(ctx context.Context, input CreateRequestRecord) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create request transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if input.Quota != nil {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int4, $2::int4)`,
			quotaLockNamespace, input.Quota.UserID); err != nil {
			return nil, fmt.Errorf("acquire request quota lock: %w", err)
		}
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM media_requests
			WHERE requested_by_user_id = $1
			  AND created_at >= $2
		`, input.Quota.UserID, input.Quota.WindowStart).Scan(&count); err != nil {
			return nil, fmt.Errorf("count requests for quota: %w", err)
		}
		if count >= input.Quota.MaxRequests {
			return nil, ErrQuotaExceeded
		}
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	status := input.Status
	if status == "" {
		status = StatusPending
	}
	outcome := input.Outcome
	if outcome == "" {
		outcome = OutcomeActive
	}

	var approvedAt any
	if status != StatusPending {
		approvedAt = now
	}

	req, err := r.insertRequest(ctx, tx, input, status, outcome, now, approvedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrAlreadyRequested
		}
		return nil, err
	}
	if err := r.recordEvent(ctx, tx, req.ID, "created", input.Requester, ""); err != nil {
		return nil, err
	}
	if status == StatusApproved {
		if err := r.recordEvent(ctx, tx, req.ID, "approved", input.Requester, "auto approved"); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create request transaction: %w", err)
	}
	return req, nil
}

type requestExecutor interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (r *Repository) insertRequest(
	ctx context.Context,
	exec requestExecutor,
	input CreateRequestRecord,
	status Status,
	outcome Outcome,
	now time.Time,
	approvedAt any,
) (*Request, error) {
	var tvdbID any
	if input.Input.TVDBID != nil {
		tvdbID = *input.Input.TVDBID
	}
	var year any
	if input.Input.Year != nil {
		year = *input.Input.Year
	}
	row := exec.QueryRow(ctx, `
		INSERT INTO media_requests (
			id, provider, media_type, tmdb_id, tvdb_id, imdb_id, title, year,
			overview, poster_path, backdrop_path, status, outcome,
			requested_by_user_id, requested_by_profile_id, is_anime, created_at, updated_at, approved_at
		)
		VALUES (
			$1, 'tmdb', $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13, $14, $15, $16, $16, $17
		)
		RETURNING `+requestColumns(), input.ID, input.Input.MediaType, input.Input.TMDBID, tvdbID,
		strings.TrimSpace(input.Input.IMDbID), strings.TrimSpace(input.Input.Title), year,
		strings.TrimSpace(input.Input.Overview), strings.TrimSpace(input.Input.PosterPath),
		strings.TrimSpace(input.Input.BackdropPath), status, outcome,
		input.Requester.UserID, input.Requester.ProfileID, input.IsAnime, now, approvedAt)
	req, err := scanRequest(row)
	if err != nil {
		return nil, fmt.Errorf("insert request: %w", err)
	}
	return req, nil
}

func (r *Repository) GetRequest(ctx context.Context, id string) (*Request, error) {
	req, err := scanRequest(r.pool.QueryRow(ctx, requestSelectSQL()+`
		WHERE id = $1
	`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return req, nil
}

func (r *Repository) ListReconciliationCandidates(ctx context.Context, limit int) ([]*Request, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, requestSelectSQL()+`
		WHERE outcome = 'active'
		  AND status IN ('approved', 'queued', 'downloading')
		ORDER BY updated_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list request reconciliation candidates: %w", err)
	}
	defer rows.Close()

	var out []*Request
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request reconciliation candidates: %w", err)
	}
	return out, nil
}

func (r *Repository) ListMine(ctx context.Context, userID int, filter ListFilter) ([]*Request, error) {
	sqlText, args := buildRequestListSQL("requested_by_user_id = $1", []any{userID}, filter)
	return r.listRequests(ctx, sqlText, args)
}

func (r *Repository) ListAdmin(ctx context.Context, filter ListFilter) ([]*Request, error) {
	sqlText, args := buildRequestListSQL("true", nil, filter)
	return r.listRequests(ctx, sqlText, args)
}

func (r *Repository) listRequests(ctx context.Context, sqlText string, args []any) ([]*Request, error) {
	rows, err := r.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("list requests: %w", err)
	}
	defer rows.Close()
	var out []*Request
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate requests: %w", err)
	}
	return out, nil
}

func (r *Repository) SetStatus(ctx context.Context, id string, status Status, actor Viewer) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request status transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	req, err := scanRequest(tx.QueryRow(ctx, `
		UPDATE media_requests
		SET status = $2,
		    updated_at = now(),
		    approved_at = CASE WHEN $2 = 'approved' AND approved_at IS NULL THEN now() ELSE approved_at END,
		    completed_at = CASE WHEN $2 = 'completed' AND completed_at IS NULL THEN now() ELSE completed_at END
		WHERE id = $1
		RETURNING `+requestColumns(), id, status))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("set request status: %w", err)
	}
	if err := r.recordEvent(ctx, tx, id, "status_"+string(status), actor, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request status transaction: %w", err)
	}
	return req, nil
}

func (r *Repository) SetOutcome(ctx context.Context, id string, outcome Outcome, actor Viewer, message string) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request outcome transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	req, err := scanRequest(tx.QueryRow(ctx, `
		UPDATE media_requests
		SET outcome = $2,
		    last_error = CASE
		      WHEN $2 = 'failed' THEN $3
		      WHEN $2 = 'active' THEN ''
		      ELSE last_error
		    END,
		    updated_at = now()
		WHERE id = $1
		RETURNING `+requestColumns(), id, outcome, strings.TrimSpace(message)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("set request outcome: %w", err)
	}
	if err := r.recordEvent(ctx, tx, id, "outcome_"+string(outcome), actor, message); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request outcome transaction: %w", err)
	}
	return req, nil
}

const integrationColumns = `id, kind, name, enabled, base_url, api_key_ref,
	root_folder, quality_profile_id, tags, is_4k, is_default, is_default_4k,
	anime_enabled, anime_quality_profile_id, anime_root_folder, anime_tags,
	options, last_check_at, last_check_status, last_check_error, updated_at`

func (r *Repository) ListIntegrations(ctx context.Context) ([]Integration, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+integrationColumns+` FROM request_integrations ORDER BY kind, name`)
	if err != nil {
		return nil, fmt.Errorf("list request integrations: %w", err)
	}
	defer rows.Close()

	var out []Integration
	for rows.Next() {
		integration, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, integration)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request integrations: %w", err)
	}
	return out, nil
}

func (r *Repository) GetIntegration(ctx context.Context, id string) (*Integration, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+integrationColumns+
		` FROM request_integrations WHERE id = $1`, id)
	i, err := scanIntegration(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get request integration: %w", err)
	}
	return &i, nil
}

func (r *Repository) CreateIntegration(ctx context.Context, i Integration) (*Integration, error) {
	return r.insertIntegration(ctx, r.pool, i)
}

func (r *Repository) UpdateIntegration(ctx context.Context, i Integration) (*Integration, error) {
	return r.updateIntegration(ctx, r.pool, i)
}

// insertIntegration runs the integration INSERT against any executor (pool or
// tx) so the same SQL is reused by the plain create path and the transactional
// SaveIntegrationWithDefaults path.
func (r *Repository) insertIntegration(ctx context.Context, exec requestExecutor, i Integration) (*Integration, error) {
	if i.Options == nil {
		i.Options = map[string]any{}
	}
	options, err := json.Marshal(i.Options)
	if err != nil {
		return nil, fmt.Errorf("marshal options: %w", err)
	}
	row := exec.QueryRow(ctx, `
		INSERT INTO request_integrations (
			id, kind, name, enabled, base_url, api_key_ref, root_folder,
			quality_profile_id, tags, is_4k, is_default, is_default_4k,
			anime_enabled, anime_quality_profile_id, anime_root_folder, anime_tags,
			options, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17, now())
		RETURNING `+integrationColumns,
		i.ID, i.Kind, strings.TrimSpace(i.Name), i.Enabled, strings.TrimSpace(i.BaseURL),
		strings.TrimSpace(i.APIKeyRef), strings.TrimSpace(i.RootFolder), i.QualityProfileID,
		int32Slice(i.Tags), i.Is4K, i.IsDefault, i.IsDefault4K, i.AnimeEnabled,
		i.AnimeQualityProfileID, strings.TrimSpace(i.AnimeRootFolder), int32Slice(i.AnimeTags),
		options)
	out, err := scanIntegration(row)
	if err != nil {
		return nil, fmt.Errorf("create request integration: %w", err)
	}
	return &out, nil
}

// updateIntegration runs the integration UPDATE against any executor (pool or
// tx) so the plain update path and SaveIntegrationWithDefaults share the SQL.
func (r *Repository) updateIntegration(ctx context.Context, exec requestExecutor, i Integration) (*Integration, error) {
	if i.Options == nil {
		i.Options = map[string]any{}
	}
	options, err := json.Marshal(i.Options)
	if err != nil {
		return nil, fmt.Errorf("marshal options: %w", err)
	}
	row := exec.QueryRow(ctx, `
		UPDATE request_integrations SET
			name=$2, enabled=$3, base_url=$4,
			api_key_ref = CASE WHEN $5 = '' THEN api_key_ref ELSE $5 END,
			root_folder=$6, quality_profile_id=$7, tags=$8, is_4k=$9,
			is_default=$10, is_default_4k=$11, anime_enabled=$12,
			anime_quality_profile_id=$13, anime_root_folder=$14, anime_tags=$15,
			options=$16, updated_at=now()
		WHERE id=$1
		RETURNING `+integrationColumns,
		i.ID, strings.TrimSpace(i.Name), i.Enabled, strings.TrimSpace(i.BaseURL),
		strings.TrimSpace(i.APIKeyRef), strings.TrimSpace(i.RootFolder), i.QualityProfileID,
		int32Slice(i.Tags), i.Is4K, i.IsDefault, i.IsDefault4K, i.AnimeEnabled,
		i.AnimeQualityProfileID, strings.TrimSpace(i.AnimeRootFolder), int32Slice(i.AnimeTags),
		options)
	out, err := scanIntegration(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update request integration: %w", err)
	}
	return &out, nil
}

// SaveIntegrationWithDefaults clears the conflicting kind default(s) and
// creates/updates the instance in a single transaction so a save failure can
// never leave the kind with zero defaults.
func (r *Repository) SaveIntegrationWithDefaults(ctx context.Context, in Integration, isCreate bool) (*Integration, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin save integration: %w", err)
	}
	defer tx.Rollback(ctx)

	if in.IsDefault {
		if err := r.ClearDefault(ctx, tx, in.Kind, false, in.ID); err != nil {
			return nil, err
		}
	}
	if in.IsDefault4K {
		if err := r.ClearDefault(ctx, tx, in.Kind, true, in.ID); err != nil {
			return nil, err
		}
	}

	var out *Integration
	if isCreate {
		out, err = r.insertIntegration(ctx, tx, in)
	} else {
		out, err = r.updateIntegration(ctx, tx, in)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit save integration: %w", err)
	}
	return out, nil
}

func (r *Repository) DeleteIntegration(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete integration: %w", err)
	}
	defer tx.Rollback(ctx)

	var lockedID string
	if err := tx.QueryRow(ctx, `
		SELECT id FROM request_integrations WHERE id = $1 FOR UPDATE
	`, id).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock request integration: %w", err)
	}

	var hasLiveTargets bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM media_request_targets
			WHERE integration_id = $1 AND status IN ('queued', 'downloading')
		)
	`, id).Scan(&hasLiveTargets); err != nil {
		return fmt.Errorf("check integration targets: %w", err)
	}
	if hasLiveTargets {
		return ErrInvalidState
	}

	if _, err := tx.Exec(ctx, `DELETE FROM request_integrations WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete request integration: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete integration: %w", err)
	}
	return nil
}

// ClearDefault unsets the HD (or 4K) default flag for every instance of a kind
// except excludeID, so saving an instance that is itself the new default does
// not clear its own freshly-written flag.
func (r *Repository) ClearDefault(ctx context.Context, exec requestExecutor, kind string, fourK bool, excludeID string) error {
	col := "is_default"
	if fourK {
		col = "is_default_4k"
	}
	_, err := exec.Exec(ctx, `UPDATE request_integrations SET `+col+` = false WHERE kind = $1 AND id <> $2`, kind, excludeID)
	if err != nil {
		return fmt.Errorf("clear default: %w", err)
	}
	return nil
}

func (r *Repository) recordEvent(ctx context.Context, exec requestExecutor, requestID, eventType string, actor Viewer, message string) error {
	var actorUserID any
	if actor.UserID > 0 {
		actorUserID = actor.UserID
	}
	_, err := exec.Exec(ctx, `
		INSERT INTO media_request_events (
			request_id, event_type, actor_user_id, actor_profile_id, message
		)
		VALUES ($1, $2, $3, $4, $5)
	`, requestID, eventType, actorUserID, actor.ProfileID, strings.TrimSpace(message))
	if err != nil {
		return fmt.Errorf("record request event: %w", err)
	}
	return nil
}

func buildRequestListSQL(baseCondition string, baseArgs []any, filter ListFilter) (string, []any) {
	args := append([]any(nil), baseArgs...)
	conditions := []string{baseCondition}
	if filter.Status != "" {
		args = append(args, filter.Status)
		conditions = append(conditions, "status = $"+strconv.Itoa(len(args)))
	}
	if filter.Outcome != "" {
		args = append(args, filter.Outcome)
		conditions = append(conditions, "outcome = $"+strconv.Itoa(len(args)))
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	return requestSelectSQL() + `
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY created_at DESC
		LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args)), args
}

func requestSelectSQL() string {
	return "SELECT " + requestColumns() + " FROM media_requests "
}

func requestColumns() string {
	return `id, provider, media_type, tmdb_id, tvdb_id, imdb_id, title, year,
	        overview, poster_path, backdrop_path, status, outcome,
	        requested_by_user_id, requested_by_profile_id, is_anime,
	        last_error, created_at, updated_at, approved_at, completed_at`
}

type requestScanner interface {
	Scan(dest ...any) error
}

func scanRequest(row requestScanner) (*Request, error) {
	var req Request
	var tvdbID, year sql.NullInt64
	var approvedAt, completedAt sql.NullTime
	if err := row.Scan(
		&req.ID,
		&req.Provider,
		&req.MediaType,
		&req.TMDBID,
		&tvdbID,
		&req.IMDbID,
		&req.Title,
		&year,
		&req.Overview,
		&req.PosterPath,
		&req.BackdropPath,
		&req.Status,
		&req.Outcome,
		&req.RequestedByUserID,
		&req.RequestedByProfileID,
		&req.IsAnime,
		&req.LastError,
		&req.CreatedAt,
		&req.UpdatedAt,
		&approvedAt,
		&completedAt,
	); err != nil {
		return nil, err
	}
	if tvdbID.Valid {
		v := int(tvdbID.Int64)
		req.TVDBID = &v
	}
	if year.Valid {
		v := int(year.Int64)
		req.Year = &v
	}
	if approvedAt.Valid {
		req.ApprovedAt = &approvedAt.Time
	}
	if completedAt.Valid {
		req.CompletedAt = &completedAt.Time
	}
	return &req, nil
}

type integrationScanner interface {
	Scan(dest ...any) error
}

func scanIntegration(row integrationScanner) (Integration, error) {
	var i Integration
	var quality, animeQuality sql.NullInt64
	var tags, animeTags []int32
	var optionsRaw []byte
	var lastCheckAt sql.NullTime
	if err := row.Scan(
		&i.ID, &i.Kind, &i.Name, &i.Enabled, &i.BaseURL, &i.APIKeyRef,
		&i.RootFolder, &quality, &tags, &i.Is4K, &i.IsDefault, &i.IsDefault4K,
		&i.AnimeEnabled, &animeQuality, &i.AnimeRootFolder, &animeTags,
		&optionsRaw, &lastCheckAt, &i.LastCheckStatus, &i.LastCheckError, &i.UpdatedAt,
	); err != nil {
		return Integration{}, err
	}
	if quality.Valid {
		v := int(quality.Int64)
		i.QualityProfileID = &v
	}
	if animeQuality.Valid {
		v := int(animeQuality.Int64)
		i.AnimeQualityProfileID = &v
	}
	i.Tags = intsFromInt32(tags)
	i.AnimeTags = intsFromInt32(animeTags)
	if len(optionsRaw) > 0 {
		if err := json.Unmarshal(optionsRaw, &i.Options); err != nil {
			return Integration{}, fmt.Errorf("unmarshal request integration options for %s: %w", i.ID, err)
		}
	}
	if i.Options == nil {
		i.Options = map[string]any{}
	}
	if lastCheckAt.Valid {
		i.LastCheckAt = &lastCheckAt.Time
	}
	return i, nil
}

func int32Slice(values []int) []int32 {
	out := make([]int32, 0, len(values))
	for _, value := range values {
		out = append(out, int32(value))
	}
	return out
}

func intsFromInt32(values []int32) []int {
	out := make([]int, 0, len(values))
	for _, value := range values {
		out = append(out, int(value))
	}
	return out
}
