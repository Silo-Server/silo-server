package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherSuggestionPromoteService interface {
	CheckSuggestionRoomProof(string, int, string, string) error
	PromoteRoomSuggestion(context.Context, string, string, int, string) (watchtogether.Snapshot, string, error)
}
type WatchTogetherSuggestionPromoteInput struct {
	RoomID    string `path:"room_id"`
	RoomToken string `header:"X-Room-Token" maxLength:"4096"`
	Body      struct {
		SuggestionID string `json:"suggestion_id" minLength:"1" maxLength:"128"`
	}
}

func registerWatchTogetherSuggestionPromote(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/watch-together/rooms/{room_id}/suggestions/promote", "promoteWatchTogetherSuggestion", "realtime", "Promote an eligible suggestion as the host. Already-selected content is a no-op; a changed selection resets playback readiness. Never replay after an intervening selection."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	op.MaxBodyBytes = 4096
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherSuggestionPromoteInput) (*WatchTogetherRoomReadOutput, error) {
		svc := reg.deps.WatchTogetherSuggestionPromote
		if svc == nil {
			return nil, unavailable("suggestion promotion")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		if err := svc.CheckSuggestionRoomProof(in.RoomID, user, profile, in.RoomToken); err != nil {
			return nil, serviceProblem(err)
		}
		row, token, err := svc.PromoteRoomSuggestion(ctx, in.RoomID, in.Body.SuggestionID, user, profile)
		if err != nil {
			switch {
			case errors.Is(err, watchtogether.ErrRoomForbidden):
				return nil, NewProblem(TypePermissionDenied, "Only the host account and profile may promote suggestions.")
			case errors.Is(err, watchtogether.ErrNotVoteWinner), errors.Is(err, watchtogether.ErrNoVotesCast), errors.Is(err, watchtogether.ErrVoteRoomSelection):
				return nil, NewProblem(TypeConflict, "The suggestion is not the room's current eligible vote winner.")
			case errors.Is(err, watchtogether.ErrInvalidSelection):
				return nil, NewProblem(TypeValidationFailed, "The suggestion is not playable.")
			case errors.Is(err, watchtogether.ErrSuggestionPromotionUnavailable):
				return nil, unavailable("suggestion promotion")
			default:
				return nil, suggestionProblem(err)
			}
		}
		return watchTogetherRoomOutput(row, token)
	})
}
