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

	"github.com/Silo-Server/silo-server/internal/secret"
)

type Repository struct {
	pool   *pgxpool.Pool
	cipher *secret.Cipher
}

func NewRepository(pool *pgxpool.Pool, cipher *secret.Cipher) *Repository {
	return &Repository{pool: pool, cipher: cipher}
}

func apiKeyAAD(id string) string {
	return secret.RowAAD("request_integrations", "api_key_ref", id)
}

func (r *Repository) encryptAPIKey(id, apiKey string) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", nil
	}
	return r.cipher.Encrypt(apiKey, apiKeyAAD(id))
}

func (r *Repository) GetSettings(ctx context.Context) (Settings, error) {
	var s Settings
	err := r.pool.QueryRow(ctx, `
		SELECT requests_enabled, global_max_requests, global_window_days,
		       global_auto_approval_enabled, force_dual_quality, updated_at, revision
		FROM request_settings
		WHERE id = true
	`).Scan(&s.RequestsEnabled, &s.GlobalMaxRequests, &s.GlobalWindowDays, &s.GlobalAutoApprovalEnabled, &s.ForceDualQuality, &s.UpdatedAt, &s.Revision)
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
	return r.updateSettings(ctx, r.pool, settings, -1)
}

func (r *Repository) updateSettings(ctx context.Context, exec requestExecutor, settings Settings, expected int64) (Settings, error) {
	if settings.GlobalWindowDays <= 0 {
		settings.GlobalWindowDays = 7
	}
	if settings.GlobalMaxRequests < 0 {
		settings.GlobalMaxRequests = 0
	}

	var s Settings
	err := exec.QueryRow(ctx, `
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
		WHERE $6::bigint = -1 OR request_settings.revision = $6
		RETURNING requests_enabled, global_max_requests, global_window_days,
		          global_auto_approval_enabled, force_dual_quality, updated_at, revision
	`, settings.RequestsEnabled, settings.GlobalMaxRequests, settings.GlobalWindowDays, settings.GlobalAutoApprovalEnabled, settings.ForceDualQuality, expected).
		Scan(&s.RequestsEnabled, &s.GlobalMaxRequests, &s.GlobalWindowDays, &s.GlobalAutoApprovalEnabled, &s.ForceDualQuality, &s.UpdatedAt, &s.Revision)
	if err != nil {
		return Settings{}, fmt.Errorf("update request settings: %w", err)
	}
	return s, nil
}

