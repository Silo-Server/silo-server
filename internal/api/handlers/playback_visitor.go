package handlers

import (
	"context"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// VisitorRoomFiles reports the file a Watch Party room has selected.
type VisitorRoomFiles interface {
	// VisitorRoomFile returns the media file id roomID has selected, or an
	// error when the room is closed or has nothing selected.
	VisitorRoomFile(ctx context.Context, roomID string) (int, error)
}

// checkVisitorStart confines a Watch Party visitor's playback start to its
// room. A visitor plays as the host's profile, so the account's access policy
// and stream limits apply unchanged; this check adds that the file is the
// room's current selection, pinned for the whole attempt, and that the
// session writes no resume or history state to the host's profile
// (progress_persistence "client"). Every other caller passes.
func (h *PlaybackHandler) checkVisitorStart(ctx context.Context, request playback.StartRequestV3) error {
	claims := apimw.GetClaims(ctx)
	if !claims.IsWatchPartyVisitor() {
		return nil
	}
	refused := playbackOperationError(http.StatusForbidden, "forbidden", "A Watch Party visitor can only play its room's selected file, pinned, with client-side progress")
	if h.VisitorRooms == nil || claims.WatchPartyRoomID == "" {
		return refused
	}
	if request.AllowAlternateVersions == nil || *request.AllowAlternateVersions ||
		request.ProgressPersistence != playback.ProgressPersistenceClientV3 {
		return refused
	}
	fileID, err := h.VisitorRooms.VisitorRoomFile(ctx, claims.WatchPartyRoomID)
	if err != nil || fileID <= 0 || fileID != request.FileID {
		return refused
	}
	return nil
}
