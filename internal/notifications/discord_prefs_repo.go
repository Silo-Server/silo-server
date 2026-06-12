package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DiscordPrefs is one account's Discord DM notification state as the API
// surfaces it: the OAuth-linked identity, the user-chosen mode, and the last
// delivery failure (for link health in the settings UI).
type DiscordPrefs struct {
	UserID          int
	DiscordUserID   string
	DiscordUsername string
	Mode            string
	LinkFailure     string
}

// Linked reports whether the account has completed the Discord OAuth link.
func (p DiscordPrefs) Linked() bool { return p.DiscordUserID != "" }

// DiscordPrefsRepository owns notification_discord_prefs and the one-time
// link-state rows for the OAuth flow.
type DiscordPrefsRepository struct {
	pool *pgxpool.Pool
}

// NewDiscordPrefsRepository creates a DiscordPrefsRepository.
func NewDiscordPrefsRepository(pool *pgxpool.Pool) *DiscordPrefsRepository {
	return &DiscordPrefsRepository{pool: pool}
}

// Get returns the account's Discord prefs; missing rows default to mode off
// and no linked identity.
func (r *DiscordPrefsRepository) Get(ctx context.Context, userID int) (DiscordPrefs, error) {
	prefs := DiscordPrefs{UserID: userID, Mode: ChannelModeOff}
	err := r.pool.QueryRow(ctx, `
		SELECT discord_user_id, discord_username, mode, link_failure
		FROM notification_discord_prefs WHERE user_id = $1`,
		userID,
	).Scan(&prefs.DiscordUserID, &prefs.DiscordUsername, &prefs.Mode, &prefs.LinkFailure)
	if errors.Is(err, pgx.ErrNoRows) {
		return prefs, nil
	}
	if err != nil {
		return prefs, fmt.Errorf("get discord prefs: %w", err)
	}
	return prefs, nil
}

// SetIdentity stores the OAuth-linked Discord identity. The watermark resets
// to now so the backlog never floods a fresh link, the DM channel cache is
// cleared (the identity may have changed), and failure state is wiped so the
// first send happens promptly. An existing mode survives a re-link.
func (r *DiscordPrefsRepository) SetIdentity(ctx context.Context, userID int, discordUserID, discordUsername string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_discord_prefs (user_id, discord_user_id, discord_username)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET
			discord_user_id = EXCLUDED.discord_user_id,
			discord_username = EXCLUDED.discord_username,
			dm_channel_id = '',
			watermark_created_at = now(),
			watermark_id = '',
			last_attempt_at = NULL,
			consecutive_failures = 0,
			link_failure = '',
			updated_at = now()`,
		userID, discordUserID, discordUsername)
	if err != nil {
		return fmt.Errorf("set discord identity: %w", err)
	}
	return nil
}

// ClearIdentity unlinks the Discord account and switches the channel off.
func (r *DiscordPrefsRepository) ClearIdentity(ctx context.Context, userID int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_discord_prefs SET
			discord_user_id = '',
			discord_username = '',
			dm_channel_id = '',
			mode = 'off',
			last_attempt_at = NULL,
			consecutive_failures = 0,
			link_failure = '',
			updated_at = now()
		WHERE user_id = $1`,
		userID)
	if err != nil {
		return fmt.Errorf("clear discord identity: %w", err)
	}
	return nil
}