func (r *Repository) GetUserLimit(ctx context.Context, userID int) (*UserLimit, error) {
	var row UserLimit
	var max, window sql.NullInt64
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, limit_mode, max_requests, window_days, approval_mode, updated_at, revision
		FROM request_user_limits
		WHERE user_id = $1
	`, userID).Scan(&row.UserID, &row.LimitMode, &max, &window, &row.ApprovalMode, &row.UpdatedAt, &row.Revision)
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
	return r.upsertUserLimit(ctx, r.pool, limit, -1)
}

func (r *Repository) upsertUserLimit(ctx context.Context, exec requestExecutor, limit UserLimit, expected int64) (*UserLimit, error) {
	var max, window any
	if limit.MaxRequests != nil {
		max = *limit.MaxRequests
	}
	if limit.WindowDays != nil {
		window = *limit.WindowDays
	}
	var row UserLimit
	var scannedMax, scannedWindow sql.NullInt64
	err := exec.QueryRow(ctx, `
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
		WHERE $6::bigint = -1 OR request_user_limits.revision = $6
		RETURNING user_id, limit_mode, max_requests, window_days, approval_mode, updated_at, revision
	`, limit.UserID, limit.LimitMode, max, window, limit.ApprovalMode, expected).
		Scan(&row.UserID, &row.LimitMode, &scannedMax, &scannedWindow, &row.ApprovalMode, &row.UpdatedAt, &row.Revision)
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

// quotaOutcomes are the outcomes whose requests count against the quota. A
// decline or a failure gives the slot back; a withdrawal does not, or a
// request-and-withdraw loop could repeat without limit. A failed request an
// admin closes keeps its submission error (SetOutcome clears it on every
// other cancel) and the refund its failure gave it: cleaning up the failed
// view must not use up the requester's quota. A request withdrawn while it
// backs off after a failed attempt was never failed, so it still counts.
const quotaOutcomes = `(outcome = 'active' OR (outcome = 'cancelled' AND last_error = ''))`

// CountUserRequestsSince counts the requests an account made since a time
// that count against its quota.
func (r *Repository) CountUserRequestsSince(ctx context.Context, userID int, since time.Time) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_requests
		WHERE requested_by_user_id = $1
		  AND created_at >= $2
		  AND `+quotaOutcomes, userID, since).Scan(&count); err != nil {
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
	}
	if input.Quota != nil {
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM media_requests
			WHERE requested_by_user_id = $1
			  AND created_at >= $2
			  AND `+quotaOutcomes, input.Quota.UserID, input.Quota.WindowStart).Scan(&count); err != nil {
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
	if err := adoptTitleFollows(ctx, tx, req); err != nil {
		return nil, err
	}
	// Failed requests do not count against the quota, so the ones this
	// replaces go after the insert, once their follows have moved to it.
	if input.ReplaceFailed {
		// Only the requester's own rows: other accounts' failed requests for
		// the title are their history.
		if _, err := tx.Exec(ctx, `
			DELETE FROM media_requests
			WHERE requested_by_user_id = $1
			  AND media_type = $2
			  AND provider = 'tmdb'
			  AND tmdb_id = $3
			  AND outcome = 'failed'
		`, input.Requester.UserID, input.Input.MediaType, input.Input.TMDBID); err != nil {
			return nil, fmt.Errorf("replace failed requests: %w", err)
		}
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
	facts, err := encodeRoutingFacts(input.Facts)
	if err != nil {
		return nil, err
	}
	row := exec.QueryRow(ctx, `
		INSERT INTO media_requests (
			id, provider, media_type, tmdb_id, tvdb_id, imdb_id, title, year,
			overview, poster_path, backdrop_path, status, outcome,
			requested_by_user_id, requested_by_profile_id, is_anime, created_at, updated_at, approved_at,
			routing_facts, seasons
		)
		VALUES (
			$1, 'tmdb', $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13, $14, $15, $16, $16, $17,
			$18, $19
		)
		RETURNING `+requestColumns(), input.ID, input.Input.MediaType, input.Input.TMDBID, tvdbID,
		strings.TrimSpace(input.Input.IMDbID), strings.TrimSpace(input.Input.Title), year,
		strings.TrimSpace(input.Input.Overview), strings.TrimSpace(input.Input.PosterPath),
		strings.TrimSpace(input.Input.BackdropPath), status, outcome,
		input.Requester.UserID, input.Requester.ProfileID, input.IsAnime, now, approvedAt,
		facts, nonNilSeasons(input.Input.Seasons))
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
		ORDER BY last_reconciled_at ASC NULLS FIRST, id
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

// ListDownloadingRequests returns active requests with a downloading target
// that has download progress, the one asked about longest ago first: a
// request is as due as its most overdue such target. A target counts as asked
// whether or not its server answered, so one that stops answering takes its
// turn and moves to the back instead of heading every batch. A downloading
// target without progress is not listed, however many there are, so targets
// whose plugin never reports any (or has nothing queued) cannot crowd the
// batch; the reconcile pass records a download's first progress.
func (r *Repository) ListDownloadingRequests(ctx context.Context, limit int) ([]*Request, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, requestSelectSQL()+`
		JOIN (
			SELECT request_id, min(download_checked_at) AS checked_at
			FROM media_request_targets
			WHERE status = 'downloading' AND download_phase IS NOT NULL
			GROUP BY request_id
		) downloading ON downloading.request_id = media_requests.id
		WHERE outcome = 'active'
		ORDER BY downloading.checked_at NULLS FIRST, media_requests.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list downloading requests: %w", err)
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
		return nil, fmt.Errorf("iterate downloading requests: %w", err)
	}
	return out, nil
}

// ListLibraryWaitCandidates returns the requests that only the library can
// complete: pending ones, and ones that failed in the last 30 days without
// delivering anything. Older failures are left alone so an upgrade does not
// suddenly complete, and notify, a backlog of stale requests.
func (r *Repository) ListLibraryWaitCandidates(ctx context.Context, limit int) ([]*Request, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, requestSelectSQL()+`
		WHERE (outcome = 'active' AND status = 'pending')
		   OR (outcome = 'failed'
		       AND updated_at > now() - interval '30 days'
		       AND NOT EXISTS (
		         SELECT 1 FROM media_request_targets t
		         WHERE t.request_id = media_requests.id AND t.status = 'completed'))
		ORDER BY last_reconciled_at ASC NULLS FIRST, id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list requests waiting for the library: %w", err)
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
		return nil, fmt.Errorf("iterate requests waiting for the library: %w", err)
	}
	return out, nil
}

// ListFulfilledUnnotified returns completed requests whose fulfillment
// notification has not fired, oldest first. The horizon bounds how long a
// completed request keeps being presence-polled when its media never appears
// in the catalog (requests completed before the feature shipped are stamped
// by the migration backfill, so the horizon is defense in depth).
func (r *Repository) ListFulfilledUnnotified(ctx context.Context, limit int) ([]*Request, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, requestSelectSQL()+`
		WHERE outcome = 'active'
		  AND status = 'completed'
		  AND fulfilled_notified_at IS NULL
		  AND completed_at > now() - interval '30 days'
		-- A request still waiting on the library is stamped each pass, so it
		-- moves behind the others and cannot starve newer completions.
		ORDER BY last_reconciled_at ASC NULLS FIRST, completed_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list fulfilled unnotified requests: %w", err)
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
		return nil, fmt.Errorf("iterate fulfilled unnotified requests: %w", err)
	}
	return out, nil
}

