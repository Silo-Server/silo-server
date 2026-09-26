package playback

import (
	"context"
	"testing"
	"time"
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
		"chapter-images/100/7/original.webp",
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
}
