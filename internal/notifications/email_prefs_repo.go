package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Email notification modes. The channel is profile-level: each profile owns
// its mode, dispatch watermark, and verified destination address. There is
// deliberately no fallback to the login account's email — without it, every
// profile of a household would funnel mail to the account holder. A profile
// receives nothing until its own address is verified. The values are the
// shared account-channel modes.
const (
	EmailModeOff         = ChannelModeOff
	EmailModePerEpisode  = ChannelModePerEpisode
	EmailModeDailyDigest = ChannelModeDailyDigest
)

// Verification-send rate limits: a minimum gap between sends plus a daily
// cap, so the server cannot be used to spray arbitrary addresses.
const (
	emailVerifyMinInterval = time.Minute
	emailVerifyDailyCap    = 10
)

// EmailPrefs is one profile's email notification state: the chosen mode, the
// custom-address verification state, and the worker's dispatch watermark and
// failure backoff counters.
type EmailPrefs struct {
	ProfileID           string
	UserID              int
	Mode                string
	CustomEmail         string
	PendingEmail        string
	PendingExpiresAt    *time.Time
	WatermarkCreatedAt  time.Time
	WatermarkID         string
	LastDigestAt        *time.Time
	LastAttemptAt       *time.Time
	ConsecutiveFailures int
}

// EmailPrefsRepository owns notification_email_prefs.
type EmailPrefsRepository struct {
	pool *pgxpool.Pool
}

// NewEmailPrefsRepository creates an EmailPrefsRepository.
func NewEmailPrefsRepository(pool *pgxpool.Pool) *EmailPrefsRepository {
	return &EmailPrefsRepository{pool: pool}
}

// Get returns the profile's email prefs; missing rows default to mode off.
func (r *EmailPrefsRepository) Get(ctx context.Context, profileID string) (EmailPrefs, error) {
	prefs := EmailPrefs{ProfileID: profileID, Mode: EmailModeOff}
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, mode, custom_email, pending_email, pending_expires_at,
		       watermark_created_at, watermark_id, last_digest_at,
		       last_attempt_at, consecutive_failures
		FROM notification_email_prefs WHERE profile_id = $1`,
		profileID,
	).Scan(&prefs.UserID, &prefs.Mode, &prefs.CustomEmail, &prefs.PendingEmail,
		&prefs.PendingExpiresAt, &prefs.WatermarkCreatedAt, &prefs.WatermarkID,
		&prefs.LastDigestAt, &prefs.LastAttemptAt, &prefs.ConsecutiveFailures)
	if errors.Is(err, pgx.ErrNoRows) {
		return prefs, nil
	}
	if err != nil {
		return prefs, fmt.Errorf("get email prefs: %w", err)
	}
	// An expired pending verification is dead; don't surface it.
	if prefs.PendingExpiresAt != nil && prefs.PendingExpiresAt.Before(time.Now()) {
		prefs.PendingEmail = ""
		prefs.PendingExpiresAt = nil
	}
	return prefs, nil
}

// SetMode upserts the profile's email mode. Enabling from off (or creating
// the row) resets the watermark to now so the backlog never floods a fresh
// opt-in, and clears failure backoff so the first send happens promptly.
// The unsubscribe token is not minted here: the send path backfills it under
// the claim lock right before the first email that embeds it.
func (r *EmailPrefsRepository) SetMode(ctx context.Context, userID int, profileID, mode string) error {
	if !ValidChannelMode(mode) {
		return fmt.Errorf("invalid email mode %q", mode)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_email_prefs (profile_id, user_id, mode)
		VALUES ($1, $2, $3)
		ON CONFLICT (profile_id) DO UPDATE SET
			mode = EXCLUDED.mode,
			user_id = EXCLUDED.user_id,
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
		profileID, userID, mode)
	if err != nil {
		return fmt.Errorf("set email mode: %w", err)
	}
	return nil
}

// Errors surfaced by the custom-address verification flow.
var (
	ErrEmailVerifyRateLimited = errors.New("verification emails are rate limited; try again later")
	ErrEmailAddressInUse      = errors.New("email address is already in use")
)

// addressInUse reports whether the address is already claimed: verified for
// another profile, or identifying another login account (its email, or a
// username that is an email address). The requesting profile and its own
// account are excluded — pointing a profile at its own account email is the
// expected common case.
func addressInUse(ctx context.Context, q querier, address, profileID string, userID int) (bool, error) {
	var inUse bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM notification_email_prefs
			WHERE lower(custom_email) = lower($1) AND profile_id <> $2
		) OR EXISTS (
			SELECT 1 FROM users
			WHERE id <> $3
			  AND (lower(COALESCE(email, '')) = lower($1) OR lower(username) = lower($1))
		)`,
		address, profileID, userID,
	).Scan(&inUse)
	if err != nil {
		return false, fmt.Errorf("check address in use: %w", err)
	}
	return inUse, nil
}

