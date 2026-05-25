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
		       global_auto_approval_enabled, updated_at
		FROM request_settings
		WHERE id = true
	`).Scan(&s.RequestsEnabled, &s.GlobalMaxRequests, &s.GlobalWindowDays, &s.GlobalAutoApprovalEnabled, &s.UpdatedAt)
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
			global_auto_approval_enabled, updated_at
		)
		VALUES (true, $1, $2, $3, $4, now())
		ON CONFLICT (id) DO UPDATE SET
			requests_enabled = EXCLUDED.requests_enabled,
			global_max_requests = EXCLUDED.global_max_requests,
			global_window_days = EXCLUDED.global_window_days,
			global_auto_approval_enabled = EXCLUDED.global_auto_approval_enabled,
			updated_at = now()
		RETURNING requests_enabled, global_max_requests, global_window_days,
		          global_auto_approval_enabled, updated_at
	`, settings.RequestsEnabled, settings.GlobalMaxRequests, settings.GlobalWindowDays, settings.GlobalAutoApprovalEnabled).
		Scan(&s.RequestsEnabled, &s.GlobalMaxRequests, &s.GlobalWindowDays, &s.GlobalAutoApprovalEnabled, &s.UpdatedAt)
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
			requested_by_user_id, requested_by_profile_id, created_at, updated_at, approved_at
		)
		VALUES (
			$1, 'tmdb', $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13, $14, $15, $15, $16
		)
		RETURNING `+requestColumns(), input.ID, input.Input.MediaType, input.Input.TMDBID, tvdbID,
		strings.TrimSpace(input.Input.IMDbID), strings.TrimSpace(input.Input.Title), year,
		strings.TrimSpace(input.Input.Overview), strings.TrimSpace(input.Input.PosterPath),
		strings.TrimSpace(input.Input.BackdropPath), status, outcome,
		input.Requester.UserID, input.Requester.ProfileID, now, approvedAt)
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

func (r *Repository) MarkQueued(ctx context.Context, id string, update QueueUpdate, actor Viewer) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request queue transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	externalStatus := strings.TrimSpace(update.ExternalStatus)
	if externalStatus == "" {
		externalStatus = "queued"
	}
	req, err := scanRequest(tx.QueryRow(ctx, `
		UPDATE media_requests
		SET status = 'queued',
		    outcome = 'active',
		    integration_kind = $2,
		    external_id = $3,
		    external_status = $4,
		    last_error = '',
		    updated_at = now(),
		    approved_at = CASE WHEN approved_at IS NULL THEN now() ELSE approved_at END
		WHERE id = $1
		RETURNING `+requestColumns(), id,
		strings.TrimSpace(update.IntegrationKind),
		strings.TrimSpace(update.ExternalID),
		externalStatus,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("mark request queued: %w", err)
	}
	if err := r.recordEvent(ctx, tx, id, "status_queued", actor, externalStatus); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request queue transaction: %w", err)
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

func (r *Repository) ListIntegrations(ctx context.Context) ([]Integration, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT kind, enabled, base_url, api_key_ref, root_folder, quality_profile_id,
		       tags, options, last_check_at, last_check_status, last_check_error, updated_at
		FROM request_integrations
		ORDER BY kind
	`)
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

func (r *Repository) UpsertIntegration(ctx context.Context, integration Integration) (*Integration, error) {
	out, err := r.upsertIntegration(ctx, r.pool, integration)
	if err != nil {
		return nil, fmt.Errorf("upsert request integration: %w", err)
	}
	return out, nil
}

func (r *Repository) UpsertIntegrations(ctx context.Context, integrations []Integration) ([]Integration, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request integrations transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	out := make([]Integration, 0, len(integrations))
	for _, integration := range integrations {
		updated, err := r.upsertIntegration(ctx, tx, integration)
		if err != nil {
			return nil, err
		}
		out = append(out, *updated)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request integrations transaction: %w", err)
	}
	return out, nil
}

func (r *Repository) upsertIntegration(ctx context.Context, exec requestExecutor, integration Integration) (*Integration, error) {
	if integration.Options == nil {
		integration.Options = map[string]any{}
	}
	options, err := json.Marshal(integration.Options)
	if err != nil {
		return nil, fmt.Errorf("marshal request integration options: %w", err)
	}
	tags := int32Slice(integration.Tags)
	row := exec.QueryRow(ctx, `
		INSERT INTO request_integrations (
			kind, enabled, base_url, api_key_ref, root_folder, quality_profile_id,
			tags, options, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (kind) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			base_url = EXCLUDED.base_url,
			api_key_ref = CASE
				WHEN EXCLUDED.api_key_ref = '' THEN request_integrations.api_key_ref
				ELSE EXCLUDED.api_key_ref
			END,
			root_folder = EXCLUDED.root_folder,
			quality_profile_id = EXCLUDED.quality_profile_id,
			tags = EXCLUDED.tags,
			options = EXCLUDED.options,
			updated_at = now()
		RETURNING kind, enabled, base_url, api_key_ref, root_folder, quality_profile_id,
		          tags, options, last_check_at, last_check_status, last_check_error, updated_at
	`, integration.Kind, integration.Enabled, strings.TrimSpace(integration.BaseURL),
		strings.TrimSpace(integration.APIKeyRef), strings.TrimSpace(integration.RootFolder),
		integration.QualityProfileID, tags, options)
	out, err := scanIntegration(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
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
	        requested_by_user_id, requested_by_profile_id, integration_kind,
	        external_id, external_status, last_error, created_at, updated_at,
	        approved_at, completed_at`
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
		&req.IntegrationKind,
		&req.ExternalID,
		&req.ExternalStatus,
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
	var integration Integration
	var quality sql.NullInt64
	var tags []int32
	var optionsRaw []byte
	var lastCheckAt sql.NullTime
	if err := row.Scan(
		&integration.Kind,
		&integration.Enabled,
		&integration.BaseURL,
		&integration.APIKeyRef,
		&integration.RootFolder,
		&quality,
		&tags,
		&optionsRaw,
		&lastCheckAt,
		&integration.LastCheckStatus,
		&integration.LastCheckError,
		&integration.UpdatedAt,
	); err != nil {
		return Integration{}, err
	}
	if quality.Valid {
		v := int(quality.Int64)
		integration.QualityProfileID = &v
	}
	integration.Tags = intsFromInt32(tags)
	if len(optionsRaw) > 0 {
		if err := json.Unmarshal(optionsRaw, &integration.Options); err != nil {
			return Integration{}, fmt.Errorf("unmarshal request integration options for %s: %w", integration.Kind, err)
		}
	}
	if integration.Options == nil {
		integration.Options = map[string]any{}
	}
	if lastCheckAt.Valid {
		integration.LastCheckAt = &lastCheckAt.Time
	}
	return integration, nil
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
