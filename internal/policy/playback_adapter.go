package policy

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// ActionChecker is the subset of PDP used by action adapters.
type ActionChecker interface {
	CheckAction(context.Context, ActionInput) (ActionDecision, Meta, error)
}

// NewPlaybackAdmissionDecider adapts CheckAction to playback.SessionManager's
// admission hook. Session counts and limit loading remain owned by playback.
func NewPlaybackAdmissionDecider(checker ActionChecker) playback.AdmissionDecider {
	return func(ctx context.Context, req playback.AdmissionRequest) (playback.AdmissionDecision, error) {
		requestedAction := RequestedActionDirectPlay
		if req.RequestedMethod == playback.PlayTranscode {
			requestedAction = RequestedActionTranscode
		}
		decision, _, err := checker.CheckAction(ctx, ActionInput{
			SchemaVersion:           1,
			Action:                  ActionPlaybackAdmission,
			UserID:                  req.UserID,
			MaxStreams:              req.Limits.MaxStreams,
			MaxTranscodes:           req.Limits.MaxTranscodes,
			CurrentActiveStreams:    req.CurrentActiveStreams,
			CurrentActiveTranscodes: req.CurrentActiveTranscodes,
			RequestedAction:         requestedAction,
			RequestTime:             time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			return playback.AdmissionDecision{}, err
		}
		return playback.AdmissionDecision{
			Allowed:    decision.Allowed,
			Reason:     decision.Reason,
			ReasonCode: decision.ReasonCode,
		}, nil
	}
}
