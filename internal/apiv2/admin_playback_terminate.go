package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// AdminPlaybackTerminateService revokes a playback session's authority
// durably and then notifies the client as best effort.
type AdminPlaybackTerminateService interface {
	AdminTerminateAvailable() bool
	Terminate(context.Context, handlers.AdminTerminateInput) (handlers.AdminTerminateView, error)
}

// AdminPlaybackTerminateInput addresses one session; the body is optional.
type AdminPlaybackTerminateInput struct {
	SessionID string `path:"session_id" minLength:"1" maxLength:"128"`
	Body      struct {
		Reason string `json:"reason,omitempty" maxLength:"1024" doc:"Free-form administrator reason shown to the player when supported"`
	}
}

// AdminPlaybackTerminateReceipt reports the two facts separately: the
// durable revocation the server performed, and whether the client was told.
type AdminPlaybackTerminateReceipt struct {
	SessionID        string `json:"session_id" doc:"The terminated playback session"`
	AuthorityRevoked bool   `json:"authority_revoked" doc:"The session's playback authority is durably revoked: media tokens and progress writes for it are refused from now on. Buffered media already delivered may still play out"`
	AlreadyRevoked   bool   `json:"already_revoked" doc:"The session was already terminated before this request; the call converged without dispatching another command"`
	DurableState     string `json:"durable_state" enum:"stopped" doc:"stopped: the session is stopped and its stream tokens are denied everywhere"`
	ClientNotified   bool   `json:"client_notified" doc:"A dismissal command was written to the session's realtime lane. It is never awaited; false means no lane was open or the write failed"`
	Delivery         string `json:"delivery" enum:"dispatched,unavailable,failed,none" doc:"dispatched: written to the lane. unavailable: no realtime lane. failed: the lane write failed. none: nothing to notify (already revoked)"`
	CommandID        string `json:"command_id,omitempty" doc:"The dismissal command identity when one was issued"`
}
type AdminPlaybackTerminateOutput struct {
	Body AdminPlaybackTerminateReceipt
}

const adminPlaybackActionTerminate = "terminate"

func registerAdminPlaybackTerminate(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/sessions/{session_id}/"+adminPlaybackActionTerminate, "terminateAdminPlaybackSession", "admin", "Durably revoke a playback session's authority first, then notify the client as best effort. The receipt reports both facts separately and never promises that buffered media stops instantly."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, op, func(ctx context.Context, in *AdminPlaybackTerminateInput) (*AdminPlaybackTerminateOutput, error) {
		svc := reg.deps.AdminPlaybackTerminate
		if svc == nil || !svc.AdminTerminateAvailable() {
			return nil, unavailable("administrator terminate")
		}
		view, err := svc.Terminate(ctx, handlers.AdminTerminateInput{SessionID: in.SessionID, ActorID: claimsFrom(ctx).UserID, Reason: in.Body.Reason})
		if err != nil {
			return nil, adminPlaybackTerminateProblem(err)
		}
		return &AdminPlaybackTerminateOutput{Body: AdminPlaybackTerminateReceipt{SessionID: view.SessionID, AuthorityRevoked: view.AuthorityRevoked, AlreadyRevoked: view.AlreadyRevoked, DurableState: view.DurableState, ClientNotified: view.ClientNotified, Delivery: view.Delivery, CommandID: view.CommandID}}, nil
	})
}

func adminPlaybackTerminateProblem(err error) *Problem {
	switch {
	case errors.Is(err, handlers.ErrAdminTerminateUnavailable):
		return unavailable("administrator terminate")
	case errors.Is(err, playback.ErrSessionNotFound):
		return NewProblem(TypeNotFound, "Playback session not found.")
	case errors.Is(err, handlers.ErrAdminPlaybackCommandInvalid):
		return validationProblem("path.session_id", codeInvalid, "The session identity is invalid.")
	}
	if operation, ok := errors.AsType[*handlers.PlaybackOperationError](err); ok {
		if operation.Status == http.StatusServiceUnavailable {
			return NewProblem(TypeConflict, "The session's durable authority could not be revoked in its current state; retry the terminate.")
		}
		return playbackProblem(err)
	}
	return serviceProblem(err)
}