// querier is the subset of pgx.Tx / pgxpool.Pool the uniqueness check needs.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RequestPendingAddress stores a new pending custom address and its
// verification token, enforcing the resend rate limits atomically. The
// previous pending state (if any) is replaced. The daily cap's "day" is the
// UTC day of pending_last_sent_at: the counter resets on the first request of
// a new day.
func (r *EmailPrefsRepository) RequestPendingAddress(ctx context.Context, userID int, profileID, email, tokenHash string, expiresAt time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin pending address tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lastSentAt *time.Time
	var sendsToday int
	err = tx.QueryRow(ctx, `
		SELECT pending_last_sent_at, verify_sends_today
		FROM notification_email_prefs WHERE profile_id = $1 FOR UPDATE`,
		profileID,
	).Scan(&lastSentAt, &sendsToday)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read verify rate state: %w", err)
	}

	now := time.Now()
	if lastSentAt != nil && now.Sub(*lastSentAt) < emailVerifyMinInterval {
		return ErrEmailVerifyRateLimited
	}
	if lastSentAt == nil || !sameUTCDay(*lastSentAt, now) {
		sendsToday = 0
	}
	if sendsToday >= emailVerifyDailyCap {
		return ErrEmailVerifyRateLimited
	}

	// Friendly early rejection; the authoritative check re-runs at verify
	// time, so a conflict that appears in between still cannot land.
	inUse, err := addressInUse(ctx, tx, email, profileID, userID)
	if err != nil {
		return err
	}
	if inUse {
		return ErrEmailAddressInUse
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO notification_email_prefs
			(profile_id, user_id, pending_email, pending_token_hash,
			 pending_expires_at, pending_last_sent_at, verify_sends_today)
		VALUES ($1, $2, $3, $4, $5, now(), $6)
		ON CONFLICT (profile_id) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			pending_email = EXCLUDED.pending_email,
			pending_token_hash = EXCLUDED.pending_token_hash,
			pending_expires_at = EXCLUDED.pending_expires_at,
			pending_last_sent_at = now(),
			verify_sends_today = EXCLUDED.verify_sends_today,
			updated_at = now()`,
		profileID, userID, email, tokenHash, expiresAt, sendsToday+1)
	if err != nil {
		return fmt.Errorf("store pending address: %w", err)
	}
	return tx.Commit(ctx)
}

// sameUTCDay reports whether both instants fall on the same UTC calendar day.
func sameUTCDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}

// EmailVerifyOutcome classifies one verification-link click.
type EmailVerifyOutcome int

const (
	// EmailVerifyInvalid: unknown, already-used, or expired token.
	EmailVerifyInvalid EmailVerifyOutcome = iota
	// EmailVerifyOK: the pending address is now the verified destination.
	EmailVerifyOK
	// EmailVerifyConflict: the address was claimed by another profile or
	// account after the verification email went out.
	EmailVerifyConflict
)

// ConsumeVerifyToken promotes the pending address matching the token hash to
// the verified custom address. Single-use: the pending state is cleared on
// every outcome except invalid. Uniqueness is re-checked here — request-time
// checks can be raced — and the partial unique index on lower(custom_email)
// backstops concurrent verifications of the same address.
func (r *EmailPrefsRepository) ConsumeVerifyToken(ctx context.Context, tokenHash string) (EmailVerifyOutcome, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return EmailVerifyInvalid, fmt.Errorf("begin verify tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var profileID, pendingEmail string
	var userID int
	err = tx.QueryRow(ctx, `
		SELECT profile_id, user_id, pending_email
		FROM notification_email_prefs
		WHERE pending_token_hash = $1 AND pending_token_hash <> ''
		  AND pending_email <> ''
		  AND (pending_expires_at IS NULL OR pending_expires_at > now())
		FOR UPDATE`,
		tokenHash,
	).Scan(&profileID, &userID, &pendingEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailVerifyInvalid, nil
	}
	if err != nil {
		return EmailVerifyInvalid, fmt.Errorf("look up verify token: %w", err)
	}

	clearPending := func(outcome EmailVerifyOutcome) (EmailVerifyOutcome, error) {
		_, err := tx.Exec(ctx, `
			UPDATE notification_email_prefs SET
				pending_email = '',
				pending_token_hash = '',
				pending_expires_at = NULL,
				updated_at = now()
			WHERE profile_id = $1`,
			profileID)
		if err != nil {
			return EmailVerifyInvalid, fmt.Errorf("clear pending address: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return EmailVerifyInvalid, err
		}
		return outcome, nil
	}

	inUse, err := addressInUse(ctx, tx, pendingEmail, profileID, userID)
	if err != nil {
		return EmailVerifyInvalid, err
	}
	if inUse {
		return clearPending(EmailVerifyConflict)
	}

	_, err = tx.Exec(ctx, `
		UPDATE notification_email_prefs SET
			custom_email = pending_email,
			pending_email = '',
			pending_token_hash = '',
			pending_expires_at = NULL,
			updated_at = now()
		WHERE profile_id = $1`,
		profileID)
	if isUniqueViolation(err) {
		// Lost a same-instant race with another profile verifying the same
		// address; the unique index decided the winner.
		_ = tx.Rollback(ctx)
		return r.consumeConflictLoser(ctx, profileID)
	}
	if err != nil {
		return EmailVerifyInvalid, fmt.Errorf("consume verify token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return EmailVerifyInvalid, err
	}
	return EmailVerifyOK, nil
}

// consumeConflictLoser clears the pending state of a profile that lost a
// concurrent-verification race, in a fresh transaction (the racing one is
// poisoned by the constraint violation).
func (r *EmailPrefsRepository) consumeConflictLoser(ctx context.Context, profileID string) (EmailVerifyOutcome, error) {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_email_prefs SET
			pending_email = '',
			pending_token_hash = '',
			pending_expires_at = NULL,
			updated_at = now()
		WHERE profile_id = $1`,
		profileID)
	if err != nil {
		return EmailVerifyInvalid, fmt.Errorf("clear losing pending address: %w", err)
	}
	return EmailVerifyConflict, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ClearCustomAddress removes the verified address and any in-flight
