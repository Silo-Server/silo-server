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
	// profileSetSendTimeout bounds the background send. An attempt it
	// leaves pending is sent by the retry sweep.
	profileSetSendTimeout = 30 * time.Second
	// profileSetWakeRetention is how long a finished wake attempt is kept.
	profileSetWakeRetention = 7 * 24 * time.Hour
)

// profileSetWakeLock names the advisory lock that serializes one account's
// wakes across API servers; the account ID is its second half.
const profileSetWakeLock = "push_profile_set_wake"

// EnqueueProfileSetWake queues a wake for every registered, enabled phone
// of account userID on platforms whose login is live, unless the account had
// one queued in the last profileSetWakeInterval. queued reports whether a
// wake is queued now or was inside that interval; sendNow lists the
// attempts it created, pending, for the caller to send.
//
// minRevision, when not nil, is the first profile_set_edits revision the
// calling request recorded: the wake goes out only when an edit row at or
// above it exists, that is, when one of the request's edits committed.
//
// The account's advisory lock makes the check and the insert one step on
// every API server, so edits recorded at the same moment on two servers
// queue one wake.
func (r *PushDeviceRepository) EnqueueProfileSetWake(ctx context.Context, userID int, platforms []string, minRevision *int64) (queued bool, sendNow []string, err error) {
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
	var recent bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM push_delivery_attempts a
		JOIN push_devices d ON d.id = a.push_device_id
		WHERE d.user_id = $1 AND a.trigger_type = $2 AND a.created_at > now() - $3::interval)`,
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
	if err := r.EnqueuePushAttempts(ctx, tx, attempts); err != nil {
		return false, nil, err
	}
	// A device removed since the listing got no attempt.
	rows, err = tx.Query(ctx, `SELECT id FROM push_delivery_attempts WHERE id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return false, nil, fmt.Errorf("read profile set wake attempts: %w", err)
	}
	sendNow, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, nil, fmt.Errorf("read profile set wake attempts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, nil, fmt.Errorf("commit profile set wake: %w", err)
	}
	return len(sendNow) > 0, sendNow, nil
}

// DeleteOldProfileSetWakes removes finished wake attempts past their
// retention. Wakes name no notification delivery, so delivery retention
// never reaches them.
func (r *PushDeviceRepository) DeleteOldProfileSetWakes(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM push_delivery_attempts
		WHERE trigger_type = $1 AND outcome IN ($2, $3) AND created_at < now() - $4::interval`,
		PushTriggerProfileSet, PushOutcomeDelivered, PushOutcomeFailed, profileSetWakeRetention)
	if err != nil {
		return 0, fmt.Errorf("delete old profile set wakes: %w", err)
	}
	return tag.RowsAffected(), nil
}

// WakeProfileSetDevices is the profileedits.Waker: it wakes account
// userID's phones after a profile from its profile set changed on this
// server. It reports whether a wake was queued for at least one device or
// already was inside the debounce interval; false when push delivery is off
// for every platform, the account has no registered phone, or the request
// that calls it recorded edits none of which committed.
//
// It never fails the caller: errors are logged. It queues the wake before it
// returns and sends it in the background; an attempt the send does not
// finish is sent by the retry sweep.
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
		go s.sendProfileSetWake(ctx, sendNow)
	}
	return queued
}

// sendProfileSetWake sends the wake attempts it can claim.
func (s *System) sendProfileSetWake(ctx context.Context, attemptIDs []string) {
	ctx, cancel := context.WithTimeout(ctx, profileSetSendTimeout)
	defer cancel()
	for _, id := range attemptIDs {
		claimed, err := s.pushDeviceRepo.ClaimPushAttemptByID(ctx, id)
		if err != nil {
			s.logger.WarnContext(ctx, "claiming profile set wake failed", "attempt_id", id, "error", err)
			continue
		}
		for _, attempt := range claimed {
			s.pushSender.processAttempt(ctx, attempt)
		}
	}
}
