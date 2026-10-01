package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherCloseService interface {
	CloseWatchTogetherRoom(context.Context, string, int, string) error
}
type WatchTogetherCloseInput struct {
	RoomID string `path:"room_id"`
}

func registerWatchTogetherClose(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodDelete, Prefix+"/watch-together/rooms/{room_id}", "closeWatchTogetherRoom", "realtime", "End the room as its host account and profile. Already-ended rooms return conflict; closing does not cancel already-dispatched playback."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.DefaultStatus = http.StatusNoContent
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherCloseInput) (*struct{}, error) {
		svc := reg.deps.WatchTogetherClose
		if svc == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		if err := svc.CloseWatchTogetherRoom(ctx, in.RoomID, user, profile); err != nil {
			if errors.Is(err, watchtogether.ErrRoomForbidden) {
				return nil, NewProblem(TypePermissionDenied, "Only the host account and profile may close this room.")
			}
			return nil, suggestionProblem(err)
		}
		return &struct{}{}, nil
	})
}