// SetMode updates the account's Discord mode. Enabling from off resets the
// watermark to now so the backlog never floods a fresh opt-in, and clears
// failure backoff so the first send happens promptly. Returns
// ErrDiscordNotLinked when no linked identity exists.
func (r *DiscordPrefsRepository) SetMode(ctx context.Context, userID int, mode string) error {
	if !ValidChannelMode(mode) {
		return fmt.Errorf("invalid discord mode %q", mode)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE notification_discord_prefs SET
			mode = $2,
			watermark_created_at = CASE
				WHEN notification_discord_prefs.mode = 'off' THEN now()
				ELSE notification_discord_prefs.watermark_created_at
			END,
			watermark_id = CASE
				WHEN notification_discord_prefs.mode = 'off' THEN ''
				ELSE notification_discord_prefs.watermark_id
			END,
			last_attempt_at = NULL,
			consecutive_failures = 0,
			updated_at = now()
		WHERE user_id = $1 AND discord_user_id <> ''`,
		userID, mode)
	if err != nil {
		return fmt.Errorf("set discord mode: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Switching off without a linked row is already the desired state
		// (e.g. a mode update racing an unlink); only enabling needs a link.
		if mode == ChannelModeOff {
			return nil
		}
		return ErrDiscordNotLinked
	}
	return nil
}

// ListActiveRecipients returns every linked account with Discord DMs on.
// Disabled or deleted accounts drop out of the join.
func (r *DiscordPrefsRepository) ListActiveRecipients(ctx context.Context) ([]accountRecipient[int], error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.user_id, p.mode, p.watermark_created_at, p.watermark_id,
		       p.last_digest_at, p.last_attempt_at, p.consecutive_failures
		FROM notification_discord_prefs p
		JOIN users u ON u.id = p.user_id AND u.enabled
		WHERE p.mode <> 'off' AND p.discord_user_id <> ''
		ORDER BY p.user_id`)
	if err != nil {
		return nil, fmt.Errorf("list discord recipients: %w", err)
	}
	defer rows.Close()
	out := make([]accountRecipient[int], 0, 8)
	for rows.Next() {
		var rec accountRecipient[int]
		if err := rows.Scan(&rec.Key, &rec.Mode, &rec.WatermarkCreatedAt, &rec.WatermarkID,
			&rec.LastDigestAt, &rec.LastAttemptAt, &rec.ConsecutiveFailures); err != nil {
			return nil, fmt.Errorf("scan discord recipient: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// claimForUpdate locks the account's prefs row for one dispatch attempt.
// SKIP LOCKED makes concurrent nodes pass over each other's in-flight users
// instead of double-sending; (nil, nil) means another node holds the row.
func (r *DiscordPrefsRepository) claimForUpdate(ctx context.Context, tx pgx.Tx, userID int) (*accountRecipient[int], error) {
	rec := accountRecipient[int]{Key: userID}
	err := tx.QueryRow(ctx, `
		SELECT mode, watermark_created_at, watermark_id, last_digest_at,
		       last_attempt_at, consecutive_failures
		FROM notification_discord_prefs
		WHERE user_id = $1 AND discord_user_id <> ''
		FOR UPDATE SKIP LOCKED`,
		userID,
	).Scan(&rec.Mode, &rec.WatermarkCreatedAt, &rec.WatermarkID,
		&rec.LastDigestAt, &rec.LastAttemptAt, &rec.ConsecutiveFailures)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim discord prefs: %w", err)
	}
	return &rec, nil
}

// identityForSend reads the locked row's identity fields inside the claim
// transaction, so the send targets exactly the identity the lock covers.
func (r *DiscordPrefsRepository) identityForSend(ctx context.Context, tx pgx.Tx, userID int) (discordUserID, dmChannelID string, err error) {
	err = tx.QueryRow(ctx, `
		SELECT discord_user_id, dm_channel_id
		FROM notification_discord_prefs WHERE user_id = $1`,
		userID,
	).Scan(&discordUserID, &dmChannelID)
	if err != nil {
		return "", "", fmt.Errorf("read discord identity: %w", err)
	}
	return discordUserID, dmChannelID, nil
}

// cacheDMChannel stores a freshly opened DM channel ID under the claim lock.
func (r *DiscordPrefsRepository) cacheDMChannel(ctx context.Context, tx pgx.Tx, userID int, channelID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_discord_prefs SET dm_channel_id = $2, updated_at = now()
		WHERE user_id = $1`,
		userID, channelID)
	if err != nil {
		return fmt.Errorf("cache dm channel: %w", err)
	}
	return nil
}

// markSent advances the watermark past everything the DM covered and resets
// failure and link-health state. digestAt is non-nil for digest sends.
func (r *DiscordPrefsRepository) markSent(ctx context.Context, tx pgx.Tx, userID int, watermark Cursor, digestAt *time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_discord_prefs SET
			watermark_created_at = $2,
			watermark_id = $3,
			last_digest_at = COALESCE($4, last_digest_at),
			last_attempt_at = now(),
			consecutive_failures = 0,
			link_failure = '',
			updated_at = now()
		WHERE user_id = $1`,
		userID, watermark.CreatedAt, watermark.ID, digestAt)
	if err != nil {
		return fmt.Errorf("mark discord sent: %w", err)
	}
	return nil
}

// markFailure records a failed send for backoff and surfaces the failure in
// the settings UI; the watermark stays put so the next eligible pass retries
// the same items.
func (r *DiscordPrefsRepository) markFailure(ctx context.Context, tx pgx.Tx, userID int, message string) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_discord_prefs SET
			last_attempt_at = now(),
			consecutive_failures = consecutive_failures + 1,
			link_failure = $2,
			updated_at = now()
		WHERE user_id = $1`,
		userID, message)
	if err != nil {
		return fmt.Errorf("mark discord failure: %w", err)
	}
	return nil
}

// CreateLinkState records a one-time OAuth state row for the link flow.
func (r *DiscordPrefsRepository) CreateLinkState(ctx context.Context, state string, userID int, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_discord_link_state (state, user_id, expires_at)
		VALUES ($1, $2, $3)`,
		state, userID, expiresAt)
	if err != nil {
		return fmt.Errorf("create discord link state: %w", err)
	}
	return nil
}

// ConsumeLinkState atomically deletes and returns an unexpired link-state
// row, making each state single-use. ok is false for unknown, already-used,
// or expired states.
func (r *DiscordPrefsRepository) ConsumeLinkState(ctx context.Context, state string) (userID int, ok bool, err error) {
	err = r.pool.QueryRow(ctx, `
		DELETE FROM notification_discord_link_state
		WHERE state = $1 AND expires_at > now()
		RETURNING user_id`,
		state,
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("consume discord link state: %w", err)
	}
	return userID, true, nil
}

// DeleteExpiredLinkStates reaps abandoned link-flow rows (retention).
func (r *DiscordPrefsRepository) DeleteExpiredLinkStates(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM notification_discord_link_state WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired discord link states: %w", err)
	}
	return tag.RowsAffected(), nil
}
