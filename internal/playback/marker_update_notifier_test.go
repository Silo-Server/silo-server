package playback

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type blockingMarkerSnapshotLoader struct {
	file    *models.MediaFile
	entered chan struct{}
	release chan struct{}
}

func (l blockingMarkerSnapshotLoader) GetByID(context.Context, int) (*models.MediaFile, error) {
	close(l.entered)
	<-l.release
	return l.file, nil
}

type observedMarkerSnapshotLoader struct {
	called chan struct{}
}

func (l observedMarkerSnapshotLoader) GetByID(context.Context, int) (*models.MediaFile, error) {
	close(l.called)
	return &models.MediaFile{ID: 100}, nil
}

type blockingMarkerWriteConn struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingMarkerWriteConn) WriteJSON(any) error {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return nil
}

func waitForDispatchMessages(t *testing.T, conn *dispatchTestConn, want int) []any {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		messages := conn.sent()
		if len(messages) >= want {
			return messages
		}
		time.Sleep(time.Millisecond)
	}
	messages := conn.sent()
	t.Fatalf("messages = %d, want at least %d", len(messages), want)
	return nil
}

func markerStartFromMessage(t *testing.T, message any) float64 {
	t.Helper()
	event, ok := message.(EventEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want EventEnvelope", message)
	}
	var payload MarkersUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.Intro == nil {
		t.Fatal("intro payload is nil")
	}
	return payload.Intro.Start
}

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

	introStart, introEnd := 12.0, 75.0
	creditsStart, creditsEnd := 3600.0, 3660.0
	notifier := NewMarkerUpdateNotifier(sessions, hub)
	notifier.MarkersUpdated(context.Background(), &models.MediaFile{
		ID: 100, IntroStart: &introStart, IntroEnd: &introEnd,
		CreditsStart: &creditsStart, CreditsEnd: &creditsEnd,
	})

	messagesA := waitForDispatchMessages(t, connA, 1)
	waitForDispatchMessages(t, connB, 1)
	waitForDispatchMessages(t, connRequested, 1)
	if got := len(connOther.sent()); got != 0 {
		t.Fatalf("non-matching session messages = %d, want 0", got)
	}

	event, ok := messagesA[0].(EventEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want EventEnvelope", messagesA[0])
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
	messages := waitForDispatchMessages(t, conn, 1)
	event := messages[0].(EventEnvelope)
	var payload MarkersUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.Intro != nil || payload.Credits != nil || payload.Recap != nil || payload.Preview != nil {
		t.Fatalf("payload markers = %#v, want all nil", payload)
	}
}

func TestMarkerUpdateNotifierDoesNotBlockCallerOnWebSocketWrite(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)
	hub := NewRealtimeHub()
	conn := &blockingMarkerWriteConn{entered: make(chan struct{}), release: make(chan struct{})}
	reg := hub.Register(session.ID, conn)
	defer hub.Unregister(reg)
	notifier := NewMarkerUpdateNotifier(sessions, hub)

	returned := make(chan struct{})
	go func() {
		notifier.MarkersUpdated(context.Background(), &models.MediaFile{ID: 100})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("MarkersUpdated blocked on websocket delivery")
	}
	select {
	case <-conn.entered:
	case <-time.After(time.Second):
		t.Fatal("background websocket delivery did not start")
	}
	close(conn.release)
}

