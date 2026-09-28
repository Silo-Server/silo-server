package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherStopService interface {
	StopWatchTogetherPlayback(context.Context, string, int, string) (watchtogether.Snapshot, string, error)
}
type WatchTogetherStopInput struct {
	RoomID string `path:"room_id"`
}

func registerWatchTogetherStop(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/watch-together/rooms/{room_id}/playback/stop", "stopWatchTogetherRoomPlayback", "realtime", "Stop playback for everyone as the host without ending the room. The room returns to the lobby with the same item still staged, so the host can start it again or pick something else. A room that is not playing returns its current snapshot unchanged."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherStopInput) (*WatchTogetherRoomReadOutput, error) {
		if reg.deps.WatchTogetherStop == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		row, token, err := reg.deps.WatchTogetherStop.StopWatchTogetherPlayback(ctx, in.RoomID, user, profile)
		if err != nil {
			if errors.Is(err, watchtogether.ErrRoomForbidden) {
				return nil, NewProblem(TypePermissionDenied, "Only the host account and profile may stop playback.")
			}
			return nil, suggestionProblem(err)
		}
		return watchTogetherRoomOutput(row, token)
	})
}
