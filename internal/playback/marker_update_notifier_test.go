package playback

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMarkerUpdateNotifierTargetsMatchingSessions(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	hub := NewRealtimeHub()
	matchA := startPlaybackTestSession(t, sessions, 1, "profile-a", 100, PlayDirect)
	matchB := startPlaybackTestSession(t, sessions, 2, "profile-b", 100, PlayDirect)
	matchRequested, err := sessions.StartSessionWithFiles(4, "profile-d", 200, 100, PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSessionWithFiles: %v", err)
	}
	other := startPlaybackTestSession(t, sessions, 3, "profile-c", 101, PlayDirect)
	connA := connectRealtimeTestConn(t, sessions, hub, matchA.ID)
	connB := connectRealtimeTestConn(t, sessions, hub, matchB.ID)
	connRequested := connectRealtimeTestConn(t, sessions, hub, matchRequested.ID)
	connOther := connectRealtimeTestConn(t, sessions, hub, other.ID)

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

type markerUpdateTestBus struct {
	handlers []func(string)
	events   []string
}

func (b *markerUpdateTestBus) publish(_ context.Context, payload string) error {
	b.events = append(b.events, payload)
	for _, handler := range b.handlers {
		handler(payload)
	}
	return nil
}

func (b *markerUpdateTestBus) subscribe(_ context.Context, handler func(string)) error {
	b.handlers = append(b.handlers, handler)
	return nil
}

func newMarkerUpdateTestReplica(t *testing.T) (*MarkerUpdateNotifier, *dispatchTestConn) {
	t.Helper()
	sessions := NewSessionManager(0, 0)
	hub := NewRealtimeHub()
	session := startPlaybackTestSession(t, sessions, 1, "profile-a", 100, PlayDirect)
	return NewMarkerUpdateNotifier(sessions, hub), connectRealtimeTestConn(t, sessions, hub, session.ID)
}

func TestMarkerUpdateNotifierDeliversAcrossReplicasOnce(t *testing.T) {
	bus := &markerUpdateTestBus{}
	local, localConn := newMarkerUpdateTestReplica(t)
	remote, remoteConn := newMarkerUpdateTestReplica(t)
	ctx := context.Background()
	for _, notifier := range []*MarkerUpdateNotifier{local, remote, local} {
		if err := notifier.UseEventBus(ctx, bus.publish, bus.subscribe); err != nil {
			t.Fatal(err)
		}
	}
	if len(bus.handlers) != 2 {
		t.Fatalf("subscriptions = %d, want 2", len(bus.handlers))
	}
	segments := []models.MarkerSegment{
		{Kind: "credits", StartSeconds: 100, EndSeconds: 120},
		{Kind: "credits", StartSeconds: 150, EndSeconds: 180},
	}
	local.MarkersUpdated(ctx, &models.MediaFile{ID: 100, MarkerSegments: segments})
	if len(bus.events) != 1 {
		t.Fatalf("published events = %d, want 1 without rebroadcast", len(bus.events))
	}
	for name, conn := range map[string]*dispatchTestConn{"local": localConn, "remote": remoteConn} {
		if len(conn.messages) != 1 {
			t.Fatalf("%s messages = %d, want 1", name, len(conn.messages))
		}
		event := conn.messages[0].(EventEnvelope)
		var payload MarkersUpdatedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(payload.MarkerSegments, segments) {
			t.Fatalf("%s marker segments = %#v, want %#v", name, payload.MarkerSegments, segments)
		}
		if payload.Credits == nil || payload.Credits.Start != 100 || payload.Credits.End != 120 {
			t.Fatalf("%s credits = %#v, want first occurrence", name, payload.Credits)
		}
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal([]byte(bus.events[0]), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 3 || snapshot["source_id"] == nil || snapshot["file_id"] == nil || snapshot["marker_segments"] == nil {
		t.Fatalf("event fields = %#v, want only source, file, and marker snapshot", snapshot)
	}

	local.MarkersUpdated(ctx, &models.MediaFile{ID: 100})
	for name, conn := range map[string]*dispatchTestConn{"local": localConn, "remote": remoteConn} {
		if len(conn.messages) != 2 {
			t.Fatalf("%s messages = %d, want marker removal update", name, len(conn.messages))
		}
		var cleared MarkersUpdatedPayload
		if err := json.Unmarshal(conn.messages[1].(EventEnvelope).Payload, &cleared); err != nil {
			t.Fatal(err)
		}
		if cleared.MarkerSegments == nil || len(cleared.MarkerSegments) != 0 || cleared.Intro != nil || cleared.Credits != nil || cleared.Recap != nil || cleared.Preview != nil {
			t.Fatalf("%s cleared markers = %#v", name, cleared)
		}
	}
}

func TestMarkerUpdateNotifierDeliversLocallyBeforePublishExhaustsContext(t *testing.T) {
	notifier, conn := newMarkerUpdateTestReplica(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publish := func(context.Context, string) error {
		cancel()
		return context.Canceled
	}
	subscribe := func(context.Context, func(string)) error { return nil }
	if err := notifier.UseEventBus(ctx, publish, subscribe); err != nil {
		t.Fatal(err)
	}
	notifier.MarkersUpdated(ctx, &models.MediaFile{ID: 100})
	if len(conn.messages) != 1 {
		t.Fatalf("local messages = %d, want 1 before publish failure", len(conn.messages))
	}
}

func TestMarkerUpdateNotifierRespectsCancellation(t *testing.T) {
	bus := &markerUpdateTestBus{}
	local, localConn := newMarkerUpdateTestReplica(t)
	remote, remoteConn := newMarkerUpdateTestReplica(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := local.UseEventBus(context.Background(), bus.publish, bus.subscribe); err != nil {
		t.Fatal(err)
	}
	if err := remote.UseEventBus(ctx, bus.publish, bus.subscribe); err != nil {
		t.Fatal(err)
	}
	cancel()
	local.MarkersUpdated(ctx, &models.MediaFile{ID: 100})
	if len(bus.events) != 0 || len(localConn.messages) != 0 || len(remoteConn.messages) != 0 {
		t.Fatal("canceled update was delivered")
	}
	local.MarkersUpdated(context.Background(), &models.MediaFile{ID: 100})
	if len(localConn.messages) != 1 || len(remoteConn.messages) != 0 {
		t.Fatalf("messages after receiver shutdown: local=%d remote=%d, want 1 and 0", len(localConn.messages), len(remoteConn.messages))
	}
}
