package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherStageService interface {
	StageWatchTogetherItem(context.Context, string, int, string, watchtogether.SelectItemInput) (watchtogether.Snapshot, string, error)
}
type WatchTogetherStageInput struct {
	RoomID string `path:"room_id"`
	Body   struct {
		ContentID ID  `json:"content_id" minLength:"1"`
		FileID    *ID `json:"file_id,omitempty"`
		LibraryID *ID `json:"library_id,omitempty"`
	}
}

func registerWatchTogetherStage(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPut, Prefix+"/watch-together/rooms/{room_id}/staged-selection", "stageWatchTogetherRoomItem", "realtime", "Stage playable content in a host-pick lobby without starting it. The room stays in the lobby until the host starts playback; staging the same resolved content, file, and library again is a no-op. Refused once the room is playing."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.MaxBodyBytes = 4096
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherStageInput) (*WatchTogetherRoomReadOutput, error) {
		if reg.deps.WatchTogetherStage == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		input := watchtogether.SelectItemInput{ContentID: string(in.Body.ContentID)}
		if in.Body.FileID != nil {
			n, p := in.Body.FileID.positive("body.file_id")
			if p != nil {
				return nil, p
			}
			input.FileID = new(n)
		}
		if in.Body.LibraryID != nil {
			n, p := in.Body.LibraryID.positive("body.library_id")
			if p != nil {
				return nil, p
			}
			input.LibraryID = new(n)
		}
		row, token, err := reg.deps.WatchTogetherStage.StageWatchTogetherItem(ctx, in.RoomID, user, profile, input)
		if err != nil {
			return nil, watchTogetherStageProblem(err)
		}
		return watchTogetherRoomOutput(row, token)
	})
}

func watchTogetherStageProblem(err error) error {
	switch {
	case errors.Is(err, watchtogether.ErrRoomForbidden):
		return NewProblem(TypePermissionDenied, "Only the host account and profile may stage content.")
	case errors.Is(err, watchtogether.ErrVoteRoomSelection):
		return NewProblem(TypeConflict, "This room votes for what plays; suggest instead.")
	case errors.Is(err, watchtogether.ErrRoomNotInLobby):
		return NewProblem(TypeConflict, "The room is already playing; select content to switch it.")
	case errors.Is(err, watchtogether.ErrInvalidSelection):
		return NewProblem(TypeValidationFailed, "Content is not playable in this room.")
	default:
		return suggestionProblem(err)
	}
}