// MarkFulfilledNotified stamps the fulfillment-notification marker. Idempotent;
// stamped reports whether this call set it.
func (r *Repository) MarkFulfilledNotified(ctx context.Context, id string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_requests SET fulfilled_notified_at = now()
		WHERE id = $1 AND fulfilled_notified_at IS NULL`, id)
	if err != nil {
		return false, fmt.Errorf("mark request fulfill-notified: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) SetExternalIDs(ctx context.Context, id string, tvdbID int, imdbID string) (int, error) {
	var saved int
	err := r.pool.QueryRow(ctx, `
		UPDATE media_requests
		SET tvdb_id = CASE WHEN tvdb_id IS NULL OR tvdb_id <= 0 THEN $2 ELSE tvdb_id END,
		    imdb_id = CASE WHEN imdb_id = '' THEN $3 ELSE imdb_id END,
		    updated_at = now()
		WHERE id = $1
		RETURNING tvdb_id`, id, tvdbID, strings.TrimSpace(imdbID)).Scan(&saved)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("set request external ids: %w", err)
	}
	return saved, nil
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

// guardCondition restricts an UPDATE to rows a StateGuard accepts. $2 and $3
// carry the guard's statuses and outcomes (an empty array accepts any value),
// and $4 its UnsentOnly flag.
const guardCondition = `(cardinality($2::text[]) = 0 OR status = ANY($2::text[]))
		  AND (cardinality($3::text[]) = 0 OR outcome = ANY($3::text[]))
		  AND (NOT $4::boolean OR status <> 'approved' OR (
		    (submit_lease_until IS NULL OR submit_lease_until <= now())
		    AND NOT EXISTS (SELECT 1 FROM media_request_targets t WHERE t.request_id = media_requests.id)))`

func guardArgs(g StateGuard) ([]string, []string, bool) {
	statuses := make([]string, 0, len(g.Statuses))
	for _, s := range g.Statuses {
		statuses = append(statuses, string(s))
	}
	outcomes := make([]string, 0, len(g.Outcomes))
	for _, o := range g.Outcomes {
		outcomes = append(outcomes, string(o))
	}
	return statuses, outcomes, g.UnsentOnly
}

// guardMiss explains a guarded UPDATE that matched no row: the request is
// gone, or it has moved past the states the guard accepts.
func guardMiss(ctx context.Context, exec requestExecutor, id string) error {
	var exists bool
	if err := exec.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM media_requests WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("check request existence: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return ErrInvalidState
}

func (r *Repository) SetStatus(ctx context.Context, id string, from StateGuard, status Status, actor Viewer) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request status transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	statuses, outcomes, unsent := guardArgs(from)
	// A fresh approval starts a fresh submission budget.
	req, err := scanRequest(tx.QueryRow(ctx, `
		UPDATE media_requests
		SET status = $5,
		    updated_at = now(),
		    approved_at = CASE WHEN $5 = 'approved' AND approved_at IS NULL THEN now() ELSE approved_at END,
		    completed_at = CASE WHEN $5 = 'completed' AND completed_at IS NULL THEN now() ELSE completed_at END,
		    submit_attempts = CASE WHEN $5 = 'approved' THEN 0 ELSE submit_attempts END,
		    submit_lease_until = CASE WHEN $5 = 'approved' THEN NULL ELSE submit_lease_until END,
		    next_submit_at = CASE WHEN $5 = 'approved' THEN NULL ELSE next_submit_at END
		WHERE id = $1
		  AND `+guardCondition+`
		RETURNING `+requestColumns(), id, statuses, outcomes, unsent, status))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, guardMiss(ctx, tx, id)
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

func (r *Repository) SetOutcome(ctx context.Context, id string, from StateGuard, outcome Outcome, actor Viewer, message string) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request outcome transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	statuses, outcomes, unsent := guardArgs(from)
	req, err := scanRequest(tx.QueryRow(ctx, `
		UPDATE media_requests
		SET outcome = $5,
		    last_error = CASE
		      WHEN $5 = 'failed' THEN $6
		      WHEN $5 = 'active' THEN ''
		      -- Only a failed request keeps its error when closed; the quota
		      -- refunds exactly those (see quotaOutcomes).
		      WHEN $5 = 'cancelled' AND outcome <> 'failed' THEN ''
		      ELSE last_error
		    END,
		    outcome_reason = CASE
		      WHEN $5 IN ('declined', 'cancelled') THEN $6
		      WHEN $5 = 'active' THEN ''
		      ELSE outcome_reason
		    END,
		    updated_at = now()
		WHERE id = $1
		  AND `+guardCondition+`
		RETURNING `+requestColumns(), id, statuses, outcomes, unsent, outcome, strings.TrimSpace(message)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, guardMiss(ctx, tx, id)
		}
		return nil, fmt.Errorf("set request outcome: %w", err)
	}
	// A declined or withdrawn title is no longer on its way, so its follows
	// go in the same transaction. Once this commits, another request for the
	// title can open and gather followers, and a cleanup run after the commit
	// would remove theirs.
	if outcome == OutcomeDeclined || outcome == OutcomeCancelled {
		if err := forgetTitleFollows(ctx, tx, req); err != nil {
			return nil, err
		}
	}
	if err := r.recordEvent(ctx, tx, id, "outcome_"+string(outcome), actor, message); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request outcome transaction: %w", err)
	}
	return req, nil
}

func (r *Repository) ReopenFailed(ctx context.Context, id string, actor Viewer) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request reopen transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	req, err := scanRequest(tx.QueryRow(ctx, `
		UPDATE media_requests
		SET outcome = 'active',
		    status = 'approved',
		    last_error = '',
		    submit_attempts = 0,
		    submit_lease_until = NULL,
		    next_submit_at = NULL,
		    approved_at = COALESCE(approved_at, now()),
		    updated_at = now()
		WHERE id = $1
		  AND outcome = 'failed'
		RETURNING `+requestColumns(), id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, guardMiss(ctx, tx, id)
		}
		// Another account requested the title after this one failed, and only
		// one active request per title may exist.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrAlreadyRequested
		}
		return nil, fmt.Errorf("reopen failed request: %w", err)
	}
	// A request for the title created after this one failed may have taken
	// its follows and failed too; they come back with the retry.
	if err := adoptTitleFollows(ctx, tx, req); err != nil {
		return nil, err
	}
	if err := r.recordEvent(ctx, tx, id, "retried", actor, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request reopen transaction: %w", err)
	}
	return req, nil
}

func (r *Repository) ClaimSubmission(ctx context.Context, id string, lease time.Duration) (*Request, bool, error) {
	req, err := scanRequest(r.pool.QueryRow(ctx, `
		UPDATE media_requests
		SET submit_attempts = submit_attempts + 1,
		    submit_lease_until = now() + make_interval(secs => $2)
		WHERE id = $1
		  AND status = 'approved'
		  AND outcome = 'active'
		  AND (submit_lease_until IS NULL OR submit_lease_until <= now())
		  AND (next_submit_at IS NULL OR next_submit_at <= now())
		RETURNING `+requestColumns(), id, lease.Seconds()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("claim request submission: %w", err)
	}
	return req, true, nil
}

func (r *Repository) DeferSubmission(ctx context.Context, id string, leaseUntil time.Time, delay time.Duration, message string) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request defer transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	message = strings.TrimSpace(message)
	req, err := scanRequest(tx.QueryRow(ctx, `
		UPDATE media_requests
		SET next_submit_at = now() + make_interval(secs => $2),
		    submit_lease_until = NULL,
		    last_error = $3,
		    updated_at = now()
		WHERE id = $1
		  AND status = 'approved'
		  AND outcome = 'active'
		  AND submit_lease_until = $4
		RETURNING `+requestColumns(), id, delay.Seconds(), message, leaseUntil))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, guardMiss(ctx, tx, id)
		}
		return nil, fmt.Errorf("defer request submission: %w", err)
	}
	if err := r.recordEvent(ctx, tx, id, "submit_deferred", Viewer{}, message); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request defer transaction: %w", err)
	}
	return req, nil
}

func (r *Repository) FailSubmission(ctx context.Context, id string, leaseUntil time.Time, actor Viewer, message string) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request fail transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	message = strings.TrimSpace(message)
	req, err := scanRequest(tx.QueryRow(ctx, `
		UPDATE media_requests
		SET outcome = 'failed',
		    last_error = $2,
		    submit_lease_until = NULL,
		    updated_at = now()
		WHERE id = $1
		  AND status = 'approved'
		  AND outcome = 'active'
		  AND submit_lease_until = $3
		RETURNING `+requestColumns(), id, message, leaseUntil))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, guardMiss(ctx, tx, id)
		}
		return nil, fmt.Errorf("fail request submission: %w", err)
	}
	if err := r.recordEvent(ctx, tx, id, "outcome_"+string(OutcomeFailed), actor, message); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request fail transaction: %w", err)
	}
	return req, nil
}

func (r *Repository) MarkAvailable(ctx context.Context, id string, actor Viewer) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request available transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	req, err := scanRequest(tx.QueryRow(ctx, `
		UPDATE media_requests
		SET status = 'completed',
		    outcome = 'active',
		    last_error = '',
		    completed_at = COALESCE(completed_at, now()),
		    updated_at = now()
		WHERE id = $1
		  AND (
		    (outcome = 'active'
		      AND status IN ('pending', 'approved', 'queued', 'downloading')
		      AND (status <> 'approved' OR submit_lease_until IS NULL OR submit_lease_until <= now()))
		    -- A failed request is complete only when the title came from
		    -- elsewhere. One that failed after delivering some quality keeps
		    -- its failure visible for an admin to retry.
		    OR (outcome = 'failed' AND NOT EXISTS (
		      SELECT 1 FROM media_request_targets t
		      WHERE t.request_id = media_requests.id AND t.status = 'completed'))
		  )
		  -- The title arriving completes only a request with nothing still on
		  -- its way; a submission that queued a target since the caller looked
		  -- lets the targets drive completion instead.
		  AND NOT EXISTS (
		    SELECT 1 FROM media_request_targets t
		    WHERE t.request_id = media_requests.id AND t.status IN ('queued', 'downloading'))
		RETURNING `+requestColumns(), id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, guardMiss(ctx, tx, id)
		}
		return nil, fmt.Errorf("mark request available: %w", err)
	}
	if err := r.recordEvent(ctx, tx, id, "available_in_library", actor, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request available transaction: %w", err)
	}
	return req, nil
}

func (r *Repository) MarkReconciled(ctx context.Context, id string) error {
	if _, err := r.pool.Exec(ctx, `UPDATE media_requests SET last_reconciled_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("mark request reconciled: %w", err)
	}
	return nil
}

