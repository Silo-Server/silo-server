package playback

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	if payload.MarkerSegments == nil || len(payload.MarkerSegments) != 0 {
		t.Fatalf("payload.MarkerSegments = %#v, want an empty array", payload.MarkerSegments)
	}
}

type markerUpdateTestBus struct {
	handlers   []func(string)
	events     []string
	publishErr error
}

func (b *markerUpdateTestBus) publish(_ context.Context, payload string) error {
	b.events = append(b.events, payload)
	if b.publishErr != nil {
		return b.publishErr
	}
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
	session, err := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.SetRealtimeConnection(session.ID, true); err != nil {
		t.Fatal(err)
	}
	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	t.Cleanup(func() { hub.Unregister(reg) })
	return NewMarkerUpdateNotifier(sessions, hub), conn
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
	if len(remoteConn.messages) != 2 {
		t.Fatalf("remote messages = %d, want marker removal update", len(remoteConn.messages))
	}
	var cleared MarkersUpdatedPayload
	if err := json.Unmarshal(remoteConn.messages[1].(EventEnvelope).Payload, &cleared); err != nil {
		t.Fatal(err)
	}
	if len(cleared.MarkerSegments) != 0 || cleared.Credits != nil {
		t.Fatalf("cleared markers = %#v", cleared)
	}
}

func TestMarkerUpdateNotifierKeepsLocalDeliveryWhenPublishFails(t *testing.T) {
	notifier, conn := newMarkerUpdateTestReplica(t)
	bus := &markerUpdateTestBus{publishErr: errors.New("event bus unavailable")}
	if err := notifier.UseEventBus(context.Background(), bus.publish, bus.subscribe); err != nil {
		t.Fatal(err)
	}
	notifier.MarkersUpdated(context.Background(), &models.MediaFile{ID: 100})
	if len(conn.messages) != 1 {
		t.Fatalf("local messages = %d, want 1 despite publish failure", len(conn.messages))
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
