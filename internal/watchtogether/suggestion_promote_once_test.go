package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPromotionOncePreservesSelectedVariantPG(t *testing.T) {
	pool := selectionPG(t)
	repo := NewRepository(pool)
	now := time.Now().UTC()
	room := baseRoom(now)
	room.SelectionMode = RoomSelectionModeVote
	if _, err := repo.CreateRoom(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	resolver := &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "winner-content", FileID: new(7), LibraryID: new(8)}}
	s := newServiceForTest(now, &stubRepo{room: room}, nil, nil, resolver)
	s.repo = repo
	suggestions := &stubSuggestions{ordered: []Suggestion{{ID: "winner", RoomID: room.ID, ContentID: "winner-content", VoteCount: 3}, {ID: "loser", RoomID: room.ID, ContentID: "loser-content", VoteCount: 1}, {ID: "foreign", RoomID: "other", ContentID: "foreign"}}}
	s.suggestions = suggestions
	conn := new(recordingConn)
	if _, _, err := s.Connect(t.Context(), room.ID, 7, "host", conn); err != nil {
		t.Fatal(err)
	}
	live := s.rooms[room.ID]
	memberKey := buildMemberKey(7, "host")
	for _, tc := range []struct {
		id      string
		user    int
		profile string
		want    error
	}{{"winner", 8, "host", ErrRoomForbidden}, {"winner", 7, "guest", ErrRoomForbidden}, {"foreign", 7, "host", ErrSuggestionNotFound}} {
		if _, err := s.PromoteSuggestionOnce(t.Context(), room.ID, tc.id, tc.user, tc.profile); !errors.Is(err, tc.want) {
			t.Fatalf("refusal: %v", err)
		}
	}
	first, err := s.PromoteSuggestionOnce(t.Context(), room.ID, "winner", 7, "host")
	if err != nil {
		t.Fatal(err)
	}
	_, err = withRoomOperation(t.Context(), s, room.ID, func(context.Context) (struct{}, error) {
		member := live.members[memberKey]
		member.sessionID = "fresh"
		member.isReady, member.isBuffering, member.ignoreWait = true, true, true
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := repo.UpdateAnchor(t.Context(), room.ID, 99, false, RoomPlaybackStatePlaying, false, now.Add(time.Second), first.Generation+1, first.Generation)
	if err != nil {
		t.Fatal(err)
	}
	live.room = *advanced
	before := len(conn.payloads)
	resolver.resolved = &ResolvedSelection{ContentID: "winner-content", FileID: new(9), LibraryID: new(8)}
	repeat, err := s.PromoteSuggestionOnce(t.Context(), room.ID, "winner", 7, "host")
	member := live.members[memberKey]
	if err != nil || repeat.Generation != advanced.Generation || repeat.SelectionRevision != first.SelectionRevision || repeat.AnchorPositionSeconds != 99 || repeat.IsPaused || *repeat.SelectedFileID != 7 || member.sessionID != "fresh" || !member.isReady || !member.isBuffering || !member.ignoreWait || len(conn.payloads) != before {
		t.Fatalf("repeat reset: %+v %v", repeat, err)
	}
	// Direct host-pick selection still compares the explicit resolved file variant.
	if _, err = pool.Exec(t.Context(), `UPDATE watch_together_rooms SET selection_mode='host_pick' WHERE id=$1`, room.ID); err != nil {
		t.Fatal(err)
	}
	live.room.SelectionMode = RoomSelectionModeHostPick
	changed, err := s.SelectItemOnce(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "winner-content", FileID: new(9)})
	member = live.members[memberKey]
	if err != nil || *changed.SelectedFileID != 9 || changed.SelectionRevision != first.SelectionRevision+1 || member.sessionID != "" {
		t.Fatalf("direct selection comparison changed: %+v %v", changed, err)
	}
	// The host may override the tally: promoting the runner-up starts it.
	live.room.SelectionMode = RoomSelectionModeVote
	if _, err = pool.Exec(t.Context(), `UPDATE watch_together_rooms SET selection_mode='vote' WHERE id=$1`, room.ID); err != nil {
		t.Fatal(err)
	}
	resolver.resolved = &ResolvedSelection{ContentID: "loser-content", FileID: new(3), LibraryID: new(8)}
	overridden, err := s.PromoteSuggestionOnce(t.Context(), room.ID, "loser", 7, "host")
	if err != nil || overridden.SelectedContentID == nil || *overridden.SelectedContentID != "loser-content" {
		t.Fatalf("host override: %+v %v", overridden, err)
	}
	// Closed rooms still refuse promotion.
	if _, err = pool.Exec(t.Context(), `UPDATE watch_together_rooms SET phase='ended' WHERE id=$1`, room.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PromoteSuggestionOnce(t.Context(), room.ID, "winner", 7, "host"); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("closed: %v", err)
	}
}
