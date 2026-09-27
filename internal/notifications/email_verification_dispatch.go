package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5"
)

// Delivery policy for retained verification messages (user decision, 2026-09-07):
// an uncertain send is retried once with the same message, accepting a possible
// duplicate email over a lost verification. There is no hold-for-manual path.
const (
	// emailVerificationMaxAttempts bounds hand-offs per intent: the original
	// send plus exactly one retry after failure or uncertainty.
	emailVerificationMaxAttempts = 2
	// emailVerificationClaimLease is how long a 'sending' claim stays owned.
	// Past it the claim is an uncertain send: the worker crashed (or lost its
	// node) between hand-off and record. SMTP send timeout is 30s.
	emailVerificationClaimLease   = 3 * time.Minute
	emailVerificationPollInterval = time.Minute
	emailVerificationNudgeDelay   = time.Second
	// emailVerificationReceiptRetention keeps the receipt row (without payload)
	// after expiry so exact replays still answer current=false instead of
	// admitting the same UUID as a fresh intent.
	emailVerificationReceiptRetention = 30 * 24 * time.Hour
)

// emailVerificationDispatchStates mirrors the CHECK constraint.
const (
	dispatchQueued    = "queued"
	dispatchSending   = "sending"
	dispatchDelivered = "delivered"
	dispatchFailed    = "failed"
)

var errEmailVerificationCrashInjected = errors.New("crash injected between send and record")

// emailVerificationClaim is one claimed outbox row plus the pending-state
// observations needed to authorize the send under the claim.
type emailVerificationClaim struct {
	ID        string
	UserID    int
	ProfileID string
	TokenHash string
	ExpiresAt time.Time
	Attempts  int
	Payload   string
	// Pending state read from notification_email_prefs at claim time.
	PrefsUser  int
	PrefsHash  string
	PrefsFound bool
}

// ClaimVerificationDispatch claims the oldest dispatchable row for this worker:
// a 'queued' row whose last claim (if any) is older than the lease, or a
// 'sending' row whose lease lapsed (uncertain send). The claim increments the
// attempt counter so a crash after hand-off is counted. Returns nil, nil when
// nothing is claimable.
func (r *EmailPrefsRepository) ClaimVerificationDispatch(ctx context.Context, now time.Time) (*emailVerificationClaim, error) {
	if r == nil || r.pool == nil {
		return nil, ErrEmailVerificationUnavailable
	}
	stale := now.Add(-emailVerificationClaimLease)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	c := &emailVerificationClaim{}
	err = tx.QueryRow(ctx, `
		UPDATE notification_email_verifications v
		SET dispatch_state=$3, dispatch_attempts=dispatch_attempts+1, dispatch_claimed_at=$1
		WHERE v.id = (
			SELECT id FROM notification_email_verifications
			WHERE dispatch_attempts < $4
			  AND (dispatch_claimed_at IS NULL OR dispatch_claimed_at < $2)
			  AND dispatch_state IN ($5, $3)
			ORDER BY created_at, id
			LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING v.id, v.user_id, v.profile_id, v.pending_token_hash, v.expires_at, v.dispatch_attempts, v.payload_ciphertext`,
		now, stale, dispatchSending, emailVerificationMaxAttempts, dispatchQueued,
	).Scan(&c.ID, &c.UserID, &c.ProfileID, &c.TokenHash, &c.ExpiresAt, &c.Attempts, &c.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim verification dispatch: %w", err)
	}
	err = tx.QueryRow(ctx, `SELECT user_id,pending_token_hash FROM notification_email_prefs WHERE profile_id=$1`, c.ProfileID).Scan(&c.PrefsUser, &c.PrefsHash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		c.PrefsFound = false
	case err != nil:
		return nil, fmt.Errorf("read pending state under claim: %w", err)
	default:
		c.PrefsFound = true
	}
	return c, tx.Commit(ctx)
}

// RecordVerificationDispatch moves a claimed row to a terminal state. It is
// keyed on the claim's attempt count so a lease-expired duplicate claim cannot
// be overwritten by the original worker finishing late.
func (r *EmailPrefsRepository) RecordVerificationDispatch(ctx context.Context, id string, attempts int, state, reason string, now time.Time) error {
	if state != dispatchDelivered && state != dispatchFailed {
		return fmt.Errorf("record verification dispatch: %q is not terminal", state)
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_email_verifications
		SET dispatch_state=$2, dispatch_error=$3, dispatch_completed_at=$4
		WHERE id=$1 AND dispatch_state=$5 AND dispatch_attempts=$6`,
		id, state, reason, now, dispatchSending, attempts)
	if err != nil {
		return fmt.Errorf("record verification dispatch: %w", err)
	}
	return nil
}

// RequeueVerificationDispatch returns a claimed row to 'queued' after a
// provider rejection so a later pass retries the same message. The claim time
// stays set, which backs the retry off by one lease; the attempt already
// counted, and the max-attempts guard in the claim bounds it.
func (r *EmailPrefsRepository) RequeueVerificationDispatch(ctx context.Context, id string, attempts int, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_email_verifications
		SET dispatch_state=$2, dispatch_error=$3
		WHERE id=$1 AND dispatch_state=$4 AND dispatch_attempts=$5`,
		id, dispatchQueued, reason, dispatchSending, attempts)
	if err != nil {
		return fmt.Errorf("requeue verification dispatch: %w", err)
	}
	return nil
}

