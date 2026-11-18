package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubSuggestions serves an already-ordered list, the way the repository's
// "vote_count DESC, created_at ASC" query does.
type stubSuggestions struct {
	ordered []Suggestion
}

func (s *stubSuggestions) CreateSuggestion(context.Context, Suggestion) (*Suggestion, error) {
	return nil, errors.New("not used")
}

func (s *stubSuggestions) GetSuggestion(_ context.Context, id string) (*Suggestion, error) {
	for _, suggestion := range s.ordered {
		if suggestion.ID == id {
			found := suggestion
			return &found, nil
		}
	}
	return nil, ErrSuggestionNotFound
}

func (s *stubSuggestions) ListSuggestions(context.Context, string, int, string) ([]Suggestion, error) {
	out := make([]Suggestion, len(s.ordered))
	copy(out, s.ordered)
	return out, nil
}

func (s *stubSuggestions) DeleteSuggestion(context.Context, string) error     { return nil }
func (s *stubSuggestions) AddVote(context.Context, string, int, string) error { return nil }
func (s *stubSuggestions) RemoveVote(context.Context, string, int, string) error {
	return nil
}

func newVoteRoomService(t *testing.T, mode RoomSelectionMode, ordered []Suggestion) (*Service, *stubRepo) {
	t.Helper()
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	repo := &stubRepo{room: Room{
		ID:                 "room-1",
		Code:               "ROOM1234",
		JoinToken:          "TOKEN1234",
		HostUserID:         7,
		HostProfileID:      "host",
		Phase:              RoomPhaseLobby,
		SelectionMode:      mode,
		GuestControlPolicy: GuestControlPolicyHostOnly,
		IsPaused:           true,
		AnchorUpdatedAt:    now,
		Generation:         1,
		CreatedAt:          now,
	}}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{},
		&stubFiles{},
		&stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie-winner"}},
	)
	service.suggestions = &stubSuggestions{ordered: ordered}
	t.Cleanup(service.Close)
	return service, repo
}

func voteRoomSuggestions() []Suggestion {
	return []Suggestion{
		{ID: "winner", RoomID: "room-1", ContentID: "movie-winner", Title: "Heat", VoteCount: 3},
		{ID: "runner-up", RoomID: "room-1", ContentID: "movie-other", Title: "Alien", VoteCount: 1},
	}
}

func TestPromotingSomethingOtherThanTheWinnerIsRefused(t *testing.T) {
	service, _ := newVoteRoomService(t, RoomSelectionModeVote, voteRoomSuggestions())

	_, err := service.PromoteSuggestion(context.Background(), "room-1", "runner-up", 7, "host")
	if !errors.Is(err, ErrNotVoteWinner) {
		t.Fatalf("PromoteSuggestion() error = %v, want ErrNotVoteWinner", err)
	}
}

func (s *stubSuggestions) ListSuggestionsPage(ctx context.Context, room string, _ int, profile string, _ int, _ *SuggestionPosition) ([]Suggestion, bool, error) {
	rows, err := s.ListSuggestions(ctx, room, 7, profile)
	return rows, false, err
}
