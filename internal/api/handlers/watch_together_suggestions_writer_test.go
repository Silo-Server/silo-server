package handlers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func TestWatchTogetherSuggestionRowsMatchSocketProtocols(t *testing.T) {
	frame := map[string]any{"type": "suggestions_update", "suggestions": []watchtogether.Suggestion{{ID: "suggestion", VoteCount: 2, VotedByMe: false}}}
	var first string
	for _, v2 := range []bool{false, true} {
		socket := newRoomWriterSocket(false)
		conn := newWatchTogetherRoomConn(socket)
		conn.includeMemberStatus = v2
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.WriteJSON(frame); err != nil {
			t.Fatal(err)
		}
		select {
		case data := <-socket.frames:
			if first == "" {
				first = data
			} else if data != first {
				t.Fatalf("v1 %s != v2 %s", first, data)
			}
		case <-time.After(time.Second):
			t.Fatal("suggestion frame not written")
		}
	}
}
