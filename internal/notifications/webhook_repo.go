package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebhookRepository owns notification_webhooks and webhook_delivery_attempts.
type WebhookRepository struct {
	pool *pgxpool.Pool
}

// NewWebhookRepository creates a WebhookRepository.
func NewWebhookRepository(pool *pgxpool.Pool) *WebhookRepository {
	return &WebhookRepository{pool: pool}
}

const webhookColumns = `
	id, user_id, profile_id, name, type, url_ciphertext, url_host,
	signing_secret_ciphertext, enabled,
	notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up,
	notify_requests,
	consecutive_failures, disabled_reason,
	last_success_at, last_failure_at, last_failure_status, last_failure_message,
	created_at, updated_at, revision`

func scanWebhook(row pgx.Row) (*Webhook, error) {
	var hook Webhook
	err := row.Scan(
		&hook.ID, &hook.UserID, &hook.ProfileID, &hook.Name, &hook.Type,
		&hook.URLCiphertext, &hook.URLHost, &hook.SigningSecretCiphertext, &hook.Enabled,
		&hook.NotifyFavorites, &hook.NotifyWatchlist, &hook.NotifyContinueWatching, &hook.NotifyNextUp,
		&hook.NotifyRequests,
		&hook.ConsecutiveFailures, &hook.DisabledReason,
		&hook.LastSuccessAt, &hook.LastFailureAt, &hook.LastFailureStatus, &hook.LastFailureMessage,
		&hook.CreatedAt, &hook.UpdatedAt, &hook.Revision,
	)
	if err != nil {
		return nil, err
	}
	return &hook, nil
}

func scanWebhooks(rows pgx.Rows) ([]Webhook, error) {
	defer rows.Close()
	hooks := make([]Webhook, 0, 4)
	for rows.Next() {
		hook, err := scanWebhook(rows)
		if err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		hooks = append(hooks, *hook)
	}
	return hooks, rows.Err()
}

// ListByProfile returns all of an account profile's webhooks for the listing
// endpoint. Profile IDs repeat across accounts, so every management query
// matches the account too.
func (r *WebhookRepository) ListByProfile(ctx context.Context, userID int, profileID string) ([]Webhook, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+webhookColumns+` FROM notification_webhooks WHERE user_id = $1 AND profile_id = $2 ORDER BY created_at`,
		userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	return scanWebhooks(rows)
}

// ListEnabledByProfiles loads enabled webhooks for a set of profiles, keyed
// by account and profile: profile IDs repeat across accounts. Used by the
// outbox enqueue inside the delivery's transaction.
func (r *WebhookRepository) ListEnabledByProfiles(ctx context.Context, tx pgx.Tx, profiles []profileKey) (map[profileKey][]Webhook, error) {
	out := make(map[profileKey][]Webhook, len(profiles))
	if len(profiles) == 0 {
		return out, nil
	}
	userIDs, profileIDs := profileKeyArrays(profiles)
	rows, err := tx.Query(ctx,
		`SELECT `+webhookColumns+` FROM notification_webhooks
		WHERE (user_id, profile_id) IN (SELECT * FROM unnest($1::int[], $2::text[])) AND enabled`,
		userIDs, profileIDs)
	if err != nil {
		return nil, fmt.Errorf("list enabled webhooks: %w", err)
	}
	hooks, err := scanWebhooks(rows)
	if err != nil {
		return nil, err
	}
	for _, hook := range hooks {
		key := profileKey{hook.UserID, hook.ProfileID}
		out[key] = append(out[key], hook)
	}
	return out, nil
}

