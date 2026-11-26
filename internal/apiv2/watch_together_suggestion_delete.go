package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherSuggestionDeleteService interface {
	CheckSuggestionRoomProof(string, int, string, string) error
	// ownOnly drops the host's power to delete any suggestion.
	DeleteRoomSuggestion(ctx context.Context, room, suggestion string, user int, profile string, ownOnly bool) error
}

func registerWatchTogetherSuggestionDelete(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodDelete, Prefix+"/watch-together/rooms/{room_id}/suggestions/{suggestion_id}", "deleteWatchTogetherSuggestion", "realtime", "Delete one suggestion as its suggester or the room host. Missing suggestions return 404; uncertain deletion must be reconciled by reading the room."), Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.DefaultStatus = http.StatusNoContent
	op.Errors = []int{409}
	op.Description = "A login session opened by another app the person allowed (its app key was enrolled through a grant) deletes only a suggestion its account and profile made; " +
		"it does not get the host's power to delete any suggestion, so another one is 403 permission_denied."
	Register(reg, withOrigins(op, grantedOwnSuggestion), func(ctx context.Context, in *WatchTogetherSuggestionVoteInput) (*struct{}, error) {
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
		// A session another app opened deletes only its own suggestions: as
		// the host's account and profile it would otherwise remove other
		// members' suggestions and change which one wins a vote.
		reaches, p := reg.sessionReachesOtherDevices(ctx)
		if p != nil {
			return nil, p
		}
		if err := svc.DeleteRoomSuggestion(ctx, in.RoomID, in.SuggestionID, user, profile, !reaches); err != nil {
			if errors.Is(err, watchtogether.ErrRoomForbidden) {
				return nil, NewProblem(TypePermissionDenied, "Only the room host or suggester may delete this suggestion.")
			}
			return nil, suggestionProblem(err)
		}
		return &struct{}{}, nil
	})
}
