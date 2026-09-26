package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

const opFallbackWatchTogetherSource = "fallbackWatchTogetherSource"

type WatchTogetherSourceFallbackService interface {
	FallbackWatchTogetherSource(context.Context, string, int, string, string, watchtogether.SourceFallbackInput) (watchtogether.Snapshot, string, error)
}

type WatchTogetherSourceFallbackInput struct {
	RoomID    string `path:"room_id"`
	RoomToken string `header:"X-Room-Token" maxLength:"4096"`
	Body      struct {
		SelectionRevision int64  `json:"selection_revision" minimum:"1"`
		FailedFileID      ID     `json:"failed_file_id"`
		Reason            string `json:"reason" enum:"no_alternate_version,hdr_transcode_unsupported,subtitle_conversion_unsupported,transcoding_disabled"`
	}
}

func registerWatchTogetherSourceFallback(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/watch-together/rooms/{room_id}/source-fallback", opFallbackWatchTogetherSource, "realtime", "Report a playback refusal as a connected room member. Atomically replace the shared source with a lower-ranked version of the same edition and part, preserve the anchor, and reset readiness. A stale file or selection revision returns the current snapshot without changing it."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.MaxBodyBytes = 4096
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherSourceFallbackInput) (*WatchTogetherRoomReadOutput, error) {
		if reg.deps.WatchTogetherSourceFallback == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		fileID, p := in.Body.FailedFileID.positive("body.failed_file_id")
		if p != nil {
			return nil, p
		}
		row, token, err := reg.deps.WatchTogetherSourceFallback.FallbackWatchTogetherSource(ctx, in.RoomID, user, profile, in.RoomToken, watchtogether.SourceFallbackInput{SelectionRevision: in.Body.SelectionRevision, FailedFileID: fileID, Reason: in.Body.Reason})
		if errors.Is(err, watchtogether.ErrRoomForbidden) {
			return nil, NewProblem(TypePermissionDenied, "Only a connected room member may request source fallback.")
		}
		if errors.Is(err, watchtogether.ErrSourceFallbackUnavailable) {
			return nil, NewProblem(TypeConflict, "No alternative room source can satisfy this playback refusal.")
		}
		if errors.Is(err, watchtogether.ErrInvalidSelection) {
			return nil, NewProblem(TypeValidationFailed, "Invalid room source fallback.")
		}
		if err != nil {
			return nil, suggestionProblem(err)
		}
		return watchTogetherRoomOutput(row, token)
	})
}
