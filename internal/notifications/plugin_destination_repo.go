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
	notify_next_up, notify_requests,
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
		&d.NotifyNextUp, &d.NotifyRequests,
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

// ErrPluginDestinationNameTaken means the profile already has a destination
// with that name. It is also an ErrPluginDestinationInvalid.
var ErrPluginDestinationNameTaken = errors.New("a destination with this name already exists")

func isPluginDestinationNameViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "notification_plugin_destinations_name_key"
}

// InsertWithLimit stores a new destination unless the profile already has
// maxPerProfile. The count and insert run under an advisory lock on the
// account profile, so concurrent creates can't both pass the check.
//
// The account row is key-share locked first, the one row lock this takes,
// as the insert's foreign key would: an account deletion locks that row
// before its cascade reaches the profile and its destinations, so the two
// wait on each other in one order and can't deadlock. An account deleted
// before that refuses the create with ErrPluginDestinationProfileGone.
// Profiles may live outside PostgreSQL, so the profile is checked by the
// service, through the user store, not here.
func (r *PluginDestinationRepository) InsertWithLimit(ctx context.Context, d PluginDestination, maxPerProfile int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin plugin destination insert: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var accountExists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 FOR KEY SHARE)`, d.UserID).Scan(&accountExists); err != nil {
		return fmt.Errorf("lock plugin destination account: %w", err)
	}
	if !accountExists {
		return ErrPluginDestinationProfileGone
	}
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('notification_plugin_destinations:' || $1::integer || ':' || $2, 0))`,
		d.UserID, d.ProfileID); err != nil {
		return fmt.Errorf("lock plugin destination quota: %w", err)
	}
	// A taken name is checked before the limit, so a full profile still
	// answers a duplicate name as one.
	var count int
	var nameTaken bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*), coalesce(bool_or(name = $3), false) FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2`,
		d.UserID, d.ProfileID, d.Name).Scan(&count, &nameTaken); err != nil {
		return fmt.Errorf("count plugin destinations: %w", err)
	}
	if nameTaken {
		return fmt.Errorf("%w: %w", ErrPluginDestinationInvalid, ErrPluginDestinationNameTaken)
	}
	if count >= maxPerProfile {
		return ErrPluginDestinationLimit
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_plugin_destinations
			(id, user_id, profile_id, plugin_id, capability_id, name, config_ciphertext, enabled,
			 notify_next_up, notify_requests)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		d.ID, d.UserID, d.ProfileID, d.PluginID, d.CapabilityID, d.Name, d.ConfigCiphertext, d.Enabled,
		d.NotifyNextUp, d.NotifyRequests); err != nil {
		if isPluginDestinationNameViolation(err) {
			return fmt.Errorf("%w: %w", ErrPluginDestinationInvalid, ErrPluginDestinationNameTaken)
		}
		return fmt.Errorf("insert plugin destination: %w", err)
	}
	return tx.Commit(ctx)
}

// deleteByID removes a destination, and its attempts by cascade.
func (r *PluginDestinationRepository) deleteByID(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM notification_plugin_destinations WHERE id = $1`, id)
	return err
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
// The lock is FOR NO KEY UPDATE, since the row's key doesn't change: it
// doesn't wait for a delivery transaction's key-share lock on the row.
func (r *PluginDestinationRepository) UpdateGuarded(ctx context.Context, userID int, profileID, id string, apply func(*PluginDestination) error) (*PluginDestination, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	d, err := scanPluginDestination(tx.QueryRow(ctx,
		`SELECT `+pluginDestinationColumns+` FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2 AND id = $3 FOR NO KEY UPDATE`,
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
			notify_next_up = $7, notify_requests = $8,
			consecutive_failures = $9, disabled_reason = $10,
			revision = revision + 1, updated_at = now()
		WHERE user_id = $1 AND profile_id = $2 AND id = $3
		RETURNING `+pluginDestinationColumns,
		userID, profileID, id, d.Name, d.ConfigCiphertext, d.Enabled,
		d.NotifyNextUp, d.NotifyRequests,
		d.ConsecutiveFailures, d.DisabledReason))
	if isPluginDestinationNameViolation(err) {
		return nil, fmt.Errorf("%w: %w", ErrPluginDestinationInvalid, ErrPluginDestinationNameTaken)
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

// DeleteAllForProfile removes a deleted account profile's destinations (see
// deleteProfileTargetsInOrder for the locking).
func (r *PluginDestinationRepository) DeleteAllForProfile(ctx context.Context, userID int, profileID string) error {
	return deleteProfileTargetsInOrder(ctx, r.pool, "notification_plugin_destinations", userID, profileID)
}

// deleteProfileTargetsInOrder deletes an account profile's rows of a
// delivery target table. The table name is a constant of the callers.
//
// It first takes the profile's inbox lock. Every transaction that enqueues
// attempts for a profile's targets holds that lock already (BulkInsert takes
// it before the enqueues, and a fanout takes the locks of all its recipients
// at once), so a purge waits for a whole delivery transaction instead of
// interleaving its row locks with the several enqueue calls one transaction
// makes, which can deadlock whatever order each call locks in.
func deleteProfileTargetsInOrder(ctx context.Context, pool *pgxpool.Pool, table string, userID int, profileID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockInboxProfiles(ctx, tx, []string{profileID}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM `+table+` WHERE user_id = $1 AND profile_id = $2 ORDER BY id FOR UPDATE`, userID, profileID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE user_id = $1 AND profile_id = $2`, userID, profileID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lockForFinish locks a destination row for an attempt's bookkeeping. A
// destination deleted meanwhile locks nothing; its attempt cascaded.
func (r *PluginDestinationRepository) lockForFinish(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM notification_plugin_destinations WHERE id = $1 FOR NO KEY UPDATE`, id)
	return err
}

// pluginDestinationExec runs a statement on the pool or in a transaction.
type pluginDestinationExec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RecordSuccess clears the failure streak after a delivered message, while
// the destination is at sentRevision, the revision the send used.
//
// Delivery bookkeeping leaves revision alone: revision guards the owner's
// edits, and a send finishing must not turn an edit in progress into a 412.
func (r *PluginDestinationRepository) RecordSuccess(ctx context.Context, q pluginDestinationExec, id string, sentRevision int64) error {
	_, err := q.Exec(ctx, `
		UPDATE notification_plugin_destinations
		SET last_success_at = now(), consecutive_failures = 0
		WHERE id = $1 AND revision = $2`, id, sentRevision)
	return err
}

// RecordFailure extends the failure streak and keeps the plugin's reason,
// while the destination is at sentRevision: an owner who edited it during
// the send starts with a clean streak. It leaves revision alone, as
// RecordSuccess does.
func (r *PluginDestinationRepository) RecordFailure(ctx context.Context, q pluginDestinationExec, id, message string, sentRevision int64) error {
	_, err := q.Exec(ctx, `
		UPDATE notification_plugin_destinations
		SET last_failure_at = now(), last_failure_message = left($2, 256),
		    consecutive_failures = consecutive_failures + 1
		WHERE id = $1 AND revision = $3`, id, message, sentRevision)
	return err
}

// Disable turns a destination off with a reason its owner sees.
//
// It applies only while the destination is enabled and at sentRevision, the
// revision a failed send used, and reports whether it did: an owner who
// edited it during that send keeps the edit working. With quietSince, it
// applies only when the destination had no successful send and no owner
// edit since then (updated_at moves on every edit, and on a disable). Unlike
// the other bookkeeping it bumps revision: it changes enabled, which the
// owner edits, so an editor that hasn't seen the disable must reload first.
func (r *PluginDestinationRepository) Disable(ctx context.Context, q pluginDestinationExec, id, reason string, sentRevision int64, quietSince *time.Time) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE notification_plugin_destinations
		SET enabled = false, disabled_reason = left($2, 256), revision = revision + 1, updated_at = now()
		WHERE id = $1 AND revision = $3 AND enabled
		  AND ($4::timestamptz IS NULL
		       OR ((last_success_at IS NULL OR last_success_at < $4) AND updated_at < $4))`, id, reason, sentRevision, quietSince)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// EnqueueAttempts inserts pending outbox rows inside the delivery's
// transaction and returns the deliveries it queued an attempt for, the only
// ones worth handing to the dispatcher. attempt_number starts at 0: no send
// tried yet.
//
// The destinations were read without a lock, so one may be deleted before
// this insert. Its foreign key would then abort the whole delivery
// transaction, inbox row and every other channel included. The insert
// instead key-share locks the destinations that still exist and queues only
// for those: a deletion committed first drops its attempt, and one after
// waits for this transaction and then cascades. The lock blocks nothing but
// deletion and key changes, and the delivery transaction makes no plugin
// call.
func (r *PluginDestinationRepository) EnqueueAttempts(ctx context.Context, tx pgx.Tx, attempts []DeliveryAttempt) (map[string]bool, error) {
	return enqueueAttemptsForLiveTargets(ctx, tx, "plugin_destination_delivery_attempts", "destination_id", "notification_plugin_destinations", attempts)
}

// enqueueAttemptsForLiveTargets inserts pending attempts into an outbox
// table for the targets that still exist, key-share locking them until the
// transaction ends (see PluginDestinationRepository.EnqueueAttempts), and
// returns the delivery IDs it inserted an attempt for. The caller holds the
// recipients' inbox locks, which a profile purge takes before it locks the
// profile's targets (see deleteProfileTargetsInOrder). The table and column
// names are constants of the callers.
func enqueueAttemptsForLiveTargets(ctx context.Context, tx pgx.Tx, table, targetColumn, targetTable string, attempts []DeliveryAttempt) (map[string]bool, error) {
	if len(attempts) == 0 {
		return nil, nil
	}
	ids := make([]string, len(attempts))
	deliveries := make([]string, len(attempts))
	targets := make([]string, len(attempts))
	for i, attempt := range attempts {
		ids[i], deliveries[i], targets[i] = attempt.ID, attempt.NotificationDeliveryID, attempt.TargetID
	}
	query := `WITH live AS (
			SELECT t.id FROM ` + targetTable + ` t WHERE t.id = ANY($3::text[])
			ORDER BY t.id
			FOR KEY SHARE
		)
		INSERT INTO ` + table + ` (id, notification_delivery_id, ` + targetColumn + `, attempt_number, outcome)
		SELECT a.id, a.delivery_id, live.id, 0, $4
		FROM unnest($1::text[], $2::text[], $3::text[]) AS a(id, delivery_id, target_id)
		JOIN live ON live.id = a.target_id
		ON CONFLICT DO NOTHING
		RETURNING notification_delivery_id`
	rows, err := tx.Query(ctx, query, ids, deliveries, targets, WebhookOutcomePending)
	if err != nil {
		return nil, fmt.Errorf("enqueue %s: %w", table, err)
	}
	queued, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("enqueue %s: %w", table, err)
	}
	out := make(map[string]bool, len(queued))
	for _, id := range queued {
		out[id] = true
	}
	return out, nil
}

// pluginDestinationClaimLease hides a claimed attempt from other claimers.
// A claim holds at most pluginDestinationClaimLimit attempts, sent one after
// another, each with at most pluginConnectTimeout to reach its plugin and
// pluginSendTimeout to send, so the lease outlasts a whole claim; a server
// that dies mid-send frees its attempts when the lease runs out.
const pluginDestinationClaimLease = 5 * time.Minute

// pluginDestinationClaimLimit bounds one claim: 10 attempts at 10 seconds to
// connect and 15 to send take at most 250 seconds, inside the lease. A
// profile has at most PluginDestinationMaxPerProfile destinations, so one
// delivery fits.
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

// ClaimHeld reports whether this server's claim on an attempt still holds
// with at least remaining left on its lease, so a send started now finishes
// inside it. Checked just before the send: a claim that ran out may already
// be another server's.
func (r *PluginDestinationRepository) ClaimHeld(ctx context.Context, claimed DeliveryAttempt, remaining time.Duration) (bool, error) {
	var held bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM plugin_destination_delivery_attempts
			WHERE id = $1 AND outcome = 'retrying' AND next_retry_at = $2 AND next_retry_at > now() + $3
		)`, claimed.ID, claimed.NextRetryAt, remaining).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("check plugin destination claim: %w", err)
	}
	return held, nil
}

// FinalizeAttempt records a send's outcome, the attempt number it used, and
// when to try again, if the claim still holds: the attempt's lease is the one
// claimed returned. It reports false when the lease ran out and another
// server claimed the attempt since, whose result then stands.
func (r *PluginDestinationRepository) FinalizeAttempt(ctx context.Context, q pluginDestinationExec, claimed DeliveryAttempt, outcome string, attemptNumber int, failureMessage string, nextRetryAt *time.Time) (bool, error) {
	var message *string
	if failureMessage != "" {
		message = &failureMessage
	}
	tag, err := q.Exec(ctx, `
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
