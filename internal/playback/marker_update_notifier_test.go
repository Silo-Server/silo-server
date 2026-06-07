package playback

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMarkerUpdateNotifierTargetsMatchingSessions(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	matchA, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	matchB, _ := sessions.StartSession(2, "profile-b", 100, PlayDirect, false)
	matchRequested, _ := sessions.StartSessionWithFiles(4, "profile-d", 200, 100, PlayDirect, false)
	other, _ := sessions.StartSession(3, "profile-c", 101, PlayDirect, false)

	_ = sessions.SetRealtimeConnection(matchA.ID, true)
	_ = sessions.SetRealtimeConnection(matchB.ID, true)
	_ = sessions.SetRealtimeConnection(matchRequested.ID, true)
	_ = sessions.SetRealtimeConnection(other.ID, true)

	hub := NewRealtimeHub()
	connA := &dispatchTestConn{}
	connB := &dispatchTestConn{}
	connRequested := &dispatchTestConn{}
	connOther := &dispatchTestConn{}
	regA := hub.Register(matchA.ID, connA)
	regB := hub.Register(matchB.ID, connB)
	regRequested := hub.Register(matchRequested.ID, connRequested)
	regOther := hub.Register(other.ID, connOther)
	defer hub.Unregister(regA)
	defer hub.Unregister(regB)
	defer hub.Unregister(regRequested)
	defer hub.Unregister(regOther)

	introStart := 12.0
	introEnd := 75.0
	creditsStart := 3600.0
	creditsEnd := 3660.0
	notifier := NewMarkerUpdateNotifier(sessions, hub)
	notifier.MarkersUpdated(context.Background(), &models.MediaFile{
		ID:           100,
		IntroStart:   &introStart,
		IntroEnd:     &introEnd,
		CreditsStart: &creditsStart,
		CreditsEnd:   &creditsEnd,
	})

	if len(connA.messages) != 1 {
		t.Fatalf("matching session A messages = %d, want 1", len(connA.messages))
	}
	if len(connB.messages) != 1 {
		t.Fatalf("matching session B messages = %d, want 1", len(connB.messages))
	}
	if len(connRequested.messages) != 1 {
		t.Fatalf("requested-file session messages = %d, want 1", len(connRequested.messages))
	}
	if len(connOther.messages) != 0 {
		t.Fatalf("non-matching session messages = %d, want 0", len(connOther.messages))
	}

	event, ok := connA.messages[0].(EventEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want EventEnvelope", connA.messages[0])
	}
	if event.Type != RealtimeMessageTypeEvent || event.Name != RealtimeEventMarkersUpdated {
		t.Fatalf("event = %#v, want markers updated event", event)
	}

	var payload MarkersUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.SessionID != matchA.ID || payload.FileID != 100 {
		t.Fatalf("payload = %#v, want session/file identifiers", payload)
	}
	if payload.Intro == nil || payload.Intro.Start != introStart || payload.Intro.End != introEnd {
		t.Fatalf("payload.Intro = %#v, want intro range", payload.Intro)
	}
	if payload.Credits == nil || payload.Credits.Start != creditsStart || payload.Credits.End != creditsEnd {
		t.Fatalf("payload.Credits = %#v, want credits range", payload.Credits)
	}
}

func TestMarkerUpdateNotifierSendsAllClearedMarkers(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)

	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	defer hub.Unregister(reg)

	notifier := NewMarkerUpdateNotifier(sessions, hub)
	notifier.MarkersUpdated(context.Background(), &models.MediaFile{ID: 100})

	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want 1 all-cleared marker update", len(conn.messages))
	}
	event, ok := conn.messages[0].(EventEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want EventEnvelope", conn.messages[0])
	}
	var payload MarkersUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.Intro != nil || payload.Credits != nil || payload.Recap != nil || payload.Preview != nil {
		t.Fatalf("payload markers = %#v, want all nil", payload)
	}
}