func TestMarkerUpdateNotifierNeverDeliversStaleSnapshotAfterNewerUpdate(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)
	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	registration := hub.Register(session.ID, conn)
	defer hub.Unregister(registration)
	notifier := NewMarkerUpdateNotifier(sessions, hub)

	oldStart, oldEnd := 1.0, 50.0
	newStart, newEnd := 2.0, 60.0
	entered := make(chan struct{})
	release := make(chan struct{})
	snapshotDone := make(chan struct {
		sent bool
		err  error
	}, 1)
	go func() {
		sent, err := notifier.SendSessionSnapshotFromLoader(context.Background(), registration, 100, blockingMarkerSnapshotLoader{
			file:    &models.MediaFile{ID: 100, IntroStart: &oldStart, IntroEnd: &oldEnd},
			entered: entered, release: release,
		})
		snapshotDone <- struct {
			sent bool
			err  error
		}{sent: sent, err: err}
	}()
	<-entered

	updateReturned := make(chan struct{})
	go func() {
		notifier.MarkersUpdated(context.Background(), &models.MediaFile{ID: 100, IntroStart: &newStart, IntroEnd: &newEnd})
		close(updateReturned)
	}()
	close(release)
	result := <-snapshotDone
	if result.err != nil {
		t.Fatalf("SendSessionSnapshotFromLoader: %v", result.err)
	}
	<-updateReturned
	messages := waitForDispatchMessages(t, conn, 1)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current := conn.sent()
		if len(current) > 0 {
			messages = current
			if markerStartFromMessage(t, current[len(current)-1]) == newStart {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if len(messages) > 2 {
		t.Fatalf("messages = %d, want at most snapshot and update", len(messages))
	}
	if got := markerStartFromMessage(t, messages[len(messages)-1]); got != newStart {
		t.Fatalf("last marker start = %v, want newest update %v", got, newStart)
	}
	if len(messages) == 2 && markerStartFromMessage(t, messages[0]) != oldStart {
		t.Fatalf("first marker was not the old snapshot: %#v", messages[0])
	}
}

func TestMarkerSnapshotFromReplacedConnectionDoesNotReachReplacement(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	hub := NewRealtimeHub()
	oldConn := &dispatchTestConn{}
	oldRegistration := hub.Register(session.ID, oldConn)
	notifier := NewMarkerUpdateNotifier(sessions, hub)

	introStart, introEnd := 1.0, 50.0
	entered := make(chan struct{})
	release := make(chan struct{})
	snapshotDone := make(chan error, 1)
	go func() {
		_, err := notifier.SendSessionSnapshotFromLoader(context.Background(), oldRegistration, 100, blockingMarkerSnapshotLoader{
			file:    &models.MediaFile{ID: 100, IntroStart: &introStart, IntroEnd: &introEnd},
			entered: entered, release: release,
		})
		snapshotDone <- err
	}()
	<-entered

	if !hub.Unregister(oldRegistration) {
		t.Fatal("old connection did not unregister")
	}
	newConn := &dispatchTestConn{}
	newRegistration := hub.Register(session.ID, newConn)
	defer hub.Unregister(newRegistration)
	close(release)
	if err := <-snapshotDone; err != nil {
		t.Fatalf("SendSessionSnapshotFromLoader: %v", err)
	}
	if len(oldConn.sent()) != 0 || len(newConn.sent()) != 0 {
		t.Fatal("stale snapshot reached an old or replacement connection")
	}
}

func TestMarkerSnapshotSkipsEmptyRows(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	registration := hub.Register(session.ID, conn)
	defer hub.Unregister(registration)
	notifier := NewMarkerUpdateNotifier(sessions, hub)

	sent, err := notifier.SendSessionSnapshotFromLoader(context.Background(), registration, 100, observedMarkerSnapshotLoader{called: make(chan struct{})})
	if err != nil {
		t.Fatalf("SendSessionSnapshotFromLoader: %v", err)
	}
	if sent || len(conn.sent()) != 0 {
		t.Fatal("empty persisted markers emitted a clearing snapshot")
	}
}

func TestMarkerSnapshotStopsWhenContextIsCanceled(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	hub := NewRealtimeHub()
	registration := hub.Register(session.ID, &dispatchTestConn{})
	defer hub.Unregister(registration)
	notifier := NewMarkerUpdateNotifier(sessions, hub)
	loaderCalled := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := notifier.SendSessionSnapshotFromLoader(ctx, registration, 100, observedMarkerSnapshotLoader{called: loaderCalled})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendSessionSnapshotFromLoader error = %v, want context.Canceled", err)
	}
	select {
	case <-loaderCalled:
		t.Fatal("canceled snapshot called the loader")
	default:
	}
}
