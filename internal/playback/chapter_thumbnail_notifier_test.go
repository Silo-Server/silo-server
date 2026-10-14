package playback

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type chapterThumbnailTestPresigner struct{}

func (chapterThumbnailTestPresigner) DirectURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://example.com/" + key, nil
}

func TestChapterThumbnailNotifierTargetsMatchingSessions(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	matchA, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	matchB, _ := sessions.StartSession(2, "profile-b", 100, PlayDirect, false)
	other, _ := sessions.StartSession(3, "profile-c", 101, PlayDirect, false)

	_ = sessions.SetRealtimeConnection(matchA.ID, true)
	_ = sessions.SetRealtimeConnection(matchB.ID, true)
	_ = sessions.SetRealtimeConnection(other.ID, true)

	hub := NewRealtimeHub()
	connA := &dispatchTestConn{}
	connB := &dispatchTestConn{}
	connOther := &dispatchTestConn{}
	regA := hub.Register(matchA.ID, connA)
	regB := hub.Register(matchB.ID, connB)
	regOther := hub.Register(other.ID, connOther)
	defer hub.Unregister(regA)
	defer hub.Unregister(regB)
	defer hub.Unregister(regOther)

	notifier := NewChapterThumbnailNotifier(sessions, hub, chapterThumbnailTestPresigner{}.DirectURL, 0)
	notifier.ChapterThumbnailReady(
		context.Background(),
		100,
		7,
		"chapter-images/100/7/w300.webp",
		"thumbhash",
	)

	if len(connA.messages) != 1 {
		t.Fatalf("matching session A messages = %d, want 1", len(connA.messages))
	}
	if len(connB.messages) != 1 {
		t.Fatalf("matching session B messages = %d, want 1", len(connB.messages))
	}
	if len(connOther.messages) != 0 {
		t.Fatalf("non-matching session messages = %d, want 0", len(connOther.messages))
	}

	event, ok := connA.messages[0].(EventEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want EventEnvelope", connA.messages[0])
	}
	if event.Type != RealtimeMessageTypeEvent || event.Name != RealtimeEventChapterThumbnailReady {
		t.Fatalf("event = %#v, want chapter thumbnail event", event)
	}
	var payload ChapterThumbnailReadyPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.SessionID != matchA.ID || payload.FileID != 100 || payload.ChapterIndex != 7 {
		t.Fatalf("payload = %#v, want matching session/file/chapter identifiers", payload)
	}
	// thumbnail_path names the served object; the notifier signs it as is.
	if want := "https://example.com/chapter-images/100/7/w300.webp"; payload.ThumbnailURL != want {
		t.Fatalf("thumbnail_url = %q, want %q", payload.ThumbnailURL, want)
	}
}

