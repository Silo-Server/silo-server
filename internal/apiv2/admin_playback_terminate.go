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
		Reason string `json:"reason,omitempty" maxLength:"1024" doc:"Free-form administrator reason. A delivered realtime command carries it as the frame's top-level reason, and a player that supports it shows it with its notice for a stop or terminate"`
	}
}

// AdminPlaybackTerminateReceipt reports the two facts separately: the
// durable revocation the server performed, and whether the client was told.
type AdminPlaybackTerminateReceipt struct {
	SessionID        string `json:"session_id" doc:"The terminated playback session"`
	AuthorityRevoked bool   `json:"authority_revoked" doc:"The session's playback authority is durably revoked: media tokens and progress writes for it are refused from now on. Buffered media already delivered may still play out"`
	AlreadyRevoked   bool   `json:"already_revoked" doc:"The session's playback attempt record already showed an administrator's end before this request: an earlier terminate, a stop the player did not confirm, or a stop the player confirmed over the control socket before its own stop reached the server. The call converged: nothing was dispatched, delivery is none and client_notified is false. A session whose record shows the player's own stop, or that expired, is not found (404), as is any ended session once its record lapses, at least 24 hours after the play started"`
	DurableState     string `json:"durable_state" enum:"stopped" doc:"stopped: the session is stopped and its stream tokens are denied everywhere"`
	ClientNotified   bool   `json:"client_notified" doc:"A dismissal command was written to the session's realtime lane. It is never awaited; false means no lane was open, the write failed, or nothing was sent because the session was already revoked"`
	Delivery         string `json:"delivery" enum:"dispatched,unavailable,failed,none" doc:"dispatched: written to the lane. unavailable: no realtime lane. failed: the lane write failed. none: nothing was sent because the session was already revoked"`
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
	case errors.Is(err, handlers.ErrAdminTerminateNoReply):
		return NewProblem(TypeDependencyUnavailable, "The server holding this playback session did not answer in time. Its playback authority is revoked, but whether the player was told to stop is unknown; retrying the terminate is safe.").WithRetryAfter(30)
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
