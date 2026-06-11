package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Email notification modes. The channel is account-level: email addresses
// live on users, not profiles, so one setting covers every profile on the
// account and the worker collapses cross-profile duplicates.
const (
	EmailModeOff         = "off"
	EmailModePerEpisode  = "per_episode"
	EmailModeDailyDigest = "daily_digest"
)

// ValidEmailMode reports whether mode is a recognized email mode value.
func ValidEmailMode(mode string) bool {
	switch mode {
	case EmailModeOff, EmailModePerEpisode, EmailModeDailyDigest:
		return true
	}
	return false
}

// EmailPrefs is one account's email notification state: the user-chosen mode
// plus the worker's dispatch watermark and failure backoff counters.
type EmailPrefs struct {
	UserID              int
	Mode                string
	WatermarkCreatedAt  time.Time
	WatermarkID         string
	LastDigestAt        *time.Time
	LastAttemptAt       *time.Time
	ConsecutiveFailures int
}

// emailRecipient pairs an active prefs row with the account's email address.
type emailRecipient struct {
	EmailPrefs
	Email string
}

// EmailPrefsRepository owns notification_email_prefs.
type EmailPrefsRepository struct {
	pool *pgxpool.Pool
}

// NewEmailPrefsRepository creates an EmailPrefsRepository.
func NewEmailPrefsRepository(pool *pgxpool.Pool) *EmailPrefsRepository {
	return &EmailPrefsRepository{pool: pool}
}

// Get returns the account's email prefs; missing rows default to mode off.
func (r *EmailPrefsRepository) Get(ctx context.Context, userID int) (EmailPrefs, error) {
	prefs := EmailPrefs{UserID: userID, Mode: EmailModeOff}
	err := r.pool.QueryRow(ctx, `
		SELECT mode, watermark_created_at, watermark_id, last_digest_at,
		       last_attempt_at, consecutive_failures
		FROM notification_email_prefs WHERE user_id = $1`,
		userID,
	).Scan(&prefs.Mode, &prefs.WatermarkCreatedAt, &prefs.WatermarkID,
		&prefs.LastDigestAt, &prefs.LastAttemptAt, &prefs.ConsecutiveFailures)
	if errors.Is(err, pgx.ErrNoRows) {
		return prefs, nil
	}
	if err != nil {
		return prefs, fmt.Errorf("get email prefs: %w", err)
	}
	return prefs, nil
}

// SetMode upserts the account's email mode. Enabling from off (or creating
// the row) resets the watermark to now so the backlog never floods a fresh
// opt-in, and clears failure backoff so the first send happens promptly.
func (r *EmailPrefsRepository) SetMode(ctx context.Context, userID int, mode string) error {
	if !ValidEmailMode(mode) {
		return fmt.Errorf("invalid email mode %q", mode)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_email_prefs (user_id, mode)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET
			mode = EXCLUDED.mode,
			watermark_created_at = CASE
				WHEN notification_email_prefs.mode = 'off' THEN now()
				ELSE notification_email_prefs.watermark_created_at
			END,
			watermark_id = CASE
				WHEN notification_email_prefs.mode = 'off' THEN ''
				ELSE notification_email_prefs.watermark_id
			END,
			last_attempt_at = NULL,
			consecutive_failures = 0,
			updated_at = now()`,
		userID, mode)
	if err != nil {
		return fmt.Errorf("set email mode: %w", err)
	}
	return nil
}

// ListActiveRecipients returns every account with email notifications on and
// a usable address. Disabled or deleted accounts drop out of the join.
func (r *EmailPrefsRepository) ListActiveRecipients(ctx context.Context) ([]emailRecipient, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.user_id, p.mode, p.watermark_created_at, p.watermark_id,
		       p.last_digest_at, p.last_attempt_at, p.consecutive_failures,
		       u.email
		FROM notification_email_prefs p
		JOIN users u ON u.id = p.user_id AND u.enabled AND COALESCE(u.email, '') <> ''
		WHERE p.mode <> 'off'
		ORDER BY p.user_id`)
	if err != nil {
		return nil, fmt.Errorf("list email recipients: %w", err)
	}
	defer rows.Close()
	out := make([]emailRecipient, 0, 8)
	for rows.Next() {
		var rec emailRecipient
		if err := rows.Scan(&rec.UserID, &rec.Mode, &rec.WatermarkCreatedAt, &rec.WatermarkID,
			&rec.LastDigestAt, &rec.LastAttemptAt, &rec.ConsecutiveFailures, &rec.Email); err != nil {
			return nil, fmt.Errorf("scan email recipient: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// claimForUpdate locks the account's prefs row for one dispatch attempt.
// SKIP LOCKED makes concurrent nodes pass over each other's in-flight users
// instead of double-sending; (nil, nil) means another node holds the row.
func (r *EmailPrefsRepository) claimForUpdate(ctx context.Context, tx pgx.Tx, userID int) (*EmailPrefs, error) {
	prefs := EmailPrefs{UserID: userID}
	err := tx.QueryRow(ctx, `
		SELECT mode, watermark_created_at, watermark_id, last_digest_at,
		       last_attempt_at, consecutive_failures
		FROM notification_email_prefs
		WHERE user_id = $1
		FOR UPDATE SKIP LOCKED`,
		userID,
	).Scan(&prefs.Mode, &prefs.WatermarkCreatedAt, &prefs.WatermarkID,
		&prefs.LastDigestAt, &prefs.LastAttemptAt, &prefs.ConsecutiveFailures)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim email prefs: %w", err)
	}
	return &prefs, nil
}

// markSent advances the watermark past everything the email covered and
// resets failure backoff. digestAt is non-nil for digest sends (including
// empty digests, so eligibility stops re-checking until tomorrow).
func (r *EmailPrefsRepository) markSent(ctx context.Context, tx pgx.Tx, userID int, watermark Cursor, digestAt *time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_email_prefs SET
			watermark_created_at = $2,
			watermark_id = $3,
			last_digest_at = COALESCE($4, last_digest_at),
			last_attempt_at = now(),
			consecutive_failures = 0,
			updated_at = now()
		WHERE user_id = $1`,
		userID, watermark.CreatedAt, watermark.ID, digestAt)
	if err != nil {
		return fmt.Errorf("mark email sent: %w", err)
	}
	return nil
}

// markFailure records a failed send for backoff; the watermark stays put so
// the next eligible pass retries the same items.
func (r *EmailPrefsRepository) markFailure(ctx context.Context, tx pgx.Tx, userID int) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_email_prefs SET
			last_attempt_at = now(),
			consecutive_failures = consecutive_failures + 1,
			updated_at = now()
		WHERE user_id = $1`,
		userID)
	if err != nil {
		return fmt.Errorf("mark email failure: %w", err)
	}
	return nil
}

// HasDeliveriesSince reports whether the account has any delivery newer than
// the watermark. Cheap pre-check (index-only) so the per-episode sweep does
// not open a claim transaction for idle accounts every pass.
func (r *EmailPrefsRepository) HasDeliveriesSince(ctx context.Context, userID int, since Cursor) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM notification_deliveries
			WHERE user_id = $1 AND (created_at, id) > ($2, $3)
		)`,
		userID, since.CreatedAt, since.ID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check deliveries since watermark: %w", err)
	}
	return exists, nil
}
