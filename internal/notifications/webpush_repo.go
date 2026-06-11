package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebPushSubscription is one browser push registration, scoped to a profile.
type WebPushSubscription struct {
	ID                  string
	UserID              int
	ProfileID           string
	Endpoint            string
	P256dh              string
	Auth                string
	DeviceName          string
	Enabled             bool
	ConsecutiveFailures int
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	LastFailureStatus   *int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// WebPushRepository owns web_push_subscriptions and
// web_push_delivery_attempts.
type WebPushRepository struct {
	pool *pgxpool.Pool
}

// NewWebPushRepository creates a WebPushRepository.
func NewWebPushRepository(pool *pgxpool.Pool) *WebPushRepository {
	return &WebPushRepository{pool: pool}
}

const webPushColumns = `
	id, user_id, profile_id, endpoint, p256dh, auth, device_name, enabled,
	consecutive_failures, last_success_at, last_failure_at, last_failure_status,
	created_at, updated_at`

func scanWebPushSubscription(row pgx.Row) (*WebPushSubscription, error) {
	var sub WebPushSubscription
	err := row.Scan(
		&sub.ID, &sub.UserID, &sub.ProfileID, &sub.Endpoint, &sub.P256dh, &sub.Auth,
		&sub.DeviceName, &sub.Enabled, &sub.ConsecutiveFailures,
		&sub.LastSuccessAt, &sub.LastFailureAt, &sub.LastFailureStatus,
		&sub.CreatedAt, &sub.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

func scanWebPushSubscriptions(rows pgx.Rows) ([]WebPushSubscription, error) {
	defer rows.Close()
	subs := make([]WebPushSubscription, 0, 4)
	for rows.Next() {
		sub, err := scanWebPushSubscription(rows)
		if err != nil {
			return nil, fmt.Errorf("scan web push subscription: %w", err)
		}
		subs = append(subs, *sub)
	}
	return subs, rows.Err()
}

// Upsert registers a subscription. An existing endpoint is reassigned to the
// caller's (user, profile): one browser endpoint notifies exactly one
// profile, the one that subscribed most recently.
func (r *WebPushRepository) Upsert(ctx context.Context, sub WebPushSubscription) (*WebPushSubscription, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin web push upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Reassignment keeps the row id, so undelivered attempts enqueued for the
	// previous owner would otherwise be sent to the new owner's browser once
	// the retry worker reclaims them. Cancel them before the ownership flips.
	if _, err := tx.Exec(ctx, `
		DELETE FROM web_push_delivery_attempts
		WHERE outcome IN ('pending', 'retrying')
		  AND subscription_id IN (
			SELECT id FROM web_push_subscriptions
			WHERE endpoint = $1 AND profile_id <> $2
		  )`,
		sub.Endpoint, sub.ProfileID); err != nil {
		return nil, fmt.Errorf("purge reassigned web push attempts: %w", err)
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO web_push_subscriptions
			(id, user_id, profile_id, endpoint, p256dh, auth, device_name)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (endpoint) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			profile_id = EXCLUDED.profile_id,
			p256dh = EXCLUDED.p256dh,
			auth = EXCLUDED.auth,
			device_name = EXCLUDED.device_name,
			enabled = true,
			consecutive_failures = 0,
			updated_at = now()
		RETURNING `+webPushColumns,
		sub.ID, sub.UserID, sub.ProfileID, sub.Endpoint, sub.P256dh, sub.Auth, sub.DeviceName)
	saved, err := scanWebPushSubscription(row)
	if err != nil {
		return nil, fmt.Errorf("upsert web push subscription: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit web push upsert: %w", err)
	}
	return saved, nil
}

// ListByProfile returns a profile's subscriptions for the settings UI.
func (r *WebPushRepository) ListByProfile(ctx context.Context, profileID string) ([]WebPushSubscription, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+webPushColumns+` FROM web_push_subscriptions WHERE profile_id = $1 ORDER BY created_at`,
		profileID)
	if err != nil {
		return nil, fmt.Errorf("list web push subscriptions: %w", err)
	}
	return scanWebPushSubscriptions(rows)
}

// ListEnabledByProfiles loads enabled subscriptions keyed by profile, inside
// the fanout transaction (outbox enqueue).
func (r *WebPushRepository) ListEnabledByProfiles(ctx context.Context, tx pgx.Tx, profileIDs []string) (map[string][]WebPushSubscription, error) {
	out := make(map[string][]WebPushSubscription, len(profileIDs))
	if len(profileIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx,
		`SELECT `+webPushColumns+` FROM web_push_subscriptions WHERE profile_id = ANY($1) AND enabled`,
		profileIDs)
	if err != nil {
		return nil, fmt.Errorf("list enabled web push subscriptions: %w", err)
	}
	subs, err := scanWebPushSubscriptions(rows)
	if err != nil {
		return nil, err
	}
	for _, sub := range subs {
		out[sub.ProfileID] = append(out[sub.ProfileID], sub)
	}
	return out, nil
}

func (r *WebPushRepository) getByIDUnscoped(ctx context.Context, id string) (*WebPushSubscription, error) {
	sub, err := scanWebPushSubscription(r.pool.QueryRow(ctx,
		`SELECT `+webPushColumns+` FROM web_push_subscriptions WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get web push subscription: %w", err)
	}
	return sub, nil
}

// Delete removes one subscription scoped to the profile. Idempotent.
func (r *WebPushRepository) Delete(ctx context.Context, profileID, id string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM web_push_subscriptions WHERE profile_id = $1 AND id = $2`, profileID, id)
	return err
}

// DeleteByEndpoint removes a subscription by its endpoint (browser
// unsubscribe flow, where the client only knows the endpoint). Scoped to the
// user, not the profile: Upsert reassigns an endpoint across profiles of the
// same account, so a disable issued under one profile must still delete the
// row even when another profile owns it.
func (r *WebPushRepository) DeleteByEndpoint(ctx context.Context, userID int, endpoint string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM web_push_subscriptions WHERE user_id = $1 AND endpoint = $2`, userID, endpoint)
	return err
}

// deleteGone removes a subscription the push service reports as expired or
// revoked (HTTP 404/410). The attempts cascade.
func (r *WebPushRepository) deleteGone(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM web_push_subscriptions WHERE id = $1`, id)
	return err
}

// DeleteAllForProfile removes a deleted profile's subscriptions.
func (r *WebPushRepository) DeleteAllForProfile(ctx context.Context, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM web_push_subscriptions WHERE profile_id = $1`, profileID)
	return err
}

// RecordSuccess resets the failure streak after a delivered push.
func (r *WebPushRepository) RecordSuccess(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE web_push_subscriptions
		SET last_success_at = now(), consecutive_failures = 0, updated_at = now()
		WHERE id = $1`, id)
	return err
}

// RecordFailure increments the failure streak.
func (r *WebPushRepository) RecordFailure(ctx context.Context, id string, httpStatus *int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE web_push_subscriptions
		SET last_failure_at = now(), last_failure_status = $2,
		    consecutive_failures = consecutive_failures + 1, updated_at = now()
		WHERE id = $1`, id, httpStatus)
	return err
}