func (r *Repository) RecomputeStatus(ctx context.Context, id string, actor Viewer) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin request recompute transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status Status
	var outcome Outcome
	if err := tx.QueryRow(ctx, `SELECT status, outcome FROM media_requests WHERE id = $1 FOR UPDATE`, id).Scan(&status, &outcome); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("lock request for recompute: %w", err)
	}
	if status != StatusApproved || outcome != OutcomeActive {
		return nil, ErrInvalidState
	}
	req, err := r.recomputeAggregate(ctx, tx, id, actor)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit request recompute transaction: %w", err)
	}
	return req, nil
}

const integrationColumns = `id, name, enabled, base_url, api_key_ref,
	last_check_at, last_check_status, last_check_error, updated_at,
	capability_id, installation_id, supported_media_types, plugin_config, revision`

func (r *Repository) ListIntegrations(ctx context.Context) ([]Integration, error) {
	return r.listIntegrations(ctx, r.pool)
}

func (r *Repository) listIntegrations(ctx context.Context, exec requestExecutor) ([]Integration, error) {
	rows, err := exec.Query(ctx, `SELECT `+integrationColumns+` FROM request_integrations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list request integrations: %w", err)
	}
	defer rows.Close()

	var out []Integration
	for rows.Next() {
		integration, err := r.scanIntegration(rows)
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
	i, err := r.scanIntegration(row)
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
	if i.PluginConfig == nil {
		i.PluginConfig = map[string]any{}
	}
	pluginConfig, err := json.Marshal(i.PluginConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal plugin config: %w", err)
	}
	// capability_id is the capability sub-id ("arr"/"seerr"), validated non-empty
	// upstream in validateInstance; persist it verbatim (never default it to the
	// capability type, which the plugin runtime can't resolve).
	capabilityID := strings.TrimSpace(i.CapabilityID)
	supportedMediaTypes := i.SupportedMediaTypes
	if supportedMediaTypes == nil {
		supportedMediaTypes = []string{}
	}
	apiKeyRef, err := r.encryptAPIKey(i.ID, i.APIKeyRef)
	if err != nil {
		return nil, fmt.Errorf("encrypt api key: %w", err)
	}
	row := exec.QueryRow(ctx, `
		INSERT INTO request_integrations (
			id, name, enabled, base_url, api_key_ref,
			capability_id, installation_id, supported_media_types,
			plugin_config, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now())
		RETURNING `+integrationColumns,
		i.ID, strings.TrimSpace(i.Name), i.Enabled, strings.TrimSpace(i.BaseURL),
		apiKeyRef, capabilityID, i.InstallationID, supportedMediaTypes, pluginConfig)
	out, err := r.scanIntegration(row)
	if err != nil {
		return nil, fmt.Errorf("create request integration: %w", err)
	}
	return &out, nil
}

// updateIntegration runs the integration UPDATE against any executor (pool or
// tx) so the plain update path and SaveIntegrationWithDefaults share the SQL.
func (r *Repository) updateIntegration(ctx context.Context, exec requestExecutor, i Integration) (*Integration, error) {
	if i.PluginConfig == nil {
		i.PluginConfig = map[string]any{}
	}
	pluginConfig, err := json.Marshal(i.PluginConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal plugin config: %w", err)
	}
	// capability_id is the capability sub-id ("arr"/"seerr"), validated non-empty
	// upstream in validateInstance; persist it verbatim (never default it to the
	// capability type, which the plugin runtime can't resolve).
	capabilityID := strings.TrimSpace(i.CapabilityID)
	supportedMediaTypes := i.SupportedMediaTypes
	if supportedMediaTypes == nil {
		supportedMediaTypes = []string{}
	}
	// Encrypt the incoming key; an empty result preserves the keep-existing CASE
	// (a blank edit leaves the stored key untouched).
	apiKeyRef, err := r.encryptAPIKey(i.ID, i.APIKeyRef)
	if err != nil {
		return nil, fmt.Errorf("encrypt api key: %w", err)
	}
	row := exec.QueryRow(ctx, `
		UPDATE request_integrations SET
			name=$2, enabled=$3, base_url=$4,
			api_key_ref = CASE WHEN $5 = '' THEN api_key_ref ELSE $5 END,
			capability_id=$6, installation_id=$7,
			supported_media_types=$8, plugin_config=$9, updated_at=now()
		WHERE id=$1
		RETURNING `+integrationColumns,
		i.ID, strings.TrimSpace(i.Name), i.Enabled, strings.TrimSpace(i.BaseURL),
		apiKeyRef, capabilityID, i.InstallationID, supportedMediaTypes, pluginConfig)
	out, err := r.scanIntegration(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update request integration: %w", err)
	}
	return &out, nil
}

// SaveIntegrationWithDefaults creates/updates the instance in a single
// transaction. The host no longer enforces a single HD/4K default per kind;
// the request_router plugin's RouteTargets picks the first is_default
// connection from plugin_config, so the save path is a plain insert-or-update.
func (r *Repository) SaveIntegrationWithDefaults(ctx context.Context, in Integration, isCreate bool) (*Integration, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin save integration: %w", err)
	}
	defer tx.Rollback(ctx)

	before, standard, err := r.standardBeforeSave(ctx, tx)
	if err != nil {
		return nil, err
	}
	var out *Integration
	if isCreate {
		out, err = r.insertIntegration(ctx, tx, in)
		if err == nil {
			err = defaultFirstServer(ctx, tx, out)
		}
	} else if err = ensureRoutesStillFit(ctx, tx, in, !standard); err == nil {
		out, err = r.updateIntegration(ctx, tx, in)
	}
	if err == nil && standard {
		err = r.advanceIfStandardBroken(ctx, tx, before)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit save integration: %w", err)
	}
	return out, nil
}

// otherServerServes is true when an enabled request server other than $1
// takes the media type $2 ('movie' or 'series'): one of the matching kind, or
// a connection of another plugin (Seerr, say) that serves it.
const otherServerServes = `EXISTS (
	SELECT 1 FROM request_integrations i
	WHERE i.id <> $1 AND i.enabled
	  AND CASE WHEN coalesce(i.plugin_config->>'service_kind', '') <> ''
	           THEN i.plugin_config->>'service_kind' = CASE $2::text WHEN 'movie' THEN 'radarr' ELSE 'sonarr' END
	           ELSE cardinality(i.supported_media_types) = 0 OR $2::text = ANY(i.supported_media_types) END)`

// lockServerRouting orders adding and deleting the servers of a kind, so
// deleting the only Radarr while another is added cannot leave movies with no
// Everything else.
func lockServerRouting(ctx context.Context, tx pgx.Tx, kind string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('request-routes-first-server:' || $1))`, kind)
	return err
}

