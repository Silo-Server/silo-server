package notifications

import (
	"context"
	"fmt"
	"sync"
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
	// profileSetFinalizeTimeout bounds a wake's tail: the one write, after
	// every send has returned, that records the phones still in flight as
	// failed.
	profileSetFinalizeTimeout = 5 * time.Second
	// profileSetWakeStale is when retention marks a wake whose send never
	// finished (the process stopped mid-send) as failed.
	profileSetWakeStale = 10 * time.Minute
	// profileSetWakeRetention is how long a wake attempt is kept.
	profileSetWakeRetention = 7 * 24 * time.Hour
)

// profileSetWakeBudget bounds every phone's send of one wake, from the
// moment the wake is queued. It is half of profileSetWakeInterval. After it,
// the wake's tail takes at most profileSetFinalizeTimeout, so a wake ends,
// and releases the account's in-flight mark, within the budget plus that:
// 35 seconds. The limit measures the interval from when a wake is recorded,
// and the wake is queued up to profileSetEnqueueTimeout later, so the budget
// plus the tail must stay under the interval less that: then a wake has
// ended before the account's limit lets the next one start, and two of an
// account's wakes never overlap (the in-process guard, claimProfileSetWake,
// holds that on one server even if the clocks disagree).
// TestProfileSetWakeBudgetIsShorterThanTheInterval holds the constants to
// that. Each phone's send gets what is left of the budget. One send can make up to seven relay requests
// (renewing and replacing the relay credential, a second post without the
// push target or after idempotency_key_reuse, recovering a rejected
// credential, and the send again), each under pushRelayRequestTimeout, which
// is more than the budget, so no per-phone deadline is set: the phones are
// started in listProfileSetWakeDevices's order, phones that receive pushes
// first, profileSetWakeConcurrency at a time, and one the budget does not
// reach is recorded failed and gets the next wake. A var so tests can
// shorten it.
var profileSetWakeBudget = profileSetWakeInterval / 2

// profileSetWakeConcurrency is how many of one wake's phones are sent at
// once. Sent one after another, a single slow or dead registration at the
// front could use up the budget, and against a relay that times out every
// request only two phones would be reached. Four lets a slow phone hold one
// slot while the person's other phones go on, and keeps one wake to a
// quarter of the 16 workers the push dispatcher already sends through the
// same relay with. One wake makes at most about 83 relay requests (20 phones,
// up to four posts each, and up to three credential requests), never more
// than four at once, once a minute per account.
const profileSetWakeConcurrency = 4

// A wake is sent once and never retried: the next edit or the app opening
// repairs a missed one. Its row is never in a state any server's claim
// queries select (ClaimDuePushAttempts takes pending rows a minute old and
// retrying rows whose next_retry_at has passed, ClaimPushAttemptByID and
// ClaimPendingPushForDelivery take pending rows). An older server's sweep
// would send a claimed wake as a visible alert. So the row is inserted in
// flight, as retrying with no next_retry_at, which no claim selects since
// NULL <= now() is never true, and the send records delivered or failed.

// profileSetWakeMaxPhones caps the phones one wake reaches. A wake writes
// one attempt and makes one relay call per phone, and a person can set one
// off from their own session, so its fan-out is bounded. A household's
// phones, tablets and computers stay well under it. Which phones it reaches
// is listProfileSetWakeDevices's order.
const profileSetWakeMaxPhones = 20

