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

func TestMarkerThumbnailNotifierDeliversAcrossReplicasOnce(t *testing.T) {
	bus := &markerUpdateTestBus{}
	sessions := NewSessionManager(0, 0)
	hub := NewRealtimeHub()
	opted := startPlaybackTestSession(t, sessions, 1, "profile", 100, PlayDirect)
	legacy := startPlaybackTestSession(t, sessions, 2, "profile", 100, PlayDirect)
	a, b := connectRealtimeTestConn(t, sessions, hub, opted.ID), connectRealtimeTestConn(t, sessions, hub, legacy.ID)
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