// ExhaustVerificationDispatch marks rows that can no longer be claimed as
// failed: an uncertain send that already used every attempt, or a queued row
// whose link expired before any worker reached it. Returns rows changed.
func (r *EmailPrefsRepository) ExhaustVerificationDispatch(ctx context.Context, now time.Time) (int64, error) {
	stale := now.Add(-emailVerificationClaimLease)
	tag, err := r.pool.Exec(ctx, `
		UPDATE notification_email_verifications
		SET dispatch_state=$2,
		    dispatch_completed_at=$1,
		    dispatch_error=CASE
		        WHEN dispatch_state=$3 THEN 'uncertain after '||dispatch_attempts||' attempts'
		        ELSE 'expired before dispatch' END
		WHERE (dispatch_state=$3 AND dispatch_claimed_at < $4 AND dispatch_attempts >= $5)
		   OR (dispatch_state=$6 AND expires_at < $1)`,
		now, dispatchFailed, dispatchSending, stale, emailVerificationMaxAttempts, dispatchQueued)
	if err != nil {
		return 0, fmt.Errorf("exhaust verification dispatch: %w", err)
	}
	return tag.RowsAffected(), nil
}

// VerificationDispatchState reports a row's dispatch outcome for tests and
// diagnostics.
type VerificationDispatchState struct {
	State       string
	Attempts    int
	Error       string
	CompletedAt *time.Time
	HasPayload  bool
}

func (r *EmailPrefsRepository) VerificationDispatchState(ctx context.Context, id string) (VerificationDispatchState, error) {
	var s VerificationDispatchState
	err := r.pool.QueryRow(ctx, `SELECT dispatch_state,dispatch_attempts,dispatch_error,dispatch_completed_at,payload_ciphertext<>'' FROM notification_email_verifications WHERE id=$1`, id).Scan(&s.State, &s.Attempts, &s.Error, &s.CompletedAt, &s.HasPayload)
	return s, err
}

