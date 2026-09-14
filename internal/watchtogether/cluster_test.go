package watchtogether

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

func TestClusterSuggestionUpdateSkipsClosedRoom(t *testing.T) {
	now := time.Now().UTC()
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.Phase = RoomPhaseEnded
	service := newServiceForTest(now, repo, nil, nil, nil)
	conn := &recordingConn{}
	service.rooms[repo.room.ID].members["member"] = &memberState{connection: conn, userID: 7, profileID: "profile"}
	service.suggestions = &stubSuggestions{ordered: []Suggestion{{ID: "suggestion"}}}
	service.handleClusterEvent(cache.Event{Type: "watch_together_suggestions", Payload: `{"source":"other","room_id":"` + repo.room.ID + `"}`})
	if len(conn.payloads) != 0 {
		t.Fatalf("closed room received %d suggestion updates", len(conn.payloads))
	}
}
