package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherSelectionModeService interface {
	UpdateWatchTogetherSelectionMode(context.Context, string, int, string, watchtogether.RoomSelectionMode) (watchtogether.Snapshot, string, error)
}
type WatchTogetherSelectionModeInput struct {
	RoomID string `path:"room_id"`
	Body   struct {
		SelectionMode watchtogether.RoomSelectionMode `json:"selection_mode" enum:"host_pick,vote"`
	}
}

func registerWatchTogetherSelectionMode(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPatch, Prefix+"/watch-together/rooms/{room_id}/selection-mode", "updateWatchTogetherRoomSelectionMode", "realtime", "Switch a lobby between host picks and voting as the host. Switching drops the staged item and every member's lobby ready state; suggestions and votes are kept. Refused once the room is playing."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.MaxBodyBytes = 1024
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherSelectionModeInput) (*WatchTogetherRoomReadOutput, error) {
		if reg.deps.WatchTogetherSelectionMode == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		row, token, err := reg.deps.WatchTogetherSelectionMode.UpdateWatchTogetherSelectionMode(ctx, in.RoomID, user, profile, in.Body.SelectionMode)
		if err != nil {
			switch {
			case errors.Is(err, watchtogether.ErrRoomForbidden):
				return nil, NewProblem(TypePermissionDenied, "Only the host account and profile may change how the room picks.")
			case errors.Is(err, watchtogether.ErrRoomNotInLobby):
				return nil, NewProblem(TypeConflict, "The room is already playing; the selection mode is fixed until it returns to the lobby.")
			case errors.Is(err, watchtogether.ErrInvalidSelection):
				return nil, NewProblem(TypeValidationFailed, "Invalid selection mode.")
			default:
				return nil, suggestionProblem(err)
			}
		}
		return watchTogetherRoomOutput(row, token)
	})
}