// EnqueueProfileSetWake queues a wake for the registered, enabled phones of
// account userID on platforms whose login is live, at most
// profileSetWakeMaxPhones of them in listProfileSetWakeDevices's order, unless
// the account had one queued in the last profileSetWakeInterval. sendNow
// lists the attempts it created and committed, in flight, for the caller to
// send; it is empty when no wake was queued.
//
// minRevision, when not nil, is the first profile_set_edits revision the
// calling request recorded: the wake goes out only when an edit row at or
// above it exists, that is, when one of the request's edits committed. claim
// is called once that is known, before anything is recorded, and a false
// answer queues nothing: it is the caller's in-flight guard, so an edit that
// rolled back never takes it.
//
// One statement decides and records the wake in push_profile_set_wakes, so
// edits recorded at the same moment on two API servers queue one wake, and
// removing and registering a device again does not reset the limit. The
// time is recorded in the transaction that queues the attempts: a failure
// to queue rolls it back, and an account whose phones all went away in the
// meantime records nothing.
func (r *PushDeviceRepository) EnqueueProfileSetWake(ctx context.Context, userID int, platforms []string, minRevision *int64, claim func() bool) (sendNow []PushDeliveryAttempt, err error) {
	if r == nil || r.pool == nil || len(platforms) == 0 {
		return nil, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin profile set wake: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if minRevision != nil {
		var committed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM profile_set_edits WHERE user_id = $1 AND revision >= $2)`,
			userID, *minRevision).Scan(&committed); err != nil {
			return nil, fmt.Errorf("check profile set edit: %w", err)
		}
		if !committed {
			return nil, nil
		}
	}
	if claim != nil && !claim() {
		return nil, nil
	}
	devices, err := listProfileSetWakeDevices(ctx, tx, userID, platforms)
	if err != nil || len(devices) == 0 {
		return nil, err
	}
	due, err := recordProfileSetWake(ctx, tx, userID)
	if err != nil || !due {
		return nil, err
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
		return nil, err
	}
	// A device removed since the listing got no attempt. The rest keep the
	// listing's order, which is the order a wake sends them in.
	rows, err := tx.Query(ctx, `SELECT `+pushAttemptColumns+` FROM push_delivery_attempts
		WHERE id = ANY($1) ORDER BY array_position($1::text[], id)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read profile set wake attempts: %w", err)
	}
	if sendNow, err = scanPushDeliveryAttempts(rows); err != nil {
		return nil, fmt.Errorf("read profile set wake attempts: %w", err)
	}
	if len(sendNow) == 0 {
		return nil, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit profile set wake: %w", err)
	}
	return sendNow, nil
}

