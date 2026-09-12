package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherSelectionService interface {
	SelectWatchTogetherItem(context.Context, string, int, string, watchtogether.SelectItemInput) (watchtogether.Snapshot, string, error)
}
type WatchTogetherSelectionInput struct {
	RoomID string `path:"room_id"`
	Body   struct {
		ContentID ID  `json:"content_id" minLength:"1"`
		FileID    *ID `json:"file_id,omitempty"`
		LibraryID *ID `json:"library_id,omitempty"`
	}
}

func registerWatchTogetherSelection(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPut, Prefix+"/watch-together/rooms/{room_id}/selection", "selectWatchTogetherRoomItem", "realtime", "Select playable content as the host in a host-pick room. An identical current resolved selection is a no-op; changing selection resets readiness and playback anchor. Never replay an uncertain selection after another selection."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	op.MaxBodyBytes = 4096
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherSelectionInput) (*WatchTogetherRoomReadOutput, error) {
		if reg.deps.WatchTogetherSelection == nil {
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
		row, token, err := reg.deps.WatchTogetherSelection.SelectWatchTogetherItem(ctx, in.RoomID, user, profile, input)
		if err != nil {
			if errors.Is(err, watchtogether.ErrRoomForbidden) {
				return nil, NewProblem(TypePermissionDenied, "Only the host account and profile may change room selection.")
			}
			if errors.Is(err, watchtogether.ErrVoteRoomSelection) {
				return nil, NewProblem(TypeConflict, "This room votes for what plays; promote the winning suggestion instead.")
			}
			if errors.Is(err, watchtogether.ErrInvalidSelection) {
				return nil, NewProblem(TypeValidationFailed, "Content is not playable in this room.")
			}
			return nil, suggestionProblem(err)
		}
		return watchTogetherRoomOutput(row, token)
	})
}
