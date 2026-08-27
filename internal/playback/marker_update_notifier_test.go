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

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

type observedMarkerSnapshotLoader struct {
	called chan struct{}
}

func (l observedMarkerSnapshotLoader) GetByID(context.Context, int) (*models.MediaFile, error) {
	close(l.called)
	return &models.MediaFile{ID: 100}, nil
}

func (l blockingMarkerSnapshotLoader) GetByID(context.Context, int) (*models.MediaFile, error) {
	close(l.entered)
	<-l.release
	return l.file, nil
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

func TestMarkerUpdateNotifierSendsSnapshotToOnlyRequestedSession(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	sessionA, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	sessionB, _ := sessions.StartSession(2, "profile-b", 100, PlayDirect, false)

	hub := NewRealtimeHub()
	connA := &dispatchTestConn{}
	connB := &dispatchTestConn{}
	regA := hub.Register(sessionA.ID, connA)
	regB := hub.Register(sessionB.ID, connB)
	defer hub.Unregister(regA)
	defer hub.Unregister(regB)

	introStart := 3.0
	introEnd := 64.0
	notifier := NewMarkerUpdateNotifier(sessions, hub)
	notifier.SendSessionSnapshot(context.Background(), sessionA.ID, &models.MediaFile{
		ID:         100,
		IntroStart: &introStart,
		IntroEnd:   &introEnd,
	})

	if len(connA.messages) != 1 {
		t.Fatalf("requested session messages = %d, want 1", len(connA.messages))
	}
	if len(connB.messages) != 0 {
		t.Fatalf("other session messages = %d, want 0", len(connB.messages))
	}
}

func TestMarkerUpdateNotifierOrdersPersistedSnapshotBeforeConcurrentUpdate(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)
	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	registration := hub.Register(session.ID, conn)
	defer hub.Unregister(registration)
	notifier := NewMarkerUpdateNotifier(sessions, hub)

	oldIntroStart, oldIntroEnd := 1.0, 50.0
	newIntroStart, newIntroEnd := 2.0, 60.0
	entered := make(chan struct{})
	release := make(chan struct{})
	snapshotDone := make(chan struct{})
	go func() {
		defer close(snapshotDone)
		_ = notifier.SendSessionSnapshotFromLoader(context.Background(), registration, 100, blockingMarkerSnapshotLoader{
			file: &models.MediaFile{
				ID:         100,
				IntroStart: &oldIntroStart,
				IntroEnd:   &oldIntroEnd,
			},
			entered: entered,
			release: release,
		})
	}()
	<-entered

	updateDone := make(chan struct{})
	go func() {
		defer close(updateDone)
		notifier.MarkersUpdated(context.Background(), &models.MediaFile{
			ID:         100,
			IntroStart: &newIntroStart,
			IntroEnd:   &newIntroEnd,
		})
	}()
	close(release)
	<-snapshotDone
	<-updateDone

	if len(conn.messages) != 2 {
		t.Fatalf("messages = %d, want snapshot then update", len(conn.messages))
	}
	markerStart := func(message any) float64 {
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
	if got := markerStart(conn.messages[0]); got != oldIntroStart {
		t.Fatalf("first marker start = %v, want old snapshot %v", got, oldIntroStart)
	}
	if got := markerStart(conn.messages[1]); got != newIntroStart {
		t.Fatalf("second marker start = %v, want new update %v", got, newIntroStart)
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
		snapshotDone <- notifier.SendSessionSnapshotFromLoader(
			context.Background(),
			oldRegistration,
			100,
			blockingMarkerSnapshotLoader{
				file: &models.MediaFile{
					ID:         100,
					IntroStart: &introStart,
					IntroEnd:   &introEnd,
				},
				entered: entered,
				release: release,
			},
		)
	}()
	<-entered

	if !hub.Unregister(oldRegistration) {
		t.Fatal("old connection did not unregister")
	}
	newConn := &dispatchTestConn{}
	newRegistration := hub.Register(session.ID, newConn)
	if newRegistration == nil {
		t.Fatal("replacement registration is nil")
	}
	defer hub.Unregister(newRegistration)
	close(release)
	if err := <-snapshotDone; err != nil {
		t.Fatalf("SendSessionSnapshotFromLoader: %v", err)
	}

	if len(oldConn.messages) != 0 {
		t.Fatalf("old connection messages = %d, want 0", len(oldConn.messages))
	}
	if len(newConn.messages) != 0 {
		t.Fatalf("replacement connection messages = %d, want 0", len(newConn.messages))
	}
}

func TestQueuedStaleMarkerSnapshotDoesNotLoadFile(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	hub := NewRealtimeHub()
	oldRegistration := hub.Register(session.ID, &dispatchTestConn{})
	notifier := NewMarkerUpdateNotifier(sessions, hub)

	lock := notifier.fileLock(100)
	lock.lock()
	defer lock.unlock()
	observed := make(chan struct{})
	loaderCalled := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- notifier.SendSessionSnapshotFromLoader(
			&observedDoneContext{Context: context.Background(), observed: observed},
			oldRegistration,
			100,
			observedMarkerSnapshotLoader{called: loaderCalled},
		)
	}()
	<-observed

	newRegistration := hub.Register(session.ID, &dispatchTestConn{})
	if newRegistration == nil {
		t.Fatal("replacement registration is nil")
	}
	defer hub.Unregister(newRegistration)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SendSessionSnapshotFromLoader: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale queued snapshot did not stop after replacement")
	}
	select {
	case <-loaderCalled:
		t.Fatal("stale queued snapshot called the loader")
	default:
	}
}

func TestQueuedMarkerSnapshotStopsWhenContextIsCanceled(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	hub := NewRealtimeHub()
	registration := hub.Register(session.ID, &dispatchTestConn{})
	defer hub.Unregister(registration)
	notifier := NewMarkerUpdateNotifier(sessions, hub)

	lock := notifier.fileLock(100)
	lock.lock()
	defer lock.unlock()
	observed := make(chan struct{})
	loaderCalled := make(chan struct{})
	baseCtx, cancel := context.WithCancel(context.Background())
	ctx := &observedDoneContext{Context: baseCtx, observed: observed}
	done := make(chan error, 1)
	go func() {
		done <- notifier.SendSessionSnapshotFromLoader(
			ctx,
			registration,
			100,
			observedMarkerSnapshotLoader{called: loaderCalled},
		)
	}()
	<-observed
	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("SendSessionSnapshotFromLoader error = %v, want context.Canceled", err)
	}
	select {
	case <-loaderCalled:
		t.Fatal("canceled queued snapshot called the loader")
	default:
	}
}
