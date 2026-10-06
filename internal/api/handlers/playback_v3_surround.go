package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	capacityUnavailableReasonV3      = "capacity_unavailable"
	audioTranscodingDisabledReasonV3 = "audio_transcoding_disabled"
	transcodingDisabledReasonV3      = "transcoding_disabled"
	routePreparationFailedReasonV3   = "route_preparation_failed"
)

// surroundFallbackReasonsV3 are session admission refusals that leave the
// original track playable on the client. Every route-preparation failure
// qualifies as well (transportErrorV3.routePreparation).
var surroundFallbackReasonsV3 = map[string]bool{
	capacityUnavailableReasonV3:      true,
	audioTranscodingDisabledReasonV3: true,
	transcodingDisabledReasonV3:      true,
}

// replanWithoutSurroundConversionV3 re-plans once without a refused surround
// conversion. The conversion only improves on a route the client can already
// play, so a refusal falls back to that route instead of failing playback. A
// refusal raised after the attempt was persisted is left alone: the attempt ID
// is spent. The fallback passes through the same progressive-remux escalation
// as a first plan.
func (h *PlaybackHandler) replanWithoutSurroundConversionV3(ctx context.Context, mode mediaAuthModeV3, plannerInput func() playback.PlannerInputV3, result playback.PlannerResultV3, refusal *transportErrorV3) (playback.PlannerResultV3, bool) {
	if refusal == nil || refusal.attemptPersisted || !refusal.routePreparation && !surroundFallbackReasonsV3[refusal.reason] || !playback.IsSurroundBitstreamConversionV3(result.Plan) {
		return result, false
	}
	next := plannerInput()
	outputContextID := next.Request.ClientPlaybackContext.Output.OutputContextID
	next.AttemptedKeys = append(append([]string(nil), next.AttemptedKeys...),
		playback.PlanAttemptKeyV3(*result.Plan, outputContextID, nil))
	next.Now = time.Now()
	fallback, capabilityErr := h.planPlaybackWithCapabilitiesV3(ctx, next)
	if capabilityErr != nil || fallback.Terminal != nil || fallback.Plan == nil || playback.IsSurroundBitstreamConversionV3(fallback.Plan) {
		return result, false
	}
	fallback, escalateErr := h.escalateRefusedProgressiveRemuxV3(ctx, mode, func() playback.PlannerInputV3 { return next }, fallback)
	if escalateErr != nil {
		return result, false
	}
	slog.InfoContext(ctx, "protocol v3 surround conversion refused; using the next route",
		logComponentKey, playbackLogValueV3,
		"refusal", refusal.reason,
		"delivery", fallback.Plan.Delivery,
		"decision_reason", fallback.Plan.DecisionReason,
	)
	return fallback, true
}

// discardRefusedStartSessionV3 tears down a session whose surround conversion
// failed to start. It releases the same resources as a stop but writes no
// watch history and sends no scrobble: nothing played.
func (h *PlaybackHandler) discardRefusedStartSessionV3(ctx context.Context, sessionID string) {
	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil {
		return
	}
	unlock := h.tm.LockSessionLifecycle(session.ID)
	defer unlock()
	if err := h.sessionMgr.StopSession(session.ID); err != nil {
		return
	}
	h.cancelPlaybackStartSideEffectsV3(ctx, session.ID)
	if h.AdminStore != nil {
		if err := h.AdminStore.DeleteSession(ctx, session.ID); err != nil {
			slog.ErrorContext(ctx, "failed to delete synced session", "component", "api", "session", session.ID, "error", err)
		}
	}
	h.closeTranscodeForSession(session)
	h.deleteProxyGrantV3(ctx, session.ID)
	h.deleteNodeRecipeV3(ctx, session.TranscodeTransportID)
	h.syncSessionsNow(ctx, "stop")
}
