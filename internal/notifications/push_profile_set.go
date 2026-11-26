package notifications

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/profileedits"
)

// A profile set wake tells every phone of an account that a profile from its
// profile set changed on this server, so the app syncs without waiting to be
// opened. It is a background push that shows nothing and carries nothing a
// push does not carry already (docs/notifications-push-api.md).
const (
	// profileSetCollapseID is the collapse ID of every wake: a phone that
	// was offline gets one, however many were sent.
	profileSetCollapseID = "profile-set"
	// profileSetWakeInterval is the debounce: at most one wake per account
	// in this long, on every API server.
	profileSetWakeInterval = time.Minute
	// profileSetEnqueueTimeout bounds the enqueue, which runs before the
	// edit's response completes.
	profileSetEnqueueTimeout = 5 * time.Second
	// profileSetFinalizeTimeout bounds the write that records a wake its
	// send left unfinished.
	profileSetFinalizeTimeout = 5 * time.Second
	// profileSetWakeStale is when retention marks a wake whose send never
	// finished (the process stopped mid-send) as failed.
	profileSetWakeStale = 10 * time.Minute
	// profileSetWakeRetention is how long a wake attempt is kept.
	profileSetWakeRetention = 7 * 24 * time.Hour
)

// profileSetRelayRequests is the most relay requests one phone's send can
// make, each bounded by pushRelayRequestTimeout: preparing the relay
// credential can renew it and then replace it (2); the send can post twice,
// once more without the push target or after idempotency_key_reuse (2); a
// rejected credential can be renewed and replaced again (2), though the
// replacement cooldown allows one replace per process, so preparing and
// recovering make at most 3 credential requests together; and the send can
// post twice again (2). That is 3 + 4.
const profileSetRelayRequests = 7

// profileSetPhoneTimeout bounds one phone's whole send. It is at least the
// sum of the relay requests' own timeouts, so it never cuts off a send those
// timeouts would let finish; it ends a send stuck elsewhere, such as on the
// database. Each phone gets its own, so a slow one costs the others nothing.
var profileSetPhoneTimeout = profileSetRelayRequests*pushRelayRequestTimeout + 5*time.Second

// A wake is sent once and never retried: the next edit or the app opening
// repairs a missed one. Its row is never in a state any server's claim
// queries select (ClaimDuePushAttempts takes pending rows a minute old and
// retrying rows whose next_retry_at has passed, ClaimPushAttemptByID and
// ClaimPendingPushForDelivery take pending rows). An older server's sweep
// would send a claimed wake as a visible alert. So the row is inserted in
// flight, as retrying with no next_retry_at, which no claim selects since
// NULL <= now() is never true, and the send records delivered or failed.

// profileSetWakeMaxPhones caps the phones one wake reaches: the account's
// most recently seen registrations. A wake writes one attempt and makes one
// relay call per phone, and a person can set one off from their own
// session, so its fan-out is bounded. A household's phones, tablets and
// computers stay well under it.
const profileSetWakeMaxPhones = 20

// EnqueueProfileSetWake queues a wake for the registered, enabled phones of
// account userID on platforms whose login is live, at most
// profileSetWakeMaxPhones of them, most recently seen first, unless the
// account had one queued in the last profileSetWakeInterval. queued reports
// whether a wake is queued now or was inside that interval, sent or not;
// sendNow lists the attempts it created, in flight, for the caller to send.
//
// minRevision, when not nil, is the first profile_set_edits revision the
// calling request recorded: the wake goes out only when an edit row at or
// above it exists, that is, when one of the request's edits committed.
//
// One statement decides and records the wake in push_profile_set_wakes, so
// edits recorded at the same moment on two API servers queue one wake, and
// removing and registering a device again does not reset the limit. The
// time is recorded in the transaction that queues the attempts: a failure
// to queue rolls it back, and an account whose phones all went away in the
// meantime records nothing.
func (r *PushDeviceRepository) EnqueueProfileSetWake(ctx context.Context, userID int, platforms []string, minRevision *int64) (queued bool, sendNow []PushDeliveryAttempt, err error) {
	if r == nil || r.pool == nil || len(platforms) == 0 {
		return false, nil, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("begin profile set wake: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if minRevision != nil {
		var committed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM profile_set_edits WHERE user_id = $1 AND revision >= $2)`,
			userID, *minRevision).Scan(&committed); err != nil {
			return false, nil, fmt.Errorf("check profile set edit: %w", err)
		}
		if !committed {
			return false, nil, nil
		}
	}
	devices, err := listProfileSetWakeDevices(ctx, tx, userID, platforms)
	if err != nil || len(devices) == 0 {
		return false, nil, err
	}
	due, err := recordProfileSetWake(ctx, tx, userID)
	if err != nil {
		return false, nil, err
	}
	if !due {
		// A wake went out inside the interval: this edit is covered by it.
		return true, nil, nil
	}

	attempts := make([]PushDeliveryAttempt, 0, len(devices))
	ids := make([]string, 0, len(devices))
	for _, device := range devices {
		id := ulid.Make().String()
		ids = append(ids, id)
		attempts = append(attempts, PushDeliveryAttempt{
			ID:           id,
			PushDeviceID: device.ID,
			TriggerType:  PushTriggerProfileSet,
			Provider:     device.Provider,
			Platform:     device.Platform,
		})
	}
	if err := enqueuePushAttempts(ctx, tx, attempts, PushOutcomeRetrying); err != nil {
		return false, nil, err
	}
	// A device removed since the listing got no attempt.
	rows, err := tx.Query(ctx, `SELECT `+pushAttemptColumns+` FROM push_delivery_attempts WHERE id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return false, nil, fmt.Errorf("read profile set wake attempts: %w", err)
	}
	if sendNow, err = scanPushDeliveryAttempts(rows); err != nil {
		return false, nil, fmt.Errorf("read profile set wake attempts: %w", err)
	}
	if len(sendNow) == 0 {
		return false, nil, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, nil, fmt.Errorf("commit profile set wake: %w", err)
	}
	return true, sendNow, nil
}

