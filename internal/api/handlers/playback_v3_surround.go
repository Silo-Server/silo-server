package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	transcodeLimitReachedReasonV3    = "transcode_limit_reached"
	audioTranscodingDisabledReasonV3 = "audio_transcoding_disabled"
	transcodingDisabledReasonV3      = "transcoding_disabled"
	routePreparationFailedReasonV3   = "route_preparation_failed"
)

// surroundFallbackReasonsV3 are session admission refusals that leave the
// original track playable on the client. Every route-preparation failure
// qualifies as well (transportErrorV3.routePreparation). The account stream
// limit is not one: the original route needs a stream too.
var surroundFallbackReasonsV3 = map[string]bool{
	transcodeLimitReachedReasonV3:    true,
	audioTranscodingDisabledReasonV3: true,
	transcodingDisabledReasonV3:      true,
}

// replanWithoutSurroundConversionV3 re-plans once without a refused surround
// conversion. The conversion only improves on a route the client can already
// play, so a refusal falls back to that route instead of failing playback. A
// refusal raised after the attempt was persisted is left alone: the attempt ID
// is spent. So is a session that ended while its route was prepared: a stop
// that lands then is final. plannerInput reports false when the fallback
// cannot be planned faithfully, and the refusal then stands. The fallback
// passes through the same progressive-remux escalation as a first plan.
func (h *PlaybackHandler) replanWithoutSurroundConversionV3(ctx context.Context, mode mediaAuthModeV3, plannerInput func() (playback.PlannerInputV3, bool), result playback.PlannerResultV3, refusal *transportErrorV3) (playback.PlannerResultV3, bool) {
	if refusal == nil || refusal.attemptPersisted || refusal.reason == terminalSessionExpiredV3 ||
		!refusal.routePreparation && !surroundFallbackReasonsV3[refusal.reason] || !playback.IsSurroundBitstreamConversionV3(result.Plan) {
		return result, false
	}
	next, ok := plannerInput()
	if !ok {
		return result, false
	}
	// Each codec is its own attempt key, and the fallback plans from a fresh
	// capability snapshot, which can offer the other codec. Exclude every
	// conversion it proposes; there are only two.
	outputContextID := next.Request.ClientPlaybackContext.Output.OutputContextID
	next.AttemptedKeys = append([]string(nil), next.AttemptedKeys...)
	refused := result
	var fallback playback.PlannerResultV3
	for range 3 {
		next.AttemptedKeys = append(next.AttemptedKeys, playback.PlanAttemptKeyV3(*refused.Plan, outputContextID, nil))
		next.Now = time.Now()
		var capabilityErr error
		fallback, capabilityErr = h.planPlaybackWithCapabilitiesV3(ctx, next)
		if capabilityErr != nil || fallback.Terminal != nil || fallback.Plan == nil {
			return result, false
		}
		if !playback.IsSurroundBitstreamConversionV3(fallback.Plan) {
			break
		}
		refused = fallback
	}
	if playback.IsSurroundBitstreamConversionV3(fallback.Plan) {
		return result, false
	}
	fallback, escalateErr := h.escalateRefusedProgressiveRemuxV3(ctx, mode, func() playback.PlannerInputV3 { return next }, fallback)
	if escalateErr != nil {
		return result, false
	}
	// The fallback delivers the same file at the source bitrate, so the
	// replaced plan's settled bandwidth-limit label applies unchanged.
	fallback.Plan.BandwidthLimit = result.Plan.BandwidthLimit
	slog.InfoContext(ctx, "protocol v3 surround conversion refused; using the next route",
		logComponentKey, playbackLogValueV3,
		"refusal", refusal.reason,
		"delivery", fallback.Plan.Delivery,
		"decision_reason", fallback.Plan.DecisionReason,
	)
	return fallback, true
}

// routePreparationErrorV3 marks err, when set, as a failure to prepare the
// selected route's transport.
func routePreparationErrorV3(err *transportErrorV3) *transportErrorV3 {
	if err == nil {
		return nil
	}
	marked := *err
	marked.routePreparation = true
	return &marked
}

// discardRefusedStartSessionV3 tears down a session whose surround conversion
// failed to start. It ends the session's authority as the start's ordinary
// abort does, without the user-stop steps that can fail before the session
// stops, but writes no watch history and sends no scrobble: nothing played.
func (h *PlaybackHandler) discardRefusedStartSessionV3(ctx context.Context, sessionID string) {
	if err := h.dropLiveSession(ctx, sessionID, false); err != nil {
		slog.ErrorContext(ctx, "failed to discard a refused surround conversion session", logComponentKey, playbackLogValueV3, "session", sessionID, "error", err)
		return
	}
	if h.AdminStore != nil {
		if err := h.AdminStore.DeleteSession(ctx, sessionID); err != nil {
			slog.ErrorContext(ctx, "failed to delete synced session", "component", "api", "session", sessionID, "error", err)
		}
	}
}