// defaultFirstServer makes the first Radarr (Sonarr) server added the
// destination of Everything else for movies (series), when that media type has
// none and no other server takes it: a single-server setup then needs no
// routing. A server flagged 4K is left alone, since Everything else needs an
// HD server. Later servers change nothing.
func defaultFirstServer(ctx context.Context, tx pgx.Tx, in *Integration) error {
	kind, _ := in.PluginConfig[configServiceKind].(string)
	mediaType := map[string]MediaType{kindRadarr: MediaTypeMovie, kindSonarr: MediaTypeSeries}[kind]
	if mediaType == "" || !in.Enabled {
		return nil
	}
	for _, key := range []string{configIs4K, configIsDefault4K} {
		if flagged, _ := in.PluginConfig[key].(bool); flagged {
			return nil
		}
	}
	if err := lockServerRouting(ctx, tx, kind); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id)
		SELECT $3, $2::text, 1000, $4, true, $1
		WHERE NOT `+otherServerServes+`
		ON CONFLICT DO NOTHING
	`, in.ID, mediaType, FallbackRouteID(mediaType), fallbackRouteName); err != nil {
		return fmt.Errorf("route everything else to the first %s server: %w", kind, err)
	}
	return nil
}

func (r *Repository) DeleteIntegration(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete integration: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := lockRoutingMode(ctx, tx); err != nil {
		return err
	}
	if err := r.deleteIntegration(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// deleteIntegration deletes a server. The caller holds the routing mode lock,
// taken before any row lock as every server write does.
func (r *Repository) deleteIntegration(ctx context.Context, tx pgx.Tx, id string) error {
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

	// The last server of its kind can go with the Everything else that
	// only it served (made for it when it was added), as long as no rule
	// routes that media type; the media type then has no routing.
	var kind string
	if err := tx.QueryRow(ctx, `SELECT coalesce(plugin_config->>'service_kind', '') FROM request_integrations WHERE id = $1`, id).Scan(&kind); err != nil {
		return fmt.Errorf("read request integration kind: %w", err)
	}
	if kind != "" {
		if err := lockServerRouting(ctx, tx, kind); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM request_routes f
		WHERE f.is_fallback
		  AND $1 IN (f.hd_integration_id, f.uhd_integration_id)
		  AND coalesce(f.hd_integration_id, $1) = $1
		  AND coalesce(f.uhd_integration_id, $1) = $1
		  AND NOT EXISTS (SELECT 1 FROM request_routes r WHERE r.media_type = f.media_type AND NOT r.is_fallback)
		  AND NOT EXISTS (
			SELECT 1 FROM request_integrations i
			WHERE i.id <> $1
			  AND i.plugin_config->>'service_kind' = CASE f.media_type WHEN 'movie' THEN 'radarr' ELSE 'sonarr' END)
	`, id); err != nil {
		return fmt.Errorf("clear sole server's route: %w", err)
	}

	// Standard does not use Everything else, and hides it: a reference from
	// there does not keep the server. Advanced fills it in again from
	// Standard's servers.
	routing, err := scanRoutingSettings(tx.QueryRow(ctx, `SELECT mode, revision, updated_at FROM request_routing WHERE id`))
	if err != nil {
		return err
	}
	standard := routing.Mode == RoutingStandard
	if standard {
		if _, err := tx.Exec(ctx, `
			UPDATE request_routes SET
				hd_integration_id = NULLIF(hd_integration_id, $1),
				uhd_integration_id = NULLIF(uhd_integration_id, $1)
			WHERE is_fallback AND $1 IN (hd_integration_id, uhd_integration_id)`, id); err != nil {
			return fmt.Errorf("clear everything else under standard routing: %w", err)
		}
	}

	// Deleting a server a route sends to would silently reroute its titles.
	rows, err := tx.Query(ctx, `
		SELECT name FROM request_routes
		WHERE hd_integration_id = $1 OR uhd_integration_id = $1
		ORDER BY media_type, is_fallback, position`, id)
	if err != nil {
		return fmt.Errorf("check integration routes: %w", err)
	}
	var routes []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		routes = append(routes, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(routes) > 0 && standard {
		return &ValidationError{FormError: "Paused routing rules still send requests to this server (" + strings.Join(routes, ", ") + "). Switch to Advanced routing and send them elsewhere first."}
	}
	if len(routes) > 0 {
		return &ValidationError{FormError: "Routing still sends requests to this server (" + strings.Join(routes, ", ") + "); send them elsewhere first."}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM request_integrations WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete request integration: %w", err)
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
	if cond := adminViewCondition(filter.View); cond != "" {
		conditions = append(conditions, cond)
	}
	if filter.MediaType != "" {
		args = append(args, filter.MediaType)
		conditions = append(conditions, "media_type = $"+strconv.Itoa(len(args)))
	}
	if filter.RequestedByUserID > 0 {
		args = append(args, filter.RequestedByUserID)
		conditions = append(conditions, "requested_by_user_id = $"+strconv.Itoa(len(args)))
	}
	if q := strings.TrimSpace(filter.Query); q != "" {
		args = append(args, "%"+likeEscaper.Replace(q)+"%")
		cond := "title ILIKE $" + strconv.Itoa(len(args))
		// TMDB IDs are 4-byte integers; a longer number is only a title search.
		if tmdbID, err := strconv.ParseInt(q, 10, 32); err == nil && tmdbID > 0 {
			args = append(args, tmdbID)
			cond = "(" + cond + " OR tmdb_id = $" + strconv.Itoa(len(args)) + ")"
		}
		conditions = append(conditions, cond)
	}
	if filter.Before != nil {
		args = append(args, filter.Before.CreatedAt, filter.Before.ID)
		conditions = append(conditions, "(created_at, id) < ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
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
		ORDER BY created_at DESC, id DESC
		LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args)), args
}

// likeEscaper escapes LIKE wildcards in a search term, so "50%" matches
// itself.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// adminViewSQL holds each admin view's condition; CountAdminViews counts
// with the same ones.
var adminViewSQL = map[AdminView]string{
	AdminViewNeedsApproval: "(outcome = 'active' AND status = 'pending')",
	AdminViewInProgress:    "(outcome = 'active' AND status IN ('approved', 'queued', 'downloading'))",
	AdminViewFailed:        "(outcome = 'failed')",
	AdminViewDone:          "((outcome = 'active' AND status = 'completed') OR outcome IN ('declined', 'cancelled'))",
}

func adminViewCondition(view AdminView) string {
	return adminViewSQL[view]
}

// CountAdminViews counts the requests in each admin view.
func (r *Repository) CountAdminViews(ctx context.Context) (AdminViewCounts, error) {
	var c AdminViewCounts
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE `+adminViewSQL[AdminViewNeedsApproval]+`),
		       count(*) FILTER (WHERE `+adminViewSQL[AdminViewInProgress]+`),
		       count(*) FILTER (WHERE `+adminViewSQL[AdminViewFailed]+`),
		       count(*) FILTER (WHERE `+adminViewSQL[AdminViewDone]+`)
		FROM media_requests`).Scan(&c.NeedsApproval, &c.InProgress, &c.Failed, &c.Done)
	if err != nil {
		return AdminViewCounts{}, fmt.Errorf("count admin request views: %w", err)
	}
	return c, nil
}

// ListEvents reads a request's history, newest first.
func (r *Repository) ListEvents(ctx context.Context, requestID string, limit int) ([]RequestEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT e.id, e.request_id, e.event_type, e.actor_user_id, e.actor_profile_id, e.message,
		       e.created_at, COALESCE(u.username, '')
		FROM media_request_events e
		LEFT JOIN users u ON u.id = e.actor_user_id
		WHERE e.request_id = $1
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT $2`, requestID, limit)
	if err != nil {
		return nil, fmt.Errorf("list request events: %w", err)
	}
	defer rows.Close()
	var out []RequestEvent
	for rows.Next() {
		var e RequestEvent
		if err := rows.Scan(&e.ID, &e.RequestID, &e.EventType, &e.ActorUserID, &e.ActorProfileID, &e.Message,
			&e.CreatedAt, &e.ActorUsername); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// nonNilSeasons stores no seasons as an empty array, not NULL.
func nonNilSeasons(seasons []int) []int {
	if seasons == nil {
		return []int{}
	}
	return seasons
}

func requestSelectSQL() string {
	return "SELECT " + requestColumns() + " FROM media_requests "
}

func requestColumns() string {
	return `id, provider, media_type, tmdb_id, tvdb_id, imdb_id, title, year,
	        overview, poster_path, backdrop_path, status, outcome,
	        requested_by_user_id, requested_by_profile_id, is_anime,
	        last_error, created_at, updated_at, approved_at, completed_at,
	        submit_attempts, submit_lease_until, next_submit_at, outcome_reason, routing_facts, seasons`
}

type requestScanner interface {
	Scan(dest ...any) error
}

func scanRequest(row requestScanner) (*Request, error) {
	var req Request
	var tvdbID, year sql.NullInt64
	var approvedAt, completedAt, submitLeaseUntil, nextSubmitAt sql.NullTime
	var rawFacts []byte
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
		&req.SubmitAttempts,
		&submitLeaseUntil,
		&nextSubmitAt,
		&req.OutcomeReason,
		&rawFacts,
		&req.Seasons,
	); err != nil {
		return nil, err
	}
	facts, err := decodeRoutingFacts(rawFacts)
	if err != nil {
		return nil, err
	}
	req.RoutingFacts = facts
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
	if submitLeaseUntil.Valid {
		req.SubmitLeaseUntil = &submitLeaseUntil.Time
	}
	if nextSubmitAt.Valid {
		req.NextSubmitAt = &nextSubmitAt.Time
	}
	return &req, nil
}