// listProfileSetWakeDevices lists the phones a wake of userID reaches: its
// enabled registrations on platforms whose login is live, the
// profileSetWakeMaxPhones most recently seen.
func listProfileSetWakeDevices(ctx context.Context, tx pgx.Tx, userID int, platforms []string) ([]PushDevice, error) {
	rows, err := tx.Query(ctx, `SELECT `+pushDeviceColumns+`
		FROM push_devices
		WHERE user_id = $1
		  AND platform = ANY($2)
		  AND provider = $3
		  AND push_mode = $4
		  AND enabled
		  AND `+pushDeviceLoginLive+`
		ORDER BY last_seen_at DESC NULLS LAST, created_at DESC, id
		LIMIT $5`,
		userID, platforms, PushProviderSiloRelay, PushModePrivatePush, profileSetWakeMaxPhones)
	if err != nil {
		return nil, fmt.Errorf("list profile set wake devices: %w", err)
	}
	devices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PushDevice, error) {
		device, err := scanPushDevice(row)
		if err != nil {
			return PushDevice{}, err
		}
		return *device, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list profile set wake devices: %w", err)
	}
	return devices, nil
}

// recordProfileSetWake records a wake of userID now, unless one was
// recorded within profileSetWakeInterval, and reports whether it recorded
// one. It is one statement: a second server recording at the same moment
// waits on the account's row and then finds the new time.
func recordProfileSetWake(ctx context.Context, tx pgx.Tx, userID int) (bool, error) {
	rows, err := tx.Query(ctx, `INSERT INTO push_profile_set_wakes AS w (user_id, last_wake_at)
		VALUES ($1, now())
		ON CONFLICT (user_id) DO UPDATE SET last_wake_at = excluded.last_wake_at
		WHERE w.last_wake_at <= now() - $2::interval
		RETURNING w.user_id`, userID, profileSetWakeInterval)
	if err != nil {
		return false, fmt.Errorf("record profile set wake: %w", err)
	}
	recorded, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return false, fmt.Errorf("record profile set wake: %w", err)
	}
	return len(recorded) > 0, nil
}

