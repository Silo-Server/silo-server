package watchtogether

import (
	"context"
	"errors"
	"testing"
)

type deletionSuggestions struct {
	*stubSuggestions
	deletes int
	lists   int
}

func (s *deletionSuggestions) DeleteSuggestion(_ context.Context, id string) error {
	for i, row := range s.ordered {
		if row.ID == id {
			s.deletes++
			s.ordered = append(s.ordered[:i], s.ordered[i+1:]...)
			return nil
		}
	}
	return ErrSuggestionNotFound
}
func (s *deletionSuggestions) ListSuggestions(ctx context.Context, room string, user int, profile string) ([]Suggestion, error) {
	s.lists++
	return s.stubSuggestions.ListSuggestions(ctx, room, user, profile)
}
func TestSuggestionDeletePreservesAccountAndProfileOwnership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		user    int
		profile string
		allowed bool
	}{{"host", 7, "host", true}, {"host child", 7, "child", false}, {"suggester", 9, "guest", true}, {"suggester sibling", 9, "sibling", false}, {"foreign account", 10, "guest", false}} {
		t.Run(tc.name, func(t *testing.T) {
			row := Suggestion{ID: "suggestion", RoomID: "room-1", SuggesterUserID: 9, SuggesterProfileID: "guest"}
			svc, _ := newVoteRoomService(t, RoomSelectionModeVote, nil)
			store := &deletionSuggestions{stubSuggestions: &stubSuggestions{ordered: []Suggestion{row}}}
			svc.suggestions = store
			_, err := svc.DeleteSuggestion(t.Context(), "room-1", row.ID, tc.user, tc.profile)
			if !tc.allowed {
				if !errors.Is(err, ErrRoomForbidden) || store.deletes != 0 || store.lists != 0 {
					t.Fatalf("err=%v deletes=%d lists=%d", err, store.deletes, store.lists)
				}
				return
			}
			if err != nil || store.deletes != 1 || store.lists != 1 {
				t.Fatalf("err=%v deletes=%d lists=%d", err, store.deletes, store.lists)
			}
			_, err = svc.DeleteSuggestion(t.Context(), "room-1", row.ID, tc.user, tc.profile)
			if !errors.Is(err, ErrSuggestionNotFound) || store.deletes != 1 || store.lists != 1 {
				t.Fatalf("replay err=%v deletes=%d lists=%d", err, store.deletes, store.lists)
			}
		})
	}
}
