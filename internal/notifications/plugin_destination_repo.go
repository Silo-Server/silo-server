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

// PluginDestinationRepository owns notification_plugin_destinations and
// plugin_destination_delivery_attempts. Every profile-facing query matches
// the account as well as the profile: profile IDs repeat across accounts.
type PluginDestinationRepository struct {
	pool *pgxpool.Pool
}

// NewPluginDestinationRepository creates a PluginDestinationRepository.
func NewPluginDestinationRepository(pool *pgxpool.Pool) *PluginDestinationRepository {
	return &PluginDestinationRepository{pool: pool}
}

const pluginDestinationColumns = `
	id, user_id, profile_id, plugin_id, capability_id, name, config_ciphertext, enabled,
	notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up, notify_requests,
	consecutive_failures, disabled_reason, last_success_at, last_failure_at, last_failure_message,
	created_at, updated_at, revision`

// pluginDestinationAfterCursor continues a page after (created_at, id).
const pluginDestinationAfterCursor = ` AND (created_at, id) > ($3, $4)`

const pluginDestinationAttemptColumns = `
	id, notification_delivery_id, destination_id, attempt_number, attempted_at,
	next_retry_at, outcome, failure_message`

func scanPluginDestination(row pgx.Row) (*PluginDestination, error) {
	var d PluginDestination
	err := row.Scan(
		&d.ID, &d.UserID, &d.ProfileID, &d.PluginID, &d.CapabilityID, &d.Name, &d.ConfigCiphertext, &d.Enabled,
		&d.NotifyFavorites, &d.NotifyWatchlist, &d.NotifyContinueWatching, &d.NotifyNextUp, &d.NotifyRequests,
		&d.ConsecutiveFailures, &d.DisabledReason, &d.LastSuccessAt, &d.LastFailureAt, &d.LastFailureMessage,
		&d.CreatedAt, &d.UpdatedAt, &d.Revision,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func scanPluginDestinations(rows pgx.Rows) ([]PluginDestination, error) {
	defer rows.Close()
	out := make([]PluginDestination, 0, 4)
	for rows.Next() {
		d, err := scanPluginDestination(rows)
		if err != nil {
			return nil, fmt.Errorf("scan plugin destination: %w", err)
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func scanPluginDestinationAttempts(rows pgx.Rows) ([]DeliveryAttempt, error) {
	defer rows.Close()
	out := make([]DeliveryAttempt, 0, 8)
	for rows.Next() {
		var a DeliveryAttempt
		if err := rows.Scan(&a.ID, &a.NotificationDeliveryID, &a.TargetID, &a.AttemptNumber, &a.AttemptedAt,
			&a.NextRetryAt, &a.Outcome, &a.FailureMessage); err != nil {
			return nil, fmt.Errorf("scan plugin destination attempt: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

var errPluginDestinationNameTaken = errors.New("a destination with this name already exists")

func isPluginDestinationNameViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "notification_plugin_destinations_name_key"
}

// InsertWithLimit stores a new destination unless the profile already has
// maxPerProfile. The count and insert run under an advisory lock on the
// account profile, so concurrent creates can't both pass the check.
func (r *PluginDestinationRepository) InsertWithLimit(ctx context.Context, d PluginDestination, maxPerProfile int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin plugin destination insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('notification_plugin_destinations:' || $1::integer || ':' || $2, 0))`,
		d.UserID, d.ProfileID); err != nil {
		return fmt.Errorf("lock plugin destination quota: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2`,
		d.UserID, d.ProfileID).Scan(&count); err != nil {
		return fmt.Errorf("count plugin destinations: %w", err)
	}
	if count >= maxPerProfile {
		return ErrPluginDestinationLimit
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_plugin_destinations
			(id, user_id, profile_id, plugin_id, capability_id, name, config_ciphertext, enabled,
			 notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up, notify_requests)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		d.ID, d.UserID, d.ProfileID, d.PluginID, d.CapabilityID, d.Name, d.ConfigCiphertext, d.Enabled,
		d.NotifyFavorites, d.NotifyWatchlist, d.NotifyContinueWatching, d.NotifyNextUp, d.NotifyRequests); err != nil {
		if isPluginDestinationNameViolation(err) {
			return fmt.Errorf("%w: %s", ErrPluginDestinationInvalid, errPluginDestinationNameTaken.Error())
		}
		return fmt.Errorf("insert plugin destination: %w", err)
	}
	return tx.Commit(ctx)
}

// GetByID returns one of the account profile's destinations, or
// ErrPluginDestinationNotFound.
func (r *PluginDestinationRepository) GetByID(ctx context.Context, userID int, profileID, id string) (*PluginDestination, error) {
	d, err := scanPluginDestination(r.pool.QueryRow(ctx,
		`SELECT `+pluginDestinationColumns+` FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2 AND id = $3`,
		userID, profileID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPluginDestinationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get plugin destination: %w", err)
	}
	return d, nil
}

// getByIDUnscoped loads a destination for the delivery path; (nil, nil) when
// it was deleted.
func (r *PluginDestinationRepository) getByIDUnscoped(ctx context.Context, id string) (*PluginDestination, error) {
	d, err := scanPluginDestination(r.pool.QueryRow(ctx,
		`SELECT `+pluginDestinationColumns+` FROM notification_plugin_destinations WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get plugin destination: %w", err)
	}
	return d, nil
}

// ListPage returns at most limit of the account profile's destinations in
// creation order, after the cursor.
func (r *PluginDestinationRepository) ListPage(ctx context.Context, userID int, profileID string, limit int, after *Cursor) ([]PluginDestination, error) {
	query := `SELECT ` + pluginDestinationColumns + ` FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2`
	args := []any{userID, profileID}
	if after != nil {
		query += pluginDestinationAfterCursor
		args = append(args, after.CreatedAt, after.ID)
	}
	query += fmt.Sprintf(" ORDER BY created_at, id LIMIT $%d", len(args)+1)
	args = append(args, limit)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list plugin destinations: %w", err)
	}
	return scanPluginDestinations(rows)
}

// ListEnabledByProfiles loads enabled destinations for a set of account
// profiles inside the delivery's transaction.
func (r *PluginDestinationRepository) ListEnabledByProfiles(ctx context.Context, tx pgx.Tx, profiles []profileKey) (map[profileKey][]PluginDestination, error) {
	out := make(map[profileKey][]PluginDestination, len(profiles))
	if len(profiles) == 0 {
		return out, nil
	}
	userIDs, profileIDs := profileKeyArrays(profiles)
	rows, err := tx.Query(ctx,
		`SELECT `+pluginDestinationColumns+` FROM notification_plugin_destinations
		WHERE (user_id, profile_id) IN (SELECT * FROM unnest($1::int[], $2::text[])) AND enabled`,
		userIDs, profileIDs)
	if err != nil {
		return nil, fmt.Errorf("list enabled plugin destinations: %w", err)
	}
	destinations, err := scanPluginDestinations(rows)
	if err != nil {
		return nil, err
	}
	for _, d := range destinations {
		key := profileKey{d.UserID, d.ProfileID}
		out[key] = append(out[key], d)
	}
	return out, nil
}

// UpdateGuarded reads, checks and rewrites a destination's configuration
// under one row lock. Delivery bookkeeping is never copied from an editor.
func (r *PluginDestinationRepository) UpdateGuarded(ctx context.Context, userID int, profileID, id string, apply func(*PluginDestination) error) (*PluginDestination, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	d, err := scanPluginDestination(tx.QueryRow(ctx,
		`SELECT `+pluginDestinationColumns+` FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2 AND id = $3 FOR UPDATE`,
		userID, profileID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPluginDestinationNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := apply(d); err != nil {
		return nil, err
	}
	updated, err := scanPluginDestination(tx.QueryRow(ctx, `
		UPDATE notification_plugin_destinations SET
			name = $4, config_ciphertext = $5, enabled = $6,
			notify_favorites = $7, notify_watchlist = $8, notify_continue_watching = $9,
			notify_next_up = $10, notify_requests = $11,
			consecutive_failures = $12, disabled_reason = $13,
			revision = revision + 1, updated_at = now()
		WHERE user_id = $1 AND profile_id = $2 AND id = $3
		RETURNING `+pluginDestinationColumns,
		userID, profileID, id, d.Name, d.ConfigCiphertext, d.Enabled,
		d.NotifyFavorites, d.NotifyWatchlist, d.NotifyContinueWatching, d.NotifyNextUp, d.NotifyRequests,
		d.ConsecutiveFailures, d.DisabledReason))
	if isPluginDestinationNameViolation(err) {
		return nil, fmt.Errorf("%w: %s", ErrPluginDestinationInvalid, errPluginDestinationNameTaken.Error())
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteGuarded removes a destination, and its attempts by cascade, after
// check accepts its revision under the row lock.
func (r *PluginDestinationRepository) DeleteGuarded(ctx context.Context, userID int, profileID, id string, check func(int64) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var revision int64
	err = tx.QueryRow(ctx,
		`SELECT revision FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2 AND id = $3 FOR UPDATE`,
		userID, profileID, id).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPluginDestinationNotFound
	}
	if err != nil {
		return err
	}
	if err := check(revision); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2 AND id = $3`,
		userID, profileID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteAllForProfile removes a deleted account profile's destinations.
func (r *PluginDestinationRepository) DeleteAllForProfile(ctx context.Context, userID int, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2`, userID, profileID)
	return err
}

// RecordSuccess clears the failure streak after a delivered message.
//
// Delivery bookkeeping leaves revision alone: revision guards the owner's
// edits, and a send finishing must not turn an edit in progress into a 412.
func (r *PluginDestinationRepository) RecordSuccess(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_plugin_destinations
		SET last_success_at = now(), consecutive_failures = 0
		WHERE id = $1`, id)
	return err
}

// RecordFailure extends the failure streak and keeps the plugin's reason.
// Like Disable it applies only while the destination still has the settings
// the failed send used (sentConfig): new settings start with a clean streak.
// It leaves revision alone, as RecordSuccess does.
func (r *PluginDestinationRepository) RecordFailure(ctx context.Context, id, message, sentConfig string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_plugin_destinations
		SET last_failure_at = now(), last_failure_message = left($2, 256),
		    consecutive_failures = consecutive_failures + 1
		WHERE id = $1 AND config_ciphertext = $3`, id, message, sentConfig)
	return err
}

// Disable turns a destination off with a reason its owner sees.
//
// It applies only while the destination still has the settings a failed send
// used (sentConfig) and is enabled, and reports whether it did: an owner who
// saved new settings during that send keeps them working. Unlike the other
// bookkeeping it bumps revision: it changes enabled, which the owner edits,
// so an editor that hasn't seen the disable must reload first.
func (r *PluginDestinationRepository) Disable(ctx context.Context, id, reason, sentConfig string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE notification_plugin_destinations
		SET enabled = false, disabled_reason = left($2, 256), revision = revision + 1, updated_at = now()
		WHERE id = $1 AND config_ciphertext = $3 AND enabled`, id, reason, sentConfig)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// EnqueueAttempts inserts pending outbox rows inside the delivery's
// transaction. attempt_number starts at 0: no send tried yet.
func (r *PluginDestinationRepository) EnqueueAttempts(ctx context.Context, tx pgx.Tx, attempts []DeliveryAttempt) error {
	if len(attempts) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`INSERT INTO plugin_destination_delivery_attempts
		(id, notification_delivery_id, destination_id, attempt_number, outcome) VALUES `)
	args := make([]any, 0, len(attempts)*5)
	for i, attempt := range attempts {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := len(args)
		fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5)
		args = append(args, attempt.ID, attempt.NotificationDeliveryID, attempt.TargetID, 0, WebhookOutcomePending)
	}
	sb.WriteString(" ON CONFLICT DO NOTHING")
	if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("enqueue plugin destination attempts: %w", err)
	}
	return nil
}

// pluginDestinationClaimLease hides a claimed attempt from other claimers.
// A claim holds at most pluginDestinationClaimLimit attempts, sent one after
// another with pluginSendTimeout each, so the lease outlasts a whole claim
// and a send still running is not claimed again; a server that dies mid-send
// frees its attempts when the lease runs out.
const pluginDestinationClaimLease = 5 * time.Minute

// pluginDestinationClaimLimit bounds one claim: 10 sends at the 15-second
// limit take 150 seconds, half the lease. A profile has at most
// PluginDestinationMaxPerProfile destinations, so one delivery fits.
const pluginDestinationClaimLimit = 10

// ClaimPendingForDelivery claims a delivery's pending attempts for the
// post-commit send.
func (r *PluginDestinationRepository) ClaimPendingForDelivery(ctx context.Context, deliveryID string) ([]DeliveryAttempt, error) {
	return r.claim(ctx, `
		UPDATE plugin_destination_delivery_attempts SET outcome = 'retrying', next_retry_at = now() + $2
		WHERE id IN (
			SELECT id FROM plugin_destination_delivery_attempts
			WHERE notification_delivery_id = $1 AND outcome = 'pending'
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+pluginDestinationAttemptColumns,
		deliveryID, pluginDestinationClaimLease, pluginDestinationClaimLimit)
}

// ClaimDue claims retries that are due, and pending rows whose post-commit
// send never ran.
func (r *PluginDestinationRepository) ClaimDue(ctx context.Context, limit int) ([]DeliveryAttempt, error) {
	return r.claim(ctx, `
		UPDATE plugin_destination_delivery_attempts SET outcome = 'retrying', next_retry_at = now() + $2
		WHERE id IN (
			SELECT id FROM plugin_destination_delivery_attempts
			WHERE (outcome = 'retrying' AND next_retry_at <= now())
			   OR (outcome = 'pending' AND attempted_at <= now() - interval '60 seconds')
			ORDER BY next_retry_at NULLS FIRST
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+pluginDestinationAttemptColumns,
		limit, pluginDestinationClaimLease)
}

func (r *PluginDestinationRepository) claim(ctx context.Context, query string, args ...any) ([]DeliveryAttempt, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("claim plugin destination attempts: %w", err)
	}
	return scanPluginDestinationAttempts(rows)
}

// FinalizeAttempt records a send's outcome, the attempt number it used, and
// when to try again, if the claim still holds: the attempt's lease is the one
// claimed returned. It reports false when the lease ran out and another
// server claimed the attempt since, whose result then stands.
func (r *PluginDestinationRepository) FinalizeAttempt(ctx context.Context, claimed DeliveryAttempt, outcome string, attemptNumber int, failureMessage string, nextRetryAt *time.Time) (bool, error) {
	var message *string
	if failureMessage != "" {
		message = &failureMessage
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE plugin_destination_delivery_attempts
		SET outcome = $2, attempt_number = $3, attempted_at = now(),
		    failure_message = left($4, 256), next_retry_at = $5
		WHERE id = $1 AND outcome = 'retrying' AND next_retry_at = $6`,
		claimed.ID, outcome, attemptNumber, message, nextRetryAt, claimed.NextRetryAt)
	if err != nil {
		return false, fmt.Errorf("finalize plugin destination attempt: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DeleteOldAttempts keeps delivered attempts 7 days and failed ones 30, as
// for webhooks. Unresolved attempts stay until they resolve.
func (r *PluginDestinationRepository) DeleteOldAttempts(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM plugin_destination_delivery_attempts
		WHERE (outcome = 'delivered' AND attempted_at < $1)
		   OR (outcome IN ('failed', 'auto_disabled') AND attempted_at < $2)`,
		now.AddDate(0, 0, -7), now.AddDate(0, 0, -30))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
