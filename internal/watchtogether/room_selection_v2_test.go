package watchtogether

import (
	"testing"
	"time"
)

func TestRepeatedSelectionAdvancesRevisionAndClearsReadiness(t *testing.T) {
	now := time.Now()
	repo := &stubRepo{room: baseRoom(now)}
	svc := newServiceForTest(now, repo, nil, nil, &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie-2", FileID: new(7), LibraryID: new(8)}})
	member := &memberState{userID: 7, profileID: "host", sessionID: "old-playback", isReady: true, isBuffering: true, ignoreWait: true}
	svc.rooms["room-1"].members["host"] = member
	first, err := svc.SelectItem(t.Context(), "room-1", 7, "host", SelectItemInput{ContentID: "movie-2"})
	if err != nil {
		t.Fatal(err)
	}
	if member.sessionID != "" || member.isReady || member.isBuffering || member.ignoreWait || first.AnchorPositionSeconds != 0 || first.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatal("selection reset changed")
	}
	second, err := svc.SelectItem(t.Context(), "room-1", 7, "host", SelectItemInput{ContentID: "movie-2"})
	if err != nil {
		t.Fatal(err)
	}
	if second.SelectionRevision != first.SelectionRevision+1 || second.Generation <= first.Generation {
		t.Fatal("repeat was incorrectly treated as idempotent")
	}
}
