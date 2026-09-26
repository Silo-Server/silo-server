package watchtogether

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

func TestClusterRefreshDoesNotBlockSubscriberPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	tx, err := f.repo.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), `SELECT id FROM watch_together_rooms WHERE id=$1 FOR UPDATE`, f.roomID); err != nil {
		t.Fatal(err)
	}
	event := cache.Event{Type: "watch_together_room_state", Payload: `{"source":"other","room_id":"` + f.roomID + `"}`}
	delivered := make(chan struct{})
	go func() {
		for range 100 {
			f.host.handleClusterEvent(event)
		}
		close(delivered)
	}()
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("room row lock blocked the PubSub subscriber")
	}
	f.host.mu.Lock()
	live := f.host.rooms[f.roomID]
	coalesced := live.reconciling && live.reconcilePending
	f.host.mu.Unlock()
	if !coalesced {
		t.Fatal("notifications were not coalesced behind the active refresh")
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.host.mu.Lock()
		done := !live.reconciling && !live.reconcilePending
		f.host.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued refresh did not finish after releasing the row lock")
		}
		runtime.Gosched()
	}
}

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
