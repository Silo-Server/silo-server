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

// profileSetPhoneTimeout bounds one phone's send: a relay call, and a second
// one after the relay credential is recovered (pushRelayRequestTimeout
// each). Each phone gets its own, so a slow one costs the others nothing.
var profileSetPhoneTimeout = 2*pushRelayRequestTimeout + 5*time.Second

// A wake is sent once and never retried: the next edit or the app opening
// repairs a missed one. Its row is never in a state any server's claim
// queries select (ClaimDuePushAttempts takes pending rows a minute old and
// retrying rows whose next_retry_at has passed, ClaimPushAttemptByID and
// ClaimPendingPushForDelivery take pending rows). An older server's sweep
// would send a claimed wake as a visible alert. So the row is inserted in
// flight, as retrying with no next_retry_at, which no claim selects since
// NULL <= now() is never true, and the send records delivered or failed.

// profileSetWakeLock names the advisory lock that serializes one account's
// wakes across API servers; the account ID is its second half.
const profileSetWakeLock = "push_profile_set_wake"

// EnqueueProfileSetWake queues a wake for every registered, enabled phone
// of account userID on platforms whose login is live, unless the account had
// one queued in the last profileSetWakeInterval. queued reports whether a
// wake is queued now or was inside that interval, sent or not; sendNow lists
// the attempts it created, in flight, for the caller to send.
//
// minRevision, when not nil, is the first profile_set_edits revision the
// calling request recorded: the wake goes out only when an edit row at or
// above it exists, that is, when one of the request's edits committed.
//
// The account's advisory lock makes the check and the insert one step on
// every API server, so edits recorded at the same moment on two servers
// queue one wake.
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
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`,
		profileSetWakeLock, userID); err != nil {
		return false, nil, fmt.Errorf("lock profile set wake: %w", err)
	}
	// attempted_at is set at insert and again when the send finishes, so it
	// is never before created_at. Its bound changes no answer; it lets
	// push_delivery_attempts_device_history_idx (push_device_id,
	// attempted_at DESC) read only each device's last minute of attempts
	// instead of its whole alert history.
	var recent bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM push_delivery_attempts a
		JOIN push_devices d ON d.id = a.push_device_id
		WHERE d.user_id = $1 AND a.trigger_type = $2
		  AND a.created_at > now() - $3::interval AND a.attempted_at > now() - $3::interval)`,
		userID, PushTriggerProfileSet, profileSetWakeInterval).Scan(&recent); err != nil {
		return false, nil, fmt.Errorf("check recent profile set wake: %w", err)
	}
	if recent {
		return true, nil, nil
	}

	rows, err := tx.Query(ctx, `SELECT `+pushDeviceColumns+`
		FROM push_devices
		WHERE user_id = $1
		  AND platform = ANY($2)
		  AND provider = $3
		  AND push_mode = $4
		  AND enabled
		  AND `+pushDeviceLoginLive+`
		ORDER BY id`,
		userID, platforms, PushProviderSiloRelay, PushModePrivatePush)
	if err != nil {
		return false, nil, fmt.Errorf("list profile set wake devices: %w", err)
	}
	devices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PushDevice, error) {
		device, err := scanPushDevice(row)
		if err != nil {
			return PushDevice{}, err
		}
		return *device, nil
	})
	if err != nil {
		return false, nil, fmt.Errorf("list profile set wake devices: %w", err)
	}
	if len(devices) == 0 {
		return false, nil, nil
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
	rows, err = tx.Query(ctx, `SELECT `+pushAttemptColumns+` FROM push_delivery_attempts WHERE id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return false, nil, fmt.Errorf("read profile set wake attempts: %w", err)
	}
	if sendNow, err = scanPushDeliveryAttempts(rows); err != nil {
		return false, nil, fmt.Errorf("read profile set wake attempts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, nil, fmt.Errorf("commit profile set wake: %w", err)
	}
	return len(sendNow) > 0, sendNow, nil
}

// RetireProfileSetWakes marks wakes whose send never finished as failed and
// deletes wakes past their retention. Wakes name no notification delivery,
// so delivery retention never reaches them.
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
		s.sendProfileSetWakeDetached(ctx, sendNow)
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
// deadline, and a send that did not record an outcome is recorded failed on
// a fresh context its deadline cannot have canceled, so no wake row outlives
// its send in flight. Only a process that stops mid-send leaves one, which no
// claim selects, the queue gauge leaves out, and retention marks failed.
func (s *System) sendProfileSetWakeDetached(ctx context.Context, attempts []PushDeliveryAttempt) {
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				s.logger.ErrorContext(ctx, "profile set wake send panicked", "panic", fmt.Sprint(p))
			}
		}()
		for _, attempt := range attempts {
			s.sendProfileSetWakeTo(ctx, attempt)
		}
	}()
}

// sendProfileSetWakeTo sends one phone's wake and records its outcome.
func (s *System) sendProfileSetWakeTo(ctx context.Context, attempt PushDeliveryAttempt) {
	phoneCtx, cancel := context.WithTimeout(ctx, profileSetPhoneTimeout)
	finished := s.pushSender.processAttempt(phoneCtx, attempt)
	timedOut := phoneCtx.Err() != nil
	cancel()
	if finished != nil && finished.Outcome != PushOutcomeRetrying {
		return
	}
	reason := "wake_send_unfinished"
	if timedOut {
		reason = "wake_send_timed_out"
	}
	finalizeCtx, cancelFinalize := context.WithTimeout(ctx, profileSetFinalizeTimeout)
	defer cancelFinalize()
	if err := s.pushDeviceRepo.failUnfinishedProfileSetWake(finalizeCtx, attempt.ID, reason); err != nil {
		s.logger.WarnContext(ctx, "recording an unfinished profile set wake failed", "attempt_id", attempt.ID, "error", err)
	}
}