// GetByID returns one webhook scoped to the account profile; (nil, nil) when
// absent.
func (r *WebhookRepository) GetByID(ctx context.Context, userID int, profileID, id string) (*Webhook, error) {
	hook, err := scanWebhook(r.pool.QueryRow(ctx,
		`SELECT `+webhookColumns+` FROM notification_webhooks WHERE user_id = $1 AND profile_id = $2 AND id = $3`,
		userID, profileID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get webhook: %w", err)
	}
	return hook, nil
}

// getByIDUnscoped loads a webhook for internal delivery paths.
func (r *WebhookRepository) getByIDUnscoped(ctx context.Context, id string) (*Webhook, error) {
	hook, err := scanWebhook(r.pool.QueryRow(ctx,
		`SELECT `+webhookColumns+` FROM notification_webhooks WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get webhook: %w", err)
	}
	return hook, nil
}

// ErrWebhookNameTaken is returned when the account profile already has a
// webhook with the requested name.
var ErrWebhookNameTaken = errors.New("a webhook with this name already exists")

// isWebhookNameViolation reports whether err is the unique violation on the
// webhook name constraint, unique per (user_id, profile_id), via the typed pgx error (string
// matching on error text would break if message formatting changes).
func isWebhookNameViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "notification_webhooks_profile_name_key"
}

// InsertWithLimit persists a new webhook unless the account profile is
// already at maxPerProfile. The count and insert run under a per-profile-ID
// advisory transaction lock so concurrent creates cannot both pass the check
// and push the profile past its cap. The lock is coarser than the count (it
// also serializes a same-ID profile on another account), which is harmless.
func (r *WebhookRepository) InsertWithLimit(ctx context.Context, hook Webhook, maxPerProfile int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin webhook insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('notification_webhooks:' || $1, 0))`,
		hook.ProfileID); err != nil {
		return fmt.Errorf("lock webhook quota: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM notification_webhooks WHERE user_id = $1 AND profile_id = $2`, hook.UserID, hook.ProfileID,
	).Scan(&count); err != nil {
		return fmt.Errorf("count webhooks: %w", err)
	}
	if count >= maxPerProfile {
		return ErrWebhookLimit
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_webhooks
			(id, user_id, profile_id, name, type, url_ciphertext, url_host,
			 signing_secret_ciphertext, enabled,
			 notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up,
			 notify_requests)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		hook.ID, hook.UserID, hook.ProfileID, hook.Name, hook.Type,
		hook.URLCiphertext, hook.URLHost, hook.SigningSecretCiphertext, hook.Enabled,
		hook.NotifyFavorites, hook.NotifyWatchlist, hook.NotifyContinueWatching, hook.NotifyNextUp,
		hook.NotifyRequests); err != nil {
		if isWebhookNameViolation(err) {
			return ErrWebhookNameTaken
		}
		return fmt.Errorf("insert webhook: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit webhook insert: %w", err)
	}
	return nil
}

// Delete removes an account profile's webhook; attempts cascade. Idempotent.
func (r *WebhookRepository) Delete(ctx context.Context, userID int, profileID, id string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_webhooks WHERE user_id = $1 AND profile_id = $2 AND id = $3`, userID, profileID, id)
	return err
}

// DeleteAllForProfile removes a deleted account profile's webhooks.
func (r *WebhookRepository) DeleteAllForProfile(ctx context.Context, userID int, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_webhooks WHERE user_id = $1 AND profile_id = $2`, userID, profileID)
	return err
}

// WebhookAttemptOutcome is how one send of a webhook attempt ended.
type WebhookAttemptOutcome struct {
	AttemptID     string
	Outcome       string
	AttemptNumber int
	HTTPStatus    *int
	Message       string
	NextRetryAt   *time.Time
}

// finalizeAttemptCTE writes a WebhookAttemptOutcome ($1-$6) in the same
// statement as the webhook's streak update, so a crash between the two
// cannot leave the streaks disagreeing with the attempt history. Claims do
// not change attempt_number, so it fences the write: when a lease expired
// mid-send and another worker already finished this send, the row has moved
// on and neither the attempt nor the streaks change.
const finalizeAttemptCTE = `
	WITH finalized AS (
		UPDATE webhook_delivery_attempts
		SET outcome = $2, attempt_number = $3, attempted_at = now(),
		    http_status = $4, failure_message = left($5, 256), next_retry_at = $6
		WHERE id = $1 AND attempt_number = $3 - 1
		RETURNING 1
	)`

// errAttemptSuperseded reports that another worker already finished the send.
var errAttemptSuperseded = errors.New("webhook attempt already finalized")

func (o WebhookAttemptOutcome) args() []any {
	var message *string
	if o.Message != "" {
		message = &o.Message
	}
	return []any{o.AttemptID, o.Outcome, o.AttemptNumber, o.HTTPStatus, message, o.NextRetryAt}
}

// RecordSuccess finalizes a delivered attempt and resets both streaks. It
// returns errAttemptSuperseded when another worker already finished the send.
func (r *WebhookRepository) RecordSuccess(ctx context.Context, webhookID string, done WebhookAttemptOutcome) error {
	tag, err := r.pool.Exec(ctx, finalizeAttemptCTE+`
		UPDATE notification_webhooks
		SET last_success_at = now(), consecutive_failures = 0, consecutive_rejections = 0,
		    updated_at = now()
		WHERE id = $7 AND EXISTS (SELECT 1 FROM finalized)`, append(done.args(), webhookID)...)
	if err == nil && tag.RowsAffected() == 0 {
		return errAttemptSuperseded
	}
	return err
}

// RecordFailure finalizes a failed attempt, increments the failure streak,
// and stores the diagnostic. A rejected delivery (a non-retryable 4xx) also
// extends the rejection streak, whose new length is returned. Success,
// re-enabling, and a URL change reset both streaks. It returns
// errAttemptSuperseded when another worker already finished the send.
func (r *WebhookRepository) RecordFailure(ctx context.Context, webhookID string, done WebhookAttemptOutcome, rejected bool) (int, error) {
	var rejections int
	err := r.pool.QueryRow(ctx, finalizeAttemptCTE+`
		UPDATE notification_webhooks
		SET last_failure_at = now(),
		    last_failure_status = $4,
		    last_failure_message = left($5, 256),
		    consecutive_failures = consecutive_failures + 1,
		    consecutive_rejections = consecutive_rejections + CASE WHEN $8 THEN 1 ELSE 0 END,
		    updated_at = now()
		WHERE id = $7 AND EXISTS (SELECT 1 FROM finalized)
		RETURNING consecutive_rejections`, append(done.args(), webhookID, rejected)...).Scan(&rejections)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errAttemptSuperseded
	}
	return rejections, err
}

// Disable auto-disables an enabled webhook with a profile-visible reason. It
// reports whether this call disabled it, so concurrent failures post one
// notice.
func (r *WebhookRepository) Disable(ctx context.Context, webhookID, reason string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE notification_webhooks
		SET enabled = false, disabled_reason = left($2, 256), updated_at = now()
		WHERE id = $1 AND enabled`, webhookID, reason)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func scanDeliveryAttempts(rows pgx.Rows) ([]DeliveryAttempt, error) {
	defer rows.Close()
	attempts := make([]DeliveryAttempt, 0, 8)
	for rows.Next() {
		var attempt DeliveryAttempt
		if err := rows.Scan(
			&attempt.ID, &attempt.NotificationDeliveryID, &attempt.TargetID,
			&attempt.AttemptNumber, &attempt.AttemptedAt, &attempt.NextRetryAt,
			&attempt.HTTPStatus, &attempt.Outcome, &attempt.FailureMessage,
		); err != nil {
			return nil, fmt.Errorf("scan webhook attempt: %w", err)
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

// EnqueueAttempts inserts `pending` outbox rows inside the fanout
// transaction. attempt_number starts at 0 (no send tried yet).
func (r *WebhookRepository) EnqueueAttempts(ctx context.Context, tx pgx.Tx, attempts []DeliveryAttempt) error {
	if len(attempts) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`
		INSERT INTO webhook_delivery_attempts
			(id, notification_delivery_id, webhook_id, attempt_number, outcome)
		VALUES `)
	args := make([]any, 0, len(attempts)*5)
	for i, attempt := range attempts {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := len(args)
		sb.WriteString(fmt.Sprintf("($%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5))
		args = append(args, attempt.ID, attempt.NotificationDeliveryID, attempt.TargetID, 0, WebhookOutcomePending)
	}
	sb.WriteString(" ON CONFLICT DO NOTHING")
	if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("enqueue webhook attempts: %w", err)
	}
	return nil
}

// claimLease is how long a claimed attempt is invisible to other claimers. A
// crash mid-send surfaces the attempt to the retry worker after the lease.
const webhookClaimLease = 90 * time.Second

// ClaimPendingForDelivery claims a delivery's pending attempts for immediate
// post-commit dispatch. Claiming flips the row to `retrying` with a short
// lease instead of holding row locks across the HTTP send.
func (r *WebhookRepository) ClaimPendingForDelivery(ctx context.Context, deliveryID string) ([]DeliveryAttempt, error) {
	return r.claim(ctx, `
		UPDATE webhook_delivery_attempts SET outcome = 'retrying', next_retry_at = now() + $2
		WHERE id IN (
			SELECT id FROM webhook_delivery_attempts
			WHERE notification_delivery_id = $1 AND outcome = 'pending'
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, notification_delivery_id, webhook_id, attempt_number, attempted_at,
		          next_retry_at, http_status, outcome, failure_message`,
		deliveryID, webhookClaimLease)
}

// ClaimDue claims attempts whose retry is due, plus stale pending rows whose
// post-commit dispatch never ran (outbox recovery after a crash).
func (r *WebhookRepository) ClaimDue(ctx context.Context, limit int) ([]DeliveryAttempt, error) {
	return r.claim(ctx, `
		UPDATE webhook_delivery_attempts SET outcome = 'retrying', next_retry_at = now() + $2
		WHERE id IN (
			SELECT id FROM webhook_delivery_attempts
			WHERE (outcome = 'retrying' AND next_retry_at <= now())
			   OR (outcome = 'pending' AND attempted_at <= now() - interval '60 seconds')
			ORDER BY next_retry_at NULLS FIRST
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, notification_delivery_id, webhook_id, attempt_number, attempted_at,
		          next_retry_at, http_status, outcome, failure_message`,
		limit, webhookClaimLease)
}

func (r *WebhookRepository) claim(ctx context.Context, query string, args ...any) ([]DeliveryAttempt, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("claim webhook attempts: %w", err)
	}
	return scanDeliveryAttempts(rows)
}

// FinalizeAttempt records a send result: the new outcome, the attempt number
// just consumed, and the optional next retry time.
func (r *WebhookRepository) FinalizeAttempt(ctx context.Context, attemptID, outcome string, attemptNumber int, httpStatus *int, failureMessage string, nextRetryAt *time.Time) error {
	var message *string
	if failureMessage != "" {
		message = &failureMessage
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE webhook_delivery_attempts
		SET outcome = $2, attempt_number = $3, attempted_at = now(),
		    http_status = $4, failure_message = left($5, 256), next_retry_at = $6
		WHERE id = $1`,
		attemptID, outcome, attemptNumber, httpStatus, message, nextRetryAt)
	if err != nil {
		return fmt.Errorf("finalize webhook attempt: %w", err)
	}
	return nil
}

// DeleteOldAttempts applies attempt retention: delivered rows past 7 days,
// failed/auto_disabled rows past 30 days. Pending/retrying rows are kept
// until they resolve.
func (r *WebhookRepository) DeleteOldAttempts(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM webhook_delivery_attempts
		WHERE (outcome = 'delivered' AND attempted_at < $1)
		   OR (outcome IN ('failed', 'auto_disabled') AND attempted_at < $2)`,
		now.AddDate(0, 0, -7), now.AddDate(0, 0, -30))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ListPage returns at most limit records in stable creation order. Callers may
// request one lookahead row; this read does not alter delivery state.
func (r *WebhookRepository) ListPage(ctx context.Context, userID int, profile string, limit int, after *Cursor) ([]Webhook, error) {
	query := `SELECT ` + webhookColumns + ` FROM notification_webhooks WHERE user_id = $1 AND profile_id = $2`
	args := []any{userID, profile}
	if after != nil {
		query += ` AND (created_at, id) > ($3, $4)`
		args = append(args, after.CreatedAt, after.ID)
	}
	query += fmt.Sprintf(" ORDER BY created_at, id LIMIT $%d", len(args)+1)
	args = append(args, limit)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list notification destinations: %w", err)
	}
	return scanWebhooks(rows)
}

// DeleteGuarded holds the row lock across precondition evaluation and deletion.
// The revision trigger covers bridge, configuration and provider writers too.
func (r *WebhookRepository) DeleteGuarded(ctx context.Context, userID int, profile, id string, check func(int64) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var revision int64
	err = tx.QueryRow(ctx, `SELECT revision FROM notification_webhooks WHERE user_id=$1 AND profile_id=$2 AND id=$3 FOR UPDATE`, userID, profile, id).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWebhookNotFound
	}
	if err != nil {
		return err
	}
	if err = check(revision); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM notification_webhooks WHERE user_id=$1 AND profile_id=$2 AND id=$3`, userID, profile, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReplaceSigningSecret changes no configuration or provider bookkeeping fields.
func (r *WebhookRepository) ReplaceSigningSecret(ctx context.Context, userID int, profile, id, ciphertext string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE notification_webhooks SET signing_secret_ciphertext=$4, updated_at=now() WHERE user_id=$1 AND profile_id=$2 AND id=$3 AND type='generic'`, userID, profile, id, ciphertext)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWebhookNotFound
	}
	return nil
}

// UpdateGuarded reads, validates and updates configuration under the same row lock.
// Secret/type and provider outcome columns are never copied from an editor.
func (r *WebhookRepository) UpdateGuarded(ctx context.Context, userID int, profile, id string, apply func(*Webhook) error) (*Webhook, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	hook, err := scanWebhook(tx.QueryRow(ctx, `SELECT `+webhookColumns+` FROM notification_webhooks WHERE user_id=$1 AND profile_id=$2 AND id=$3 FOR UPDATE`, userID, profile, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWebhookNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = apply(hook); err != nil {
		return nil, err
	}
	row, err := scanWebhook(tx.QueryRow(ctx, `UPDATE notification_webhooks SET
 name=$4, url_ciphertext=$5, url_host=$6, enabled=$7,
 notify_favorites=$8, notify_watchlist=$9, notify_continue_watching=$10,
 notify_next_up=$11, notify_requests=$12, consecutive_failures=$13,
 consecutive_rejections=LEAST(consecutive_rejections, $13),
 disabled_reason=$14, updated_at=now()
 WHERE user_id=$1 AND profile_id=$2 AND id=$3 RETURNING `+webhookColumns,
		userID, profile, id, hook.Name, hook.URLCiphertext, hook.URLHost, hook.Enabled,
		hook.NotifyFavorites, hook.NotifyWatchlist, hook.NotifyContinueWatching,
		hook.NotifyNextUp, hook.NotifyRequests, hook.ConsecutiveFailures, hook.DisabledReason))
	if isWebhookNameViolation(err) {
		return nil, ErrWebhookNameTaken
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return row, nil
}
