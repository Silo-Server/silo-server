package handlers

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// nativeEndClaim is one caller's hold on ending a session: the attempt row
// stopped under the end's stop id, and the claim to run its writers.
//
// Every way a native session ends (a stop, an expiry, an abort, an
// administrator) can race another on the same or another replica. The first
// stop wins the row, and only the caller holding the finalization lease runs
// the writers that are not idempotent: watch history, scrobbles, the admin
// log. A caller that loses tears down only what it holds locally.
type nativeEndClaim struct {
	store   playback.ProgressStoreV3
	record  *playback.AttemptRecordV3
	receipt playback.StopReceiptV3
	// owned: this caller runs the end's writers. A session without an
	// attempt row (an unsequenced bridge play, or a store that keeps none)
	// cannot be arbitrated, so its caller owns its end as before.
	owned bool
}

// claimNativeEnd denies the session's tokens everywhere, stops its attempt
// row under stopID, and claims the finalization of whichever stop won the
// row, so a winner that died before finishing is taken over here.
func claimNativeEnd(ctx context.Context, planStore playback.PlanStoreV3, deny *playback.StreamDeny, sessionID, stopID string) nativeEndClaim {
	claim := nativeEndClaim{owned: true}
	if sessionID == "" {
		return claim
	}
	deny.Deny(ctx, sessionID)
	store, ok := planStore.(playback.ProgressStoreV3)
	if !ok {
		return claim
	}
	record, err := planStore.GetAttempt(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) {
			slog.WarnContext(ctx, "failed to read playback attempt before ending it", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
		return claim
	}
	receipt, _, err := store.StopAttempt(ctx, sessionID, stopID, nil)
	if err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) {
			slog.WarnContext(ctx, "failed to stop playback attempt server-side", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
		return claim
	}
	if receipt.Finalized {
		return nativeEndClaim{}
	}
	if !claimStopFinalization(ctx, store, sessionID, receipt.StopID) {
		return nativeEndClaim{}
	}
	return nativeEndClaim{store: store, record: record, receipt: receipt, owned: true}
}

// providerNotice marks a finalization whose caller sends the stop's
// watch-provider event itself, so the finalizer does not send it too. left
// records that the finalizer recorded the end and left the event to the
// caller; one that only replayed another caller's finished receipt did not.
type providerNotice struct{ left bool }

type providerNoticeKey struct{}

func withProviderNoticeDeferred(ctx context.Context) (context.Context, *providerNotice) {
	notice := &providerNotice{}
	return context.WithValue(ctx, providerNoticeKey{}, notice), notice
}

// leaveProviderNotice reports whether ctx defers the provider event to the
// finalizer's caller, and records that this finalizer left it.
func leaveProviderNotice(ctx context.Context) bool {
	notice, _ := ctx.Value(providerNoticeKey{}).(*providerNotice)
	if notice == nil {
		return false
	}
	notice.left = true
	return true
}

// claimStopFinalization claims a stopped attempt's finalization lease. A
// claim whose reply was lost may have committed, so an error is resolved by
// reading the receipt back: the lease this call asked for is unique to it.
// A claim it cannot confirm is not taken; its lease lapses and a later end
// or the replayed stop finalizes the attempt.
func claimStopFinalization(ctx context.Context, store playback.ProgressStoreV3, sessionID, stopID string) bool {
	// The store keeps microseconds, so compare the lease at that precision.
	lease := time.Now().Add(stopFinalizationLease).Truncate(time.Microsecond)
	var err error
	for range 3 {
		var claimed bool
		if claimed, err = store.ClaimStopFinalization(ctx, sessionID, lease); err == nil {
			return claimed
		}
		if receipt, _, readErr := store.StopAttempt(ctx, sessionID, stopID, nil); readErr == nil {
			return !receipt.Finalized && receipt.FinalizingUntil.Equal(lease)
		}
	}
	slog.WarnContext(ctx, "failed to claim playback stop finalization", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
	return false
}

// leased reports whether the claim holds the finalization lease, as opposed
// to owning the end by default because the store could not arbitrate it.
func (c nativeEndClaim) leased() bool {
	return c.store != nil
}

// ownEnd reports whether the claim finalizes the end its caller made, as
// opposed to an earlier stop that won the row and never finished, which the
// caller must finish with that stop's own writers (FinishOtherEnd).
func (c nativeEndClaim) ownEnd(stopID string) bool {
	return c.store == nil || c.receipt.StopID == stopID
}

// FinishOtherEnd finalizes the earlier stop a claim took over, from the
// attempt row with that stop's reason and accepted sample: its watch and
// admin history and its provider event, as the stop's own finalizer would.
func (h *PlaybackHandler) FinishOtherEnd(ctx context.Context, claim nativeEndClaim, sessionID string) {
	historyID, err := h.finishStop(ctx, claim.record, sessionID, claim.receipt.Accepted, claim.reason(sessionID, PlaybackEndStopped))
	if err != nil {
		slog.WarnContext(ctx, "failed to finish an earlier playback stop", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		return
	}
	claim.finish(ctx, sessionID, historyID)
}

// applyAccepted moves a session snapshot to the final sample its attempt
// accepted, which can be newer than the snapshot.
func (c nativeEndClaim) applyAccepted(session *playback.Session) {
	if c.store != nil && c.receipt.Accepted != nil {
		session.Position, session.IsPaused = c.receipt.Accepted.Position, c.receipt.Accepted.IsPaused
	}
}

// reason is the end reason of the stop that won the row, which can be an
// earlier end than the one this caller was making.
func (c nativeEndClaim) reason(sessionID string, fallback PlaybackEndReason) PlaybackEndReason {
	if c.store == nil {
		return fallback
	}
	return endReasonForStop(sessionID, c.receipt.StopID)
}

// finish records the end's receipt as finalized, with the watch-history row
// the writers made, so later stops replay it.
func (c nativeEndClaim) finish(ctx context.Context, sessionID, historyID string) {
	if c.store == nil {
		return
	}
	receipt := c.receipt
	receipt.Finalized = true
	receipt.FinalizingUntil = time.Time{}
	if historyID != "" {
		receipt.HistoryID = historyID
	}
	if err := c.store.RecordStopReceipt(ctx, sessionID, receipt); err != nil {
		slog.WarnContext(ctx, "failed to record server-side playback stop receipt", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
	}
}

// endSessionAsAdmin ends a session an administrator stopped or terminated,
// or whose command the player never confirmed. The attempt row is stopped
// under the administrator's stop id first, so progress and replan answer 410
// on every replica, and only the caller that owns the end writes its
// history: from this replica's live copy, or from the attempt row when
// another replica holds the copy. A session another stop already ended only
// loses this replica's copy. ErrSessionNotFound means there was nothing here
// to end.
func (h *PlaybackHandler) endSessionAsAdmin(ctx context.Context, sessionID string) error {
	claim := claimNativeEnd(ctx, h.PlanStoreV3, h.StreamDeny, sessionID, adminStopID(sessionID))
	defer h.forgetProgressSideEffectLock(sessionID)
	switch {
	case claim.store != nil:
		historyID, err := h.finishStop(ctx, claim.record, sessionID, claim.receipt.Accepted, claim.reason(sessionID, PlaybackEndTerminated))
		if err != nil {
			// The teardown failed before the writers ran: leave the end
			// unfinalized, so its lease lapses and a retry can finish it.
			return err
		}
		claim.finish(ctx, sessionID, historyID)
		return nil
	case claim.owned:
		return h.stopPlaybackSessionByID(ctx, sessionID, PlaybackEndTerminated)
	default:
		return h.dropLiveSessionByID(ctx, sessionID)
	}
}