// listProfileSetWakeDevices lists the phones a wake of userID reaches: its
// enabled registrations on platforms whose login is live, at most
// profileSetWakeMaxPhones. They are ordered by when a push to them was last
// accepted, newest first and never last, then by when they last
// registered. APNs and FCM accept a push only for a real token of the app,
// so a registration whose push was never accepted, such as one with a
// made-up token, cannot take the place of a phone that receives pushes.
// last_seen_at is written when a phone registers, not when it is used.
func listProfileSetWakeDevices(ctx context.Context, tx pgx.Tx, userID int, platforms []string) ([]PushDevice, error) {
	rows, err := tx.Query(ctx, `SELECT `+pushDeviceColumns+`
		FROM push_devices
		WHERE user_id = $1
		  AND platform = ANY($2)
		  AND provider = $3
		  AND push_mode = $4
		  AND enabled
		  AND `+pushDeviceLoginLive+`
		ORDER BY last_success_at DESC NULLS LAST, last_seen_at DESC NULLS LAST, id
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
// waits on the account's row and then finds the new time. The time is the
// statement's own (clock_timestamp), not its transaction's start, so the
// interval is measured from when each wake is recorded.
func recordProfileSetWake(ctx context.Context, tx pgx.Tx, userID int) (bool, error) {
	rows, err := tx.Query(ctx, `INSERT INTO push_profile_set_wakes AS w (user_id, last_wake_at)
		VALUES ($1, clock_timestamp())
		ON CONFLICT (user_id) DO UPDATE SET last_wake_at = excluded.last_wake_at
		WHERE w.last_wake_at <= clock_timestamp() - $2::interval
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
// server. It answers true when, and only when, this call queued a wake after
// the edit committed: the wake's attempts were written and committed for at
// least one phone. The answer is about the wake being queued, not delivered:
// it stays true if the send then fails or runs out of budget. It answers
// false when the account had a wake inside the last profileSetWakeInterval,
// when a wake for the account is still in flight on this server, when the
// request's edits all rolled back, when the account has no phone to wake,
// and when push delivery is off for every platform. A wake that left before
// this edit existed says nothing about it.
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
	claimed, handedOff := false, false
	defer func() {
		if claimed && !handedOff {
			s.releaseProfileSetWake(userID)
		}
	}()
	claim := func() bool {
		claimed = s.claimProfileSetWake(userID)
		return claimed
	}
	sendNow, err := s.pushDeviceRepo.EnqueueProfileSetWake(enqueueCtx, userID, platforms, minRevision, claim)
	if err != nil {
		s.logger.WarnContext(ctx, "queueing profile set wake failed", "user_id", userID, "error", err)
		return false
	}
	if len(sendNow) == 0 {
		return false
	}
	s.sendProfileSetWakeDetached(ctx, userID, sendNow)
	handedOff = true
	return true
}

// claimProfileSetWake marks a wake of userID as in flight on this server; it
// reports false when one already is. The mark lives in this process only: a
// row in the database would outlive a process that stopped mid-wake and
// hold the account's wakes until retention. Across servers, the budget and
// the limit row keep an account's wakes apart.
func (s *System) claimProfileSetWake(userID int) bool {
	s.wakesMu.Lock()
	defer s.wakesMu.Unlock()
	if _, busy := s.wakesInFlight[userID]; busy {
		return false
	}
	if s.wakesInFlight == nil {
		s.wakesInFlight = map[int]struct{}{}
	}
	s.wakesInFlight[userID] = struct{}{}
	return true
}

// releaseProfileSetWake ends the in-flight mark of a wake of userID.
func (s *System) releaseProfileSetWake(userID int) {
	s.wakesMu.Lock()
	defer s.wakesMu.Unlock()
	delete(s.wakesInFlight, userID)
}

// profileSetWakeInFlight reports whether a wake of userID is in flight on
// this server.
func (s *System) profileSetWakeInFlight(userID int) bool {
	s.wakesMu.Lock()
	defer s.wakesMu.Unlock()
	_, busy := s.wakesInFlight[userID]
	return busy
}

// failUnfinishedProfileSetWakes records as failed, in one statement, every
// attempt of a wake its sends left in flight: wake_send_unfinished for those
// in unfinished (a send that panicked or stopped before writing an outcome),
// wake_out_of_time for the rest (the budget ended before they finished or
// started). It runs once every send of the wake has returned, so no send can
// write after it, and a row a send did finish is left as it is.
func (r *PushDeviceRepository) failUnfinishedProfileSetWakes(ctx context.Context, attemptIDs, unfinished []string) error {
	if _, err := r.pool.Exec(ctx, `UPDATE push_delivery_attempts a
		SET outcome = $3, attempt_number = a.attempt_number + 1, attempted_at = now(),
		    upstream_reason = CASE WHEN a.id = ANY($2) THEN 'wake_send_unfinished' ELSE 'wake_out_of_time' END,
		    failure_message = CASE WHEN a.id = ANY($2) THEN 'wake_send_unfinished' ELSE 'wake_out_of_time' END,
		    updated_at = now()
		WHERE a.id = ANY($1) AND a.trigger_type = $4 AND a.outcome = $5 AND a.next_retry_at IS NULL`,
		attemptIDs, unfinished, PushOutcomeFailed, PushTriggerProfileSet, PushOutcomeRetrying); err != nil {
		return fmt.Errorf("fail unfinished profile set wakes: %w", err)
	}
	return nil
}

// sendProfileSetWakeDetached sends the wake on its own goroutine, as the
// service's other detached sends do (PostServerChannelRequestEvent): the
// caller never waits for the relay. The phones are started in order,
// profileSetWakeConcurrency at a time, each under what is left of
// profileSetWakeBudget, which starts now; the relay requests, the database
// writes and the wait for the relay credential's lock all end with it. Once
// every send has returned, one statement records every phone still in flight
// as failed, on a fresh context limited to profileSetFinalizeTimeout. The
// account's in-flight mark is released after that write, a panic included,
// so a wake holds it for at most the budget plus profileSetFinalizeTimeout
// (35 seconds), and no wake row or goroutine outlives the wake. Only a
// process that stops mid-wake leaves a row in flight, which no claim
// selects, the queue gauge leaves out, and retention marks failed.
func (s *System) sendProfileSetWakeDetached(ctx context.Context, userID int, attempts []PushDeliveryAttempt) {
	ctx = context.WithoutCancel(ctx)
	budget, cancel := context.WithTimeout(ctx, profileSetWakeBudget)
	go func() {
		defer s.releaseProfileSetWake(userID)
		defer func() {
			if p := recover(); p != nil {
				s.logProfileSetWakePanic(ctx, userID, "", p)
			}
		}()
		defer cancel()
		var (
			mu         sync.Mutex
			unfinished []string
		)
		next := make(chan PushDeliveryAttempt)
		var senders sync.WaitGroup
		for range min(profileSetWakeConcurrency, len(attempts)) {
			senders.Go(func() {
				for attempt := range next {
					if !s.sendProfileSetWakeTo(budget, userID, attempt) {
						mu.Lock()
						unfinished = append(unfinished, attempt.ID)
						mu.Unlock()
					}
				}
			})
		}
		for _, attempt := range attempts {
			next <- attempt
		}
		close(next)
		senders.Wait()

		ids := make([]string, len(attempts))
		for i, attempt := range attempts {
			ids[i] = attempt.ID
		}
		finalizeCtx, cancelFinalize := context.WithTimeout(ctx, profileSetFinalizeTimeout)
		defer cancelFinalize()
		if err := s.pushDeviceRepo.failUnfinishedProfileSetWakes(finalizeCtx, ids, unfinished); err != nil {
			s.logger.WarnContext(ctx, "recording an unfinished profile set wake failed", "user_id", userID, "error", err)
		}
	}()
}

// sendProfileSetWakeTo sends one phone's wake under what is left of budget.
// It reports false for a send that was started and neither recorded an
// outcome nor ran out of budget: one that panicked or stopped early. A phone
// the budget did not reach, or did not let finish, is left for the tail's
// out-of-time write.
func (s *System) sendProfileSetWakeTo(budget context.Context, userID int, attempt PushDeliveryAttempt) bool {
	if budget.Err() != nil {
		return true
	}
	finished, panicked := s.attemptProfileSetWake(budget, userID, attempt)
	if panicked {
		return false
	}
	return (finished != nil && finished.Outcome != PushOutcomeRetrying) || budget.Err() != nil
}

// attemptProfileSetWake runs one phone's send under budget, turning a panic
// in it into an unfinished send.
func (s *System) attemptProfileSetWake(budget context.Context, userID int, attempt PushDeliveryAttempt) (finished *PushDeliveryAttempt, panicked bool) {
	defer func() {
		if p := recover(); p != nil {
			s.logProfileSetWakePanic(budget, userID, attempt.ID, p)
			finished, panicked = nil, true
		}
	}()
	return s.pushSender.processAttempt(budget, attempt), false
}

// logProfileSetWakePanic logs a panic in a wake send by its type only: the
// send handles the decrypted push token and the relay credential, so the
// panic's text could carry either.
func (s *System) logProfileSetWakePanic(ctx context.Context, userID int, attemptID string, p any) {
	s.logger.ErrorContext(ctx, "profile set wake send panicked",
		"user_id", userID, "attempt_id", attemptID, "panic_type", fmt.Sprintf("%T", p))
}
