package watchtogether

import (
	"context"
	"errors"
)

var ErrSuggestionPromotionUnavailable = errors.New("suggestion promotion unavailable")

func (s *Service) PromoteSuggestionOnce(
	ctx context.Context,
	roomID string,
	suggestionID string,
	userID int,
	profileID string,
) (Snapshot, error) {
	if s == nil || s.suggestions == nil {
		return Snapshot{}, ErrSuggestionPromotionUnavailable
	}

	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	if live.room.HostUserID != userID || live.room.HostProfileID != profileID {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomForbidden
	}
	s.mu.Unlock()

	suggestion, err := s.suggestions.GetSuggestion(ctx, suggestionID)
	if err != nil {
		return Snapshot{}, err
	}
	if suggestion.RoomID != roomID {
		return Snapshot{}, ErrSuggestionNotFound
	}

	// The tally is advice, not a lock: the host may start any suggestion in a
	// vote room. Everyone sees which one was chosen because the selection is
	// broadcast, so a host override is visible rather than silent. viaVote
	// stays set so the vote-room gate in selectItem lets the promotion in.
	s.mu.Lock()
	isVoteRoom := live.room.SelectionMode == RoomSelectionModeVote
	s.mu.Unlock()

	return s.selectItemOnce(ctx, roomID, userID, profileID, SelectItemInput{
		ContentID: suggestion.ContentID,
	}, isVoteRoom, true)
}
