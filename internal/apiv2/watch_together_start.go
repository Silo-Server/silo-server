package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherStartService interface {
	StartWatchTogetherPlayback(context.Context, string, int, string) (watchtogether.Snapshot, string, error)
}
type WatchTogetherStartInput struct {
	RoomID string `path:"room_id"`
}

func registerWatchTogetherStart(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/watch-together/rooms/{room_id}/playback/start", "startWatchTogetherRoomPlayback", "realtime", "Start the staged content for everyone as the host. The room moves to playing and waits for members to buffer, exactly as a direct selection does. An already playing room returns its current snapshot unchanged."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherStartInput) (*WatchTogetherRoomReadOutput, error) {
		if reg.deps.WatchTogetherStart == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		row, token, err := reg.deps.WatchTogetherStart.StartWatchTogetherPlayback(ctx, in.RoomID, user, profile)
		if err != nil {
			if errors.Is(err, watchtogether.ErrRoomForbidden) {
				return nil, NewProblem(TypePermissionDenied, "Only the host account and profile may start playback.")
			}
			if errors.Is(err, watchtogether.ErrNoStagedSelection) {
				return nil, NewProblem(TypeConflict, "Stage something before starting playback.")
			}
			return nil, suggestionProblem(err)
		}
		return watchTogetherRoomOutput(row, token)
	})
}
