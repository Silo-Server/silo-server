package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherSuggestionDeleteService interface {
	CheckSuggestionRoomProof(string, int, string, string) error
	DeleteRoomSuggestion(context.Context, string, string, int, string) error
}

func registerWatchTogetherSuggestionDelete(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodDelete, Prefix+"/watch-together/rooms/{room_id}/suggestions/{suggestion_id}", "deleteWatchTogetherSuggestion", "realtime", "Delete one suggestion as its suggester or the room host. Missing suggestions return 404; uncertain deletion must be reconciled by reading the room."), Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.DefaultStatus = http.StatusNoContent
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherSuggestionVoteInput) (*struct{}, error) {
		svc := reg.deps.WatchTogetherSuggestionDelete
		if svc == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		if err := svc.CheckSuggestionRoomProof(in.RoomID, user, profile, in.RoomToken); err != nil {
			return nil, serviceProblem(err)
		}
		if err := svc.DeleteRoomSuggestion(ctx, in.RoomID, in.SuggestionID, user, profile); err != nil {
			if errors.Is(err, watchtogether.ErrRoomForbidden) {
				return nil, NewProblem(TypePermissionDenied, "Only the room host or suggester may delete this suggestion.")
			}
			return nil, suggestionProblem(err)
		}
		return &struct{}{}, nil
	})
}