func TestMarkerThumbnailNotifierRequiresNegotiation(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	opted, _ := sessions.StartSession(1, "profile", 100, PlayDirect, false)
	legacy, _ := sessions.StartSession(2, "profile", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(opted.ID, true)
	_ = sessions.SetRealtimeConnection(legacy.ID, true)
	hub := NewRealtimeHub()
	a, b := &dispatchTestConn{}, &dispatchTestConn{}
	regA, regB := hub.Register(opted.ID, a), hub.Register(legacy.ID, b)
	defer hub.Unregister(regA)
	defer hub.Unregister(regB)
	store := NewMemoryPlanStoreV3()
	if err := store.SaveAttempt(t.Context(), AttemptRecordV3{PlaybackAttemptID: opted.ID, ExpiresAt: time.Now().Add(time.Hour), SessionID: opted.ID, NormalizedRequest: StartRequestV3{ClientFeatures: []string{FeatureMarkerThumbnailsV3}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAttempt(t.Context(), AttemptRecordV3{PlaybackAttemptID: legacy.ID, ExpiresAt: time.Now().Add(time.Hour), SessionID: legacy.ID}); err != nil {
		t.Fatal(err)
	}
	notifier := NewChapterThumbnailNotifier(sessions, hub, chapterThumbnailTestPresigner{}.DirectURL, 0, store)
	notifier.MarkerThumbnailReady(t.Context(), 100, models.MarkerThumbnail{Kind: "intro", MediaChapter: models.MediaChapter{StartSeconds: 6, EndSeconds: 9, ThumbnailPath: "chapter-images/100/marker-example/w300.webp"}})
	if len(a.messages) != 1 || len(b.messages) != 0 {
		t.Fatalf("opted=%d legacy=%d", len(a.messages), len(b.messages))
	}
	event := a.messages[0].(EventEnvelope)
	if event.Name != RealtimeEventMarkerThumbnailReady {
		t.Fatal(event.Name)
	}
	var payload MarkerThumbnailReadyPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Kind != "intro" || payload.ThumbnailCaptureSeconds != 6 {
		t.Fatalf("payload %+v", payload)
	}
}

func TestMarkerThumbnailNotifierDeliversAcrossReplicasOnce(t *testing.T) {
	bus := &markerUpdateTestBus{}
	sessions := NewSessionManager(0, 0)
	opted, _ := sessions.StartSession(1, "profile", 100, PlayDirect, false)
	legacy, _ := sessions.StartSession(2, "profile", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(opted.ID, true)
	_ = sessions.SetRealtimeConnection(legacy.ID, true)
	hub := NewRealtimeHub()
	a, b := &dispatchTestConn{}, &dispatchTestConn{}
	regA, regB := hub.Register(opted.ID, a), hub.Register(legacy.ID, b)
	defer hub.Unregister(regA)
	defer hub.Unregister(regB)
	store := NewMemoryPlanStoreV3()
	if err := store.SaveAttempt(t.Context(), AttemptRecordV3{PlaybackAttemptID: opted.ID, SessionID: opted.ID, ExpiresAt: time.Now().Add(time.Hour), NormalizedRequest: StartRequestV3{ClientFeatures: []string{FeatureMarkerThumbnailsV3}}}); err != nil {
		t.Fatal(err)
	}
	producer := NewChapterThumbnailNotifier(NewSessionManager(0, 0), NewRealtimeHub(), chapterThumbnailTestPresigner{}.DirectURL, 0, store)
	consumer := NewChapterThumbnailNotifier(sessions, hub, chapterThumbnailTestPresigner{}.DirectURL, 0, store)
	for _, n := range []*ChapterThumbnailNotifier{producer, consumer, producer} {
		if err := n.UseMarkerEventBus(t.Context(), bus.publish, bus.subscribe); err != nil {
			t.Fatal(err)
		}
	}
	if len(bus.handlers) != 2 {
		t.Fatal("duplicate subscription")
	}
	marker := models.MarkerThumbnail{Kind: "credits", MediaChapter: models.MediaChapter{StartSeconds: 30, EndSeconds: 33, ThumbnailPath: "chapter-images/100/marker-example/w300.webp"}}
	producer.MarkerThumbnailReady(t.Context(), 100, marker)
	if len(bus.events) != 1 || len(a.messages) != 1 || len(b.messages) != 0 {
		t.Fatalf("publishes=%d opted=%d legacy=%d", len(bus.events), len(a.messages), len(b.messages))
	}
	var payload MarkerThumbnailReadyPayload
	if err := json.Unmarshal(a.messages[0].(EventEnvelope).Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Kind != "credits" || payload.StartSeconds != 30 || payload.EndSeconds != 33 || payload.ThumbnailCaptureSeconds != 30 {
		t.Fatalf("payload %+v", payload)
	}
	// Retired object keys must not be converted into new URLs on a subscriber.
	consumer.presign = func(context.Context, string, time.Duration) (string, error) { return "", errors.New("retired") }
	producer.MarkerThumbnailReady(t.Context(), 100, marker)
	if len(a.messages) != 1 || len(bus.events) != 2 {
		t.Fatal("retired image delivered or subscriber rebroadcast")
	}
}