// --- Attempt outbox (mirrors webhook_delivery_attempts semantics) ---

// EnqueueAttempts inserts `pending` outbox rows inside the fanout transaction.
func (r *WebPushRepository) EnqueueAttempts(ctx context.Context, tx pgx.Tx, attempts []DeliveryAttempt) error {
	if len(attempts) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`
		INSERT INTO web_push_delivery_attempts
			(id, notification_delivery_id, subscription_id, attempt_number, outcome)
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
		return fmt.Errorf("enqueue web push attempts: %w", err)
	}
	return nil
}

const webPushAttemptReturning = `
		RETURNING id, notification_delivery_id, subscription_id, attempt_number, attempted_at,
		          next_retry_at, http_status, outcome, failure_message`

// ClaimPendingForDelivery claims a delivery's pending attempts for immediate
// post-commit dispatch (lease-based, like webhooks).
func (r *WebPushRepository) ClaimPendingForDelivery(ctx context.Context, deliveryID string) ([]DeliveryAttempt, error) {
	return r.claim(ctx, `
		UPDATE web_push_delivery_attempts SET outcome = 'retrying', next_retry_at = now() + $2
		WHERE id IN (
			SELECT id FROM web_push_delivery_attempts
			WHERE notification_delivery_id = $1 AND outcome = 'pending'
			FOR UPDATE SKIP LOCKED
		)`+webPushAttemptReturning,
		deliveryID, webhookClaimLease)
}

// ClaimDue claims due retries plus stale pending rows (outbox recovery).
func (r *WebPushRepository) ClaimDue(ctx context.Context, limit int) ([]DeliveryAttempt, error) {
	return r.claim(ctx, `
		UPDATE web_push_delivery_attempts SET outcome = 'retrying', next_retry_at = now() + $2
		WHERE id IN (
			SELECT id FROM web_push_delivery_attempts
			WHERE (outcome = 'retrying' AND next_retry_at <= now())
			   OR (outcome = 'pending' AND attempted_at <= now() - interval '60 seconds')
			ORDER BY next_retry_at NULLS FIRST
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)`+webPushAttemptReturning,
		limit, webhookClaimLease)
}

func (r *WebPushRepository) claim(ctx context.Context, query string, args ...any) ([]DeliveryAttempt, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("claim web push attempts: %w", err)
	}
	return scanDeliveryAttempts(rows)
}

// FinalizeAttempt records a send result.
func (r *WebPushRepository) FinalizeAttempt(ctx context.Context, attemptID, outcome string, attemptNumber int, httpStatus *int, failureMessage string, nextRetryAt *time.Time) error {
	var message *string
	if failureMessage != "" {
		message = &failureMessage
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE web_push_delivery_attempts
		SET outcome = $2, attempt_number = $3, attempted_at = now(),
		    http_status = $4, failure_message = left($5, 256), next_retry_at = $6
		WHERE id = $1`,
		attemptID, outcome, attemptNumber, httpStatus, message, nextRetryAt)
	if err != nil {
		return fmt.Errorf("finalize web push attempt: %w", err)
	}
	return nil
}

// DeleteOldAttempts applies retention: delivered past 7 days, failed past 30.
func (r *WebPushRepository) DeleteOldAttempts(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM web_push_delivery_attempts
		WHERE (outcome = 'delivered' AND attempted_at < $1)
		   OR (outcome = 'failed' AND attempted_at < $2)`,
		now.AddDate(0, 0, -7), now.AddDate(0, 0, -30))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