type integrationScanner interface {
	Scan(dest ...any) error
}

func (r *Repository) scanIntegration(row integrationScanner) (Integration, error) {
	var i Integration
	var installationID sql.NullInt64
	var pluginConfigRaw []byte
	var lastCheckAt sql.NullTime
	if err := row.Scan(
		&i.ID, &i.Name, &i.Enabled, &i.BaseURL, &i.APIKeyRef,
		&lastCheckAt, &i.LastCheckStatus, &i.LastCheckError, &i.UpdatedAt,
		&i.CapabilityID, &installationID, &i.SupportedMediaTypes, &pluginConfigRaw, &i.Revision,
	); err != nil {
		return Integration{}, err
	}
	// Decrypt the stored api key (read-path contract: legacy plaintext passes
	// through, enc:v1: values decrypt, corrupt ciphertext errors). Callers
	// receive the literal key — there is no longer a ref/literal ambiguity.
	apiKey, err := r.cipher.DecryptIfEncrypted(i.APIKeyRef, apiKeyAAD(i.ID))
	if err != nil {
		return Integration{}, fmt.Errorf("decrypt request integration %s api key: %w", i.ID, err)
	}
	i.APIKeyRef = apiKey
	if installationID.Valid {
		v := int(installationID.Int64)
		i.InstallationID = &v
	}
	if len(pluginConfigRaw) > 0 {
		if err := json.Unmarshal(pluginConfigRaw, &i.PluginConfig); err != nil {
			return Integration{}, fmt.Errorf("unmarshal request integration plugin config for %s: %w", i.ID, err)
		}
	}
	if i.PluginConfig == nil {
		i.PluginConfig = map[string]any{}
	}
	if lastCheckAt.Valid {
		i.LastCheckAt = &lastCheckAt.Time
	}
	return i, nil
}