// RetireVerificationDispatch applies bounded retention: the encrypted payload
// is dropped from terminal rows once the link itself has expired (the receipt
// stays), and whole rows go once expiry is older than the receipt window.
func (r *EmailPrefsRepository) RetireVerificationDispatch(ctx context.Context, now time.Time) (payloads, rows int64, err error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE notification_email_verifications SET payload_ciphertext=''
		WHERE payload_ciphertext<>'' AND dispatch_state IN ($1,$2) AND expires_at < $3`,
		dispatchDelivered, dispatchFailed, now)
	if err != nil {
		return 0, 0, fmt.Errorf("retire verification payloads: %w", err)
	}
	payloads = tag.RowsAffected()
	tag, err = r.pool.Exec(ctx, `DELETE FROM notification_email_verifications WHERE expires_at < $1`, now.Add(-emailVerificationReceiptRetention))
	if err != nil {
		return payloads, 0, fmt.Errorf("retire verification receipts: %w", err)
	}
	return payloads, tag.RowsAffected(), nil
}

// emailVerificationDispatcher drains the verification outbox: claim, authorize
// under the claim, hand off to the provider, record. One message per claim;
// nodes compete through SKIP LOCKED, so it is safe on every serving node.
type emailVerificationDispatcher struct {
	repo   *EmailPrefsRepository
	cipher *secret.Cipher
	sender mail.Sender
	logger *slog.Logger
	nudge  chan struct{}
	now    func() time.Time
	// afterSend runs between provider hand-off and record. Tests inject a
	// crash here; production leaves it nil.
	afterSend func(ctx context.Context, id string) error
}

func newEmailVerificationDispatcher(repo *EmailPrefsRepository, cipher *secret.Cipher, sender mail.Sender) *emailVerificationDispatcher {
	return &emailVerificationDispatcher{
		repo:   repo,
		cipher: cipher,
		sender: sender,
		logger: slog.Default().With("component", "notifications.email_verification"),
		nudge:  make(chan struct{}, 1),
		now:    time.Now,
	}
}

// Available reports whether the provider can currently accept hand-offs. It
// backs the capability's dispatch_available and says nothing about delivery.
func (d *emailVerificationDispatcher) Available(ctx context.Context) bool {
	return d != nil && d.sender != nil && d.sender.Enabled(ctx)
}

// Nudge schedules a near-term pass after admission. Non-blocking.
func (d *emailVerificationDispatcher) Nudge() {
	if d == nil {
		return
	}
	select {
	case d.nudge <- struct{}{}:
	default:
	}
}

// Run drains the outbox until ctx is canceled.
func (d *emailVerificationDispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(emailVerificationPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.nudge:
			select {
			case <-ctx.Done():
				return
			case <-time.After(emailVerificationNudgeDelay):
			}
		}
		d.runPass(ctx)
	}
}

// runPass exhausts unclaimable rows, then claims and sends until the outbox is
// empty or the provider reports it is unavailable.
func (d *emailVerificationDispatcher) runPass(ctx context.Context) {
	now := d.now()
	if n, err := d.repo.ExhaustVerificationDispatch(ctx, now); err != nil {
		d.logger.ErrorContext(ctx, "email verification exhaust failed", "error", err)
		return
	} else if n > 0 {
		d.logger.WarnContext(ctx, "email verification intents failed without delivery", "count", n)
	}
	for ctx.Err() == nil {
		claim, err := d.repo.ClaimVerificationDispatch(ctx, d.now())
		if err != nil {
			d.logger.ErrorContext(ctx, "email verification claim failed", "error", err)
			return
		}
		if claim == nil {
			return
		}
		if err := d.dispatch(ctx, claim); err != nil {
			if errors.Is(err, errEmailVerificationCrashInjected) {
				return
			}
			d.logger.ErrorContext(ctx, "email verification dispatch failed", "intent", claim.ID, "attempt", claim.Attempts, "error", err)
			if errors.Is(err, mail.ErrNotConfigured) {
				return
			}
		}
	}
}

// dispatch authorizes one claim, hands the retained message to the provider
// and records the outcome. The message and its link never change between
// attempts; the RFC 5322 Message-ID is derived from the intent so a duplicate
// after uncertainty carries the same identity for receivers that dedupe.
func (d *emailVerificationDispatcher) dispatch(ctx context.Context, c *emailVerificationClaim) error {
	now := d.now()
	fail := func(reason string) error {
		return d.repo.RecordVerificationDispatch(ctx, c.ID, c.Attempts, dispatchFailed, reason, now)
	}
	switch {
	case !c.PrefsFound:
		return fail("profile preferences removed before dispatch")
	case c.PrefsUser != c.UserID:
		return fail("profile owner changed before dispatch")
	case c.PrefsHash != c.TokenHash:
		return fail("verification superseded or cleared before dispatch")
	case !c.ExpiresAt.After(now):
		return fail("verification expired before dispatch")
	case c.Payload == "":
		return fail("retained message already retired")
	}
	plaintext, err := d.cipher.Decrypt(c.Payload, emailVerificationAAD(c.ID))
	if err != nil {
		return fail("retained message unreadable: " + err.Error())
	}
	var msg mail.Message
	if err := json.Unmarshal([]byte(plaintext), &msg); err != nil {
		return fail("retained message malformed: " + err.Error())
	}
	if msg.Headers == nil {
		msg.Headers = map[string]string{}
	}
	msg.Headers["Message-ID"] = emailVerificationMessageID(c.ID)

	sendErr := d.sender.Send(ctx, msg)
	if d.afterSend != nil {
		if err := d.afterSend(ctx, c.ID); err != nil {
			return err
		}
	}
	if sendErr == nil {
		if err := d.repo.RecordVerificationDispatch(ctx, c.ID, c.Attempts, dispatchDelivered, "", d.now()); err != nil {
			return err
		}
		return nil
	}
	// A provider error is not proof of non-acceptance (the DATA stream may
	// have been accepted before the connection dropped). Under the accepted
	// duplicate policy every error path retries once with the same message.
	reason := fmt.Sprintf("attempt %d: %v", c.Attempts, sendErr)
	if c.Attempts >= emailVerificationMaxAttempts {
		if err := fail(reason); err != nil {
			return err
		}
		return sendErr
	}
	if err := d.repo.RequeueVerificationDispatch(ctx, c.ID, c.Attempts, reason); err != nil {
		return err
	}
	return sendErr
}

// emailVerificationMessageID is the provider-facing idempotency identity. SMTP
// has no idempotency key; the Message-ID header is the closest equivalent and
// stays constant across attempts for one intent.
func emailVerificationMessageID(intentID string) string {
	return "<" + intentID + "@silo.email-verification>"
}
