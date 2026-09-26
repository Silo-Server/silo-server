package watchtogether

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const fallbackReasonLowerResolution = "no_alternate_version"

var ErrSourceFallbackUnavailable = errors.New("no compatible room source remains")

type SourceFallbackInput struct {
	SelectionRevision int64
	FailedFileID      int
	Reason            string
}

func (in SourceFallbackInput) Valid() bool {
	if in.SelectionRevision < 1 || in.FailedFileID < 1 {
		return false
	}
	switch in.Reason {
	case fallbackReasonLowerResolution, playback.TerminalHDRTranscodeUnsupportedV3, "subtitle_conversion_unsupported", "transcoding_disabled":
		return true
	default:
		return false
	}
}

type sourceFallbackResolver interface {
	ResolveSourceFallback(context.Context, int, string, SelectItemInput, string) (*ResolvedSelection, error)
}

// FallbackSource accepts a playback refusal from any connected viewer. The
// selected file and revision fence delayed or concurrent reports. Every viewer
// moves to the replacement source through the normal attachment/readiness barrier.
func (s *Service) FallbackSource(ctx context.Context, roomID string, user int, profile string, input SourceFallbackInput) (Snapshot, error) {
	if !input.Valid() {
		return Snapshot{}, ErrInvalidSelection
	}
	room, err := s.GetRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}
	var resolved *ResolvedSelection
	var resolveErr error
	// Catalog access stays outside the room transaction and its row lock.
	if room.SelectionRevision == input.SelectionRevision && room.SelectedFileID != nil && *room.SelectedFileID == input.FailedFileID && room.SelectedContentID != nil {
		resolver, ok := s.selectionResolver.(sourceFallbackResolver)
		if !ok {
			return Snapshot{}, ErrSourceFallbackUnavailable
		}
		resolved, resolveErr = resolver.ResolveSourceFallback(ctx, user, profile, SelectItemInput{ContentID: *room.SelectedContentID, FileID: room.SelectedFileID, LibraryID: room.SelectedLibraryID}, input.Reason)
	}
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) (Snapshot, error) {
		_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
		if err != nil {
			return Snapshot{}, err
		}
		s.mu.Lock()
		member := live.members[buildMemberKey(user, profile)]
		if !memberConnected(member) {
			s.mu.Unlock()
			return Snapshot{}, ErrRoomForbidden
		}
		if live.room.Phase == RoomPhaseEnded {
			s.mu.Unlock()
			return Snapshot{}, ErrRoomClosed
		}
		if live.room.SelectionRevision != input.SelectionRevision || live.room.SelectedFileID == nil || *live.room.SelectedFileID != input.FailedFileID {
			snapshot := s.buildSnapshotLocked(live, user, profile)
			s.mu.Unlock()
			return snapshot, nil
		}
		if resolveErr != nil || resolved == nil {
			s.mu.Unlock()
			if resolveErr != nil {
				return Snapshot{}, resolveErr
			}
			return Snapshot{}, ErrSourceFallbackUnavailable
		}
		resume := live.room.PlaybackState == RoomPlaybackStatePlaying || live.room.ResumeOnReady
		conflict, err := s.applySelectionLocked(ctx, live, resolved, expectedPosition(live.room, s.now()), resume)
		if err != nil {
			s.mu.Unlock()
			return Snapshot{}, err
		}
		snapshot := s.buildSnapshotLocked(live, user, profile)
		var dispatches []snapshotDispatch
		if !conflict {
			dispatches = s.prepareSnapshotDispatchesLocked(live)
		}
		s.mu.Unlock()
		s.sendDispatches(ctx, dispatches)
		return snapshot, nil
	})
}