// RetireProfileSetWakes marks wakes whose send never finished as failed,
// deletes wakes past their retention, and deletes wake times a day old.
// Wakes name no notification delivery, so delivery retention never reaches
// them.
func (r *PushDeviceRepository) RetireProfileSetWakes(ctx context.Context) (int64, error) {
	if _, err := r.pool.Exec(ctx, `UPDATE push_delivery_attempts
		SET outcome = $2, failure_message = 'wake send did not finish', updated_at = now()
		WHERE trigger_type = $1 AND outcome = $3 AND next_retry_at IS NULL AND created_at < now() - $4::interval`,
		PushTriggerProfileSet, PushOutcomeFailed, PushOutcomeRetrying, profileSetWakeStale); err != nil {
		return 0, fmt.Errorf("fail unfinished profile set wakes: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM push_delivery_attempts
		WHERE trigger_type = $1 AND created_at < now() - $2::interval`,
		PushTriggerProfileSet, profileSetWakeRetention)
	if err != nil {
		return 0, fmt.Errorf("delete old profile set wakes: %w", err)
	}
	// A wake time only matters for profileSetWakeInterval; this also clears
	// the rows of deleted accounts.
	if _, err := r.pool.Exec(ctx, `DELETE FROM push_profile_set_wakes WHERE last_wake_at < now() - interval '1 day'`); err != nil {
		return 0, fmt.Errorf("delete old profile set wake times: %w", err)
	}
	return tag.RowsAffected(), nil
}

// WakeProfileSetDevices is the profileedits.Waker: it wakes account
// userID's phones after a profile from its profile set changed on this
// server. It reports whether a wake was queued for at least one device, or
// already was inside the debounce interval, even if that earlier send
// failed: the phone has been told, or will be within the minute, and a
// failed send is repaired by the app opening. It answers false when push
// delivery is off for every platform, the account has no registered phone,
// or the request that calls it recorded edits none of which committed.
//
// It returns once the wake is queued, which takes at most
// profileSetEnqueueTimeout, and sends it in the background. It never fails
// the caller: errors are logged.
func (s *System) WakeProfileSetDevices(ctx context.Context, userID int) bool {
	if s == nil || s.pushDeviceRepo == nil || s.pushSender == nil || userID <= 0 {
		return false
	}
	ctx = context.WithoutCancel(ctx)
	enqueueCtx, cancel := context.WithTimeout(ctx, profileSetEnqueueTimeout)
	defer cancel()
	platforms := s.Settings.EnabledPushPlatforms(enqueueCtx)
	if len(platforms) == 0 {
		return false
	}
	var minRevision *int64
	if revision, ok := profileedits.FromContext(ctx).FirstRevision(userID); ok {
		minRevision = &revision
	}
	queued, sendNow, err := s.pushDeviceRepo.EnqueueProfileSetWake(enqueueCtx, userID, platforms, minRevision)
	if err != nil {
		s.logger.WarnContext(ctx, "queueing profile set wake failed", "user_id", userID, "error", err)
		return false
	}
	if len(sendNow) > 0 {
		s.sendProfileSetWakeDetached(ctx, userID, sendNow)
	}
	return queued
}

// failUnfinishedProfileSetWake records as failed a wake its send left in
// flight: its deadline passed, or it stopped before writing an outcome. A
// row the send did finish is left as it is.
func (r *PushDeviceRepository) failUnfinishedProfileSetWake(ctx context.Context, attemptID, reason string) error {
	if _, err := r.pool.Exec(ctx, `UPDATE push_delivery_attempts
		SET outcome = $2, attempt_number = attempt_number + 1, attempted_at = now(),
		    upstream_reason = $3, failure_message = $3, updated_at = now()
		WHERE id = $1 AND trigger_type = $4 AND outcome = $5 AND next_retry_at IS NULL`,
		attemptID, PushOutcomeFailed, reason, PushTriggerProfileSet, PushOutcomeRetrying); err != nil {
		return fmt.Errorf("fail unfinished profile set wake: %w", err)
	}
	return nil
}

// sendProfileSetWakeDetached sends the wake on its own goroutine, as the
// service's other detached sends do (PostServerChannelRequestEvent): the
// caller never waits for the relay. Each phone is sent in turn under its own
// deadline, and a send that did not record an outcome, a panicking one
// included, is recorded failed on a fresh context its deadline cannot have
// canceled, so no wake row outlives its send in flight and every phone is
// tried. Only a process that stops mid-send leaves one, which no claim
// selects, the queue gauge leaves out, and retention marks failed.
func (s *System) sendProfileSetWakeDetached(ctx context.Context, userID int, attempts []PushDeliveryAttempt) {
	ctx = context.WithoutCancel(ctx)
	go func() {
		for _, attempt := range attempts {
			s.sendProfileSetWakeTo(ctx, userID, attempt)
		}
	}()
}

// sendProfileSetWakeTo sends one phone's wake and records its outcome. Its
// recover covers what runs after the send, the failure write and its log,
// which run outside the HTTP recoverer: a panic there must not end the
// process or skip the account's other phones.
func (s *System) sendProfileSetWakeTo(ctx context.Context, userID int, attempt PushDeliveryAttempt) {
	defer func() {
		if p := recover(); p != nil {
			s.logProfileSetWakePanic(ctx, userID, attempt.ID, p)
		}
	}()
	finished, timedOut, panicked := s.attemptProfileSetWake(ctx, userID, attempt)
	if !panicked && finished != nil && finished.Outcome != PushOutcomeRetrying {
		return
	}
	reason := "wake_send_unfinished"
	if timedOut && !panicked {
		reason = "wake_send_timed_out"
	}
	finalizeCtx, cancelFinalize := context.WithTimeout(ctx, profileSetFinalizeTimeout)
	defer cancelFinalize()
	if err := s.pushDeviceRepo.failUnfinishedProfileSetWake(finalizeCtx, attempt.ID, reason); err != nil {
		s.logger.WarnContext(ctx, "recording an unfinished profile set wake failed", "attempt_id", attempt.ID, "error", err)
	}
}

// attemptProfileSetWake runs one phone's send under its deadline, turning a
// panic in it into an unfinished send.
func (s *System) attemptProfileSetWake(ctx context.Context, userID int, attempt PushDeliveryAttempt) (finished *PushDeliveryAttempt, timedOut, panicked bool) {
	phoneCtx, cancel := context.WithTimeout(ctx, profileSetPhoneTimeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			s.logProfileSetWakePanic(ctx, userID, attempt.ID, p)
			finished, panicked = nil, true
		}
	}()
	finished = s.pushSender.processAttempt(phoneCtx, attempt)
	return finished, phoneCtx.Err() != nil, false
}

// logProfileSetWakePanic logs a panic in a wake send by its type only: the
// send handles the decrypted push token and the relay credential, so the
// panic's text could carry either.
func (s *System) logProfileSetWakePanic(ctx context.Context, userID int, attemptID string, p any) {
	s.logger.ErrorContext(ctx, "profile set wake send panicked",
		"user_id", userID, "attempt_id", attemptID, "panic_type", fmt.Sprintf("%T", p))
}
