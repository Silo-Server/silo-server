package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherCreateService interface {
	CreateWatchTogetherRoom(context.Context, string, int, string, watchtogether.RoomSelectionMode) (watchtogether.Snapshot, string, error)
}
type WatchTogetherCreateInput struct {
	Body struct {
		RoomID        string                          `json:"room_id" format:"uuid"`
		SelectionMode watchtogether.RoomSelectionMode `json:"selection_mode" enum:"host_pick,vote"`
	}
}

func registerWatchTogetherCreate(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/watch-together/rooms", "createWatchTogetherRoom", "realtime", "Create a room with a retained caller-selected identity. Exact replay returns current active state and retained invite credentials without connecting or resetting members. Room proof is not a session-bound socket credential."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyUniqueConstraint}
	op.DefaultStatus = http.StatusCreated
	op.MaxBodyBytes = 4096
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherCreateInput) (*WatchTogetherRoomReadOutput, error) {
		if reg.deps.WatchTogetherCreate == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		row, token, err := reg.deps.WatchTogetherCreate.CreateWatchTogetherRoom(ctx, in.Body.RoomID, user, profile, in.Body.SelectionMode)
		if err != nil {
			switch {
			case errors.Is(err, watchtogether.ErrRoomCreationIdentityConflict):
				return nil, NewProblem(TypeConflict, "Room creation identity was already used.")
			case errors.Is(err, watchtogether.ErrInvalidSelection):
				return nil, NewProblem(TypeValidationFailed, "Invalid room creation input.")
			case errors.Is(err, watchtogether.ErrRoomCreateUnavailable):
				return nil, unavailable("watch together")
			default:
				return nil, suggestionProblem(err)
			}
		}
		return watchTogetherRoomOutput(row, token)
	})
}
