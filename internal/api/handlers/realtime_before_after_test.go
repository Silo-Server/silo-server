package handlers

import (
	"testing"
	"testing/synctest"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type realtimeProofLookup struct{ calls int }

func (s *realtimeProofLookup) GetSession(string) (*playback.Session, error) {
	s.calls++
	return nil, watchtogether.ErrSessionMismatch
}

// Nine sequential synthetic calls perform only a stub counter increment. This
// verifies admission placement without sockets, data stores or expensive work.
func TestRealtimeRoomAdmissionStopsLookupAtMemberAllowance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lookup := new(realtimeProofLookup)
		service := watchtogether.NewService(nil, lookup, nil, nil, nil, nil)
		defer service.Close()
		handler := &WatchTogetherHandler{Service: service}
		connection := new(watchTogetherRoomConn)
		connection.roomID = "synthetic-room"
		registration := new(watchtogether.Registration)
		for range 9 {
			_ = handler.handleRoomClientMessage(t.Context(), connection, registration, 7, "synthetic-profile", []byte(`{"type":"attach_session","session_id":"synthetic-playback"}`))
		}
		if lookup.calls != 8 {
			t.Fatalf("downstream stub lookups=%d, want 8; ninth attempt must be refused before service entry", lookup.calls)
		}
	})
}
