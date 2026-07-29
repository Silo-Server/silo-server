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

func (s *stubSuggestions) ListSuggestions(context.Context, string, string) ([]Suggestion, error) {
	out := make([]Suggestion, len(s.ordered))
	copy(out, s.ordered)
	return out, nil
}

func (s *stubSuggestions) DeleteSuggestion(context.Context, string) error { return nil }
func (s *stubSuggestions) AddVote(context.Context, string, string) error  { return nil }
func (s *stubSuggestions) RemoveVote(context.Context, string, string) error {
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

// The gate on direct selection and the gate on promotion sit on the same code
// path, so a vote room can very easily end up with no way in at all. This is
// the test that catches that: promoting the winner must still start playback.
func TestPromotingTheWinnerStartsAVoteRoom(t *testing.T) {
	service, _ := newVoteRoomService(t, RoomSelectionModeVote, voteRoomSuggestions())

	snapshot, err := service.PromoteSuggestion(context.Background(), "room-1", "winner", 7, "host")
	if err != nil {
		t.Fatalf("PromoteSuggestion() error = %v, want the vote winner to start", err)
	}
	if snapshot.Phase != RoomPhasePlaying {
		t.Fatalf("phase = %q, want %q", snapshot.Phase, RoomPhasePlaying)
	}
	if snapshot.SelectedContentID == nil || *snapshot.SelectedContentID != "movie-winner" {
		t.Fatalf("selected content = %v, want movie-winner", snapshot.SelectedContentID)
	}
}

func TestPromotingSomethingOtherThanTheWinnerIsRefused(t *testing.T) {
	service, _ := newVoteRoomService(t, RoomSelectionModeVote, voteRoomSuggestions())

	_, err := service.PromoteSuggestion(context.Background(), "room-1", "runner-up", 7, "host")
	if !errors.Is(err, ErrNotVoteWinner) {
		t.Fatalf("PromoteSuggestion() error = %v, want ErrNotVoteWinner", err)
	}
}

func TestPromotingBeforeAnyoneVotesIsRefused(t *testing.T) {
	unvoted := []Suggestion{{ID: "a", RoomID: "room-1", ContentID: "movie-a", VoteCount: 0}}
	service, _ := newVoteRoomService(t, RoomSelectionModeVote, unvoted)

	_, err := service.PromoteSuggestion(context.Background(), "room-1", "a", 7, "host")
	if !errors.Is(err, ErrNoVotesCast) {
		t.Fatalf("PromoteSuggestion() error = %v, want ErrNoVotesCast", err)
	}
}

// The host bypassing the tally with a direct selection would make the counts on
// everyone else's screen decoration.
func TestDirectSelectionIsRefusedInAVoteRoom(t *testing.T) {
	service, _ := newVoteRoomService(t, RoomSelectionModeVote, voteRoomSuggestions())

	_, err := service.SelectItem(context.Background(), "room-1", 7, "host", SelectItemInput{
		ContentID: "movie-winner",
	})
	if !errors.Is(err, ErrVoteRoomSelection) {
		t.Fatalf("SelectItem() error = %v, want ErrVoteRoomSelection", err)
	}
}

// Neither gate applies to a host_pick room: the host picks, votes are not part
// of the mode, and an unvoted suggestion is still promotable.
func TestHostPickRoomIsUntouchedByTheVoteGates(t *testing.T) {
	unvoted := []Suggestion{{ID: "a", RoomID: "room-1", ContentID: "movie-winner", VoteCount: 0}}
	service, _ := newVoteRoomService(t, RoomSelectionModeHostPick, unvoted)

	if _, err := service.SelectItem(context.Background(), "room-1", 7, "host", SelectItemInput{
		ContentID: "movie-winner",
	}); err != nil {
		t.Fatalf("SelectItem() error = %v, want a host_pick room to select directly", err)
	}
	if _, err := service.PromoteSuggestion(context.Background(), "room-1", "a", 7, "host"); err != nil {
		t.Fatalf("PromoteSuggestion() error = %v, want a host_pick room to promote freely", err)
	}
}
