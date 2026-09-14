package watchtogether

import (
	"context"
	"errors"
)

var ErrSuggestionCreateUnavailable = errors.New("suggestion creation unavailable")

type suggestionCreator interface {
	CreateSuggestionOnce(context.Context, Suggestion) (*Suggestion, bool, error)
}

// CreateSuggestionWithIdentity uses a retained receipt without replaying a
// previously committed creation's local broadcast attempt.
func (s *Service) CreateSuggestionWithIdentity(ctx context.Context, roomID, id string, user int, profile string, input CreateSuggestionInput) (string, error) {
	if s == nil {
		return "", ErrSuggestionCreateUnavailable
	}
	store, ok := s.suggestions.(suggestionCreator)
	if !ok {
		return "", ErrSuggestionCreateUnavailable
	}
	row, inserted, err := store.CreateSuggestionOnce(ctx, Suggestion{ID: id, RoomID: roomID, SuggesterUserID: user, SuggesterProfileID: profile, ContentID: input.ContentID, ContentType: input.ContentType, Title: input.Title, Subtitle: input.Subtitle, PosterURL: input.PosterURL, Note: input.Note, CreatedAt: s.now()})
	if err != nil {
		return "", err
	}
	if inserted {
		suggestions, err := s.suggestions.ListSuggestions(ctx, roomID, user, profile)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		live := s.rooms[roomID]
		if live != nil && live.room.Phase != RoomPhaseEnded {
			dispatches := s.prepareSuggestionDispatchesLocked(live, suggestions)
			s.mu.Unlock()
			s.runDispatches(dispatches)
		} else {
			s.mu.Unlock()
		}
		s.publishSuggestionUpdate(roomID)
	}
	return row.ID, nil
}