// verification. The channel switches off in the same statement: without an
// address there is no destination, and leaving the mode on would silently
// re-arm delivery the moment a new address verifies.
func (r *EmailPrefsRepository) ClearCustomAddress(ctx context.Context, profileID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_email_prefs SET
			custom_email = '',
			pending_email = '',
			pending_token_hash = '',
			pending_expires_at = NULL,
			mode = 'off',
			updated_at = now()
		WHERE profile_id = $1`,
		profileID)
	if err != nil {
		return fmt.Errorf("clear custom address: %w", err)
	}
	return nil
}

// UnsubscribeByToken switches the matching profile's email mode off. ok is
// false when no row carries the token.
func (r *EmailPrefsRepository) UnsubscribeByToken(ctx context.Context, token string) (ok bool, err error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE notification_email_prefs SET
			mode = 'off',
			updated_at = now()
		WHERE unsubscribe_token = $1 AND unsubscribe_token <> ''`,
		token)
	if err != nil {
		return false, fmt.Errorf("unsubscribe by token: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteForProfile purges the profile's email prefs (profile deletion).
func (r *EmailPrefsRepository) DeleteForProfile(ctx context.Context, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_email_prefs WHERE profile_id = $1`, profileID)
	if err != nil {
		return fmt.Errorf("delete email prefs: %w", err)
	}
	return nil
}

// ListActiveRecipients returns every profile with email notifications on and
// a verified destination address. Disabled or deleted accounts drop out of
// the join.
func (r *EmailPrefsRepository) ListActiveRecipients(ctx context.Context) ([]accountRecipient[string], error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.profile_id, p.mode, p.watermark_created_at, p.watermark_id,
		       p.last_digest_at, p.last_attempt_at, p.consecutive_failures
		FROM notification_email_prefs p
		JOIN users u ON u.id = p.user_id AND u.enabled
		WHERE p.mode <> 'off' AND p.custom_email <> ''
		ORDER BY p.profile_id`)
	if err != nil {
		return nil, fmt.Errorf("list email recipients: %w", err)
	}
	defer rows.Close()
	out := make([]accountRecipient[string], 0, 8)
	for rows.Next() {
		var rec accountRecipient[string]
		if err := rows.Scan(&rec.Key, &rec.Mode, &rec.WatermarkCreatedAt, &rec.WatermarkID,
			&rec.LastDigestAt, &rec.LastAttemptAt, &rec.ConsecutiveFailures); err != nil {
			return nil, fmt.Errorf("scan email recipient: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// claimForUpdate locks the profile's prefs row for one dispatch attempt.
// SKIP LOCKED makes concurrent nodes pass over each other's in-flight
// profiles instead of double-sending; (nil, nil) means another node holds
// the row.
func (r *EmailPrefsRepository) claimForUpdate(ctx context.Context, tx pgx.Tx, profileID string) (*accountRecipient[string], error) {
	rec := accountRecipient[string]{Key: profileID}
	err := tx.QueryRow(ctx, `
		SELECT mode, watermark_created_at, watermark_id, last_digest_at,
		       last_attempt_at, consecutive_failures
		FROM notification_email_prefs
		WHERE profile_id = $1
		FOR UPDATE SKIP LOCKED`,
		profileID,
	).Scan(&rec.Mode, &rec.WatermarkCreatedAt, &rec.WatermarkID,
		&rec.LastDigestAt, &rec.LastAttemptAt, &rec.ConsecutiveFailures)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim email prefs: %w", err)
	}
	return &rec, nil
}

// destinationForSend resolves where the locked profile's email goes — its
// verified address — plus the row's owner and unsubscribe token. Read under
// the claim so a mid-pass address removal fails cleanly instead of sending
// to a stale recipient.
func (r *EmailPrefsRepository) destinationForSend(ctx context.Context, tx pgx.Tx, profileID string) (email string, userID int, unsubscribeToken string, err error) {
	err = tx.QueryRow(ctx, `
		SELECT p.custom_email, p.user_id, p.unsubscribe_token
		FROM notification_email_prefs p
		JOIN users u ON u.id = p.user_id AND u.enabled
		WHERE p.profile_id = $1`,
		profileID,
	).Scan(&email, &userID, &unsubscribeToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, "", fmt.Errorf("profile %s has no usable account", profileID)
	}
	if err != nil {
		return "", 0, "", fmt.Errorf("resolve email destination: %w", err)
	}
	return email, userID, unsubscribeToken, nil
}

// setUnsubscribeToken backfills a missing unsubscribe token under the claim
// lock (rows migrated from the account-level table start without one).
func (r *EmailPrefsRepository) setUnsubscribeToken(ctx context.Context, tx pgx.Tx, profileID, token string) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_email_prefs SET unsubscribe_token = $2, updated_at = now()
		WHERE profile_id = $1 AND unsubscribe_token = ''`,
		profileID, token)
	if err != nil {
		return fmt.Errorf("set unsubscribe token: %w", err)
	}
	return nil
}

// markSent advances the watermark past everything the email covered and
// resets failure backoff. digestAt is non-nil for digest sends (including
// empty digests, so eligibility stops re-checking until tomorrow).
func (r *EmailPrefsRepository) markSent(ctx context.Context, tx pgx.Tx, profileID string, watermark Cursor, digestAt *time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_email_prefs SET
			watermark_created_at = $2,
			watermark_id = $3,
			last_digest_at = COALESCE($4, last_digest_at),
			last_attempt_at = now(),
			consecutive_failures = 0,
			updated_at = now()
		WHERE profile_id = $1`,
		profileID, watermark.CreatedAt, watermark.ID, digestAt)
	if err != nil {
		return fmt.Errorf("mark email sent: %w", err)
	}
	return nil
}

// markFailure records a failed send for backoff; the watermark stays put so
// the next eligible pass retries the same items.
func (r *EmailPrefsRepository) markFailure(ctx context.Context, tx pgx.Tx, profileID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_email_prefs SET
			last_attempt_at = now(),
			consecutive_failures = consecutive_failures + 1,
			updated_at = now()
		WHERE profile_id = $1`,
		profileID)
	if err != nil {
		return fmt.Errorf("mark email failure: %w", err)
	}
	return nil
}
