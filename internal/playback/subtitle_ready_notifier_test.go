package playback

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type stubSubtitleInventoryResolver struct {
	file          *models.MediaFile
	additional    []SubtitleInventoryEntryV3
	err           error
	additionalErr error
	features      []string
	featuresErr   error
}

func (s *stubSubtitleInventoryResolver) MediaFile(context.Context, int) (*models.MediaFile, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.file, nil
}

func (s *stubSubtitleInventoryResolver) AdditionalSubtitles(context.Context, *models.MediaFile) ([]SubtitleInventoryEntryV3, error) {
	return s.additional, s.additionalErr
}

func (s *stubSubtitleInventoryResolver) SessionClientFeatures(context.Context, string) ([]string, error) {
	return s.features, s.featuresErr
}

// newSubtitleNotifierFixture registers one realtime-connected session on file
// 100 and returns its manager, hub, connection and session ID.
func newSubtitleNotifierFixture(t *testing.T) (*SessionManager, *RealtimeHub, *dispatchTestConn, string) {
	t.Helper()
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)
	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	t.Cleanup(func() { hub.Unregister(reg) })
	return sessions, hub, conn, session.ID
}

// subtitleNotifierReplica is one server replica's notifier with a connected
// session, for the cross-replica event bus tests.
func subtitleNotifierReplica(t *testing.T) (*SubtitleReadyNotifier, *dispatchTestConn) {
	t.Helper()
	sessions, hub, conn, _ := newSubtitleNotifierFixture(t)
	return NewSubtitleReadyNotifier(sessions, hub, nil), conn
}

func TestSubtitleTimingChangedDeliversAcrossReplicasOnce(t *testing.T) {
	bus := &subtitleTestBus{}
	local, localConn := subtitleNotifierReplica(t)
	remote, remoteConn := subtitleNotifierReplica(t)
	ctx := context.Background()
	for _, n := range []*SubtitleReadyNotifier{local, remote, local} {
		if err := n.UseEventBus(ctx, bus.publish, bus.subscribe); err != nil {
			t.Fatal(err)
		}
	}
	if len(bus.handlers) != 2 {
		t.Fatalf("subscriptions = %d, want 2", len(bus.handlers))
	}
	local.SubtitleTimingChanged(ctx, subtitles.SyncTarget{MediaFileID: 100, StoredID: 9})
	local.SubtitleTimingChanged(ctx, subtitles.SyncTarget{MediaFileID: 100, ExternalPath: "/media/film.en.srt"})
	if len(bus.events) != 2 {
		t.Fatalf("published events = %d, want 2 without rebroadcast", len(bus.events))
	}
	for name, conn := range map[string]*dispatchTestConn{"local": localConn, "remote": remoteConn} {
		if len(conn.messages) != 2 {
			t.Fatalf("%s messages = %d, want 2", name, len(conn.messages))
		}
		for i, want := range []SubtitleTimingChangedPayload{
			{SubtitleID: 9, SyncKey: "stored-9"},
			{SyncKey: subtitles.ExternalSyncKey("/media/film.en.srt")},
		} {
			event := conn.messages[i].(EventEnvelope)
			var payload SubtitleTimingChangedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil || event.Name != RealtimeEventSubtitleTimingChanged ||
				payload.SubtitleID != want.SubtitleID || payload.SyncKey != want.SyncKey || payload.FileID != 100 {
				t.Fatalf("%s event %d %+v payload %+v err %v", name, i, event, payload, err)
			}
		}
	}
}

// A server that predates sidecar sync publishes only the stored subtitle's
// ID; the event still names it by sync key.
func TestSubtitleTimingChangedAcceptsMessagesWithoutSyncKey(t *testing.T) {
	sessions, hub, conn, _ := newSubtitleNotifierFixture(t)
	bus := &subtitleTestBus{}
	if err := NewSubtitleReadyNotifier(sessions, hub, nil).UseEventBus(context.Background(), bus.publish, bus.subscribe); err != nil {
		t.Fatal(err)
	}
	for _, handler := range bus.handlers {
		handler(RealtimeEventSubtitleTimingChanged, `{"source_id":"older-server","file_id":100,"subtitle_id":9}`)
	}
	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(conn.messages))
	}
	var payload SubtitleTimingChangedPayload
	if err := json.Unmarshal(conn.messages[0].(EventEnvelope).Payload, &payload); err != nil || payload.SyncKey != "stored-9" || payload.SubtitleID != 9 {
		t.Fatalf("payload %+v err %v", payload, err)
	}
}

// subtitleTestBus is an in-memory event bus shared by notifier replicas.
type subtitleTestBus struct {
	handlers []func(RealtimeEventName, string)
	events   []RealtimeEventName
}

func (b *subtitleTestBus) publish(_ context.Context, event RealtimeEventName, payload string) error {
	b.events = append(b.events, event)
	for _, handler := range b.handlers {
		handler(event, payload)
	}
	return nil
}

func (b *subtitleTestBus) subscribe(_ context.Context, handler func(RealtimeEventName, string)) error {
	b.handlers = append(b.handlers, handler)
	return nil
}

func TestSubtitleSyncUpdatedReachesEveryReplicaOnce(t *testing.T) {
	bus := &subtitleTestBus{}
	local, localConn := subtitleNotifierReplica(t)
	remote, remoteConn := subtitleNotifierReplica(t)
	for _, n := range []*SubtitleReadyNotifier{local, remote} {
		if err := n.UseEventBus(context.Background(), bus.publish, bus.subscribe); err != nil {
			t.Fatal(err)
		}
	}
	progress := 0.45
	key := subtitles.ExternalSyncKey("/media/film.en.srt")
	local.SubtitleSyncUpdated(context.Background(), SubtitleSyncUpdate{FileID: 100, SyncKey: key, Timing: SubtitleSyncTiming{Scale: 1},
		Job: SubtitleSyncJob{ID: "7", Status: "running", Trigger: "manual", Phase: "analyzing", Progress: &progress, CreatedAt: "2026-10-04T00:00:00.000Z"}})
	if len(bus.events) != 1 || bus.events[0] != RealtimeEventSubtitleSyncUpdated {
		t.Fatalf("published %v", bus.events)
	}
	for name, conn := range map[string]*dispatchTestConn{"local": localConn, "remote": remoteConn} {
		if len(conn.messages) != 1 {
			t.Fatalf("%s messages = %d, want 1", name, len(conn.messages))
		}
		event := conn.messages[0].(EventEnvelope)
		var payload SubtitleSyncUpdatedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil || event.Name != RealtimeEventSubtitleSyncUpdated ||
			payload.SyncKey != key || payload.SubtitleID != 0 || payload.Job.Phase != "analyzing" || *payload.Job.Progress != progress {
			t.Fatalf("%s event %+v payload %+v err %v", name, event, payload, err)
		}
	}
}

// A subtitle downloaded or uploaded through one API server reaches viewers of
// the file connected to any server, exactly once each.
func TestSubtitleReadyDeliversAcrossReplicasOnce(t *testing.T) {
	replica := func() (*SubtitleReadyNotifier, *dispatchTestConn) {
		sessions := NewSessionManager(0, 0)
		session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
		_ = sessions.SetRealtimeConnection(session.ID, true)
		hub := NewRealtimeHub()
		conn := newWatchedConn()
		reg := hub.Register(session.ID, conn)
		t.Cleanup(func() { hub.Unregister(reg) })
		return NewSubtitleReadyNotifier(sessions, hub, nil), conn
	}

	bus := &subtitleTestBus{}
	local, localConn := replica()
	remote, remoteConn := replica()
	observed := &observedSubtitleInventoryResolver{contexts: make(chan context.Context, 4)}
	remote.inventory = observed
	ctx := context.Background()
	for _, n := range []*SubtitleReadyNotifier{local, remote} {
		if err := n.UseEventBus(ctx, bus.publish, bus.subscribe); err != nil {
			t.Fatal(err)
		}
	}
	local.SubtitleReady(ctx, 100, 42, "fr", "Movie.2024.FR")
	if len(bus.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(bus.events))
	}
	var dispatchCtx context.Context
	select {
	case dispatchCtx = <-observed.contexts:
	case <-time.After(time.Second):
		t.Fatal("remote dispatch did not start")
	}
	// The remote worker cancels its context when dispatch returns, after
	// all sends. Observe that cleanup before checking the final count.
	select {
	case <-dispatchCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("remote dispatch did not finish")
	}
	for name, conn := range map[string]*dispatchTestConn{"local": localConn, "remote": remoteConn} {
		messages := conn.sent()
		if len(messages) != 1 {
			t.Fatalf("%s messages = %d, want 1", name, len(messages))
		}
		event := messages[0].(EventEnvelope)
		var payload SubtitleReadyPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil || event.Name != RealtimeEventSubtitleReady ||
			payload.FileID != 100 || payload.SubtitleID != 42 || payload.Language != "fr" {
			t.Fatalf("%s event %+v payload %+v err %v", name, event, payload, err)
		}
	}
}

// observedSubtitleInventoryResolver exposes the real dispatch lifetime through
// the context passed to the inventory dependency.
type observedSubtitleInventoryResolver struct {
	stubSubtitleInventoryResolver
	contexts chan context.Context
}

func (r *observedSubtitleInventoryResolver) MediaFile(ctx context.Context, fileID int) (*models.MediaFile, error) {
	r.contexts <- ctx
	return r.stubSubtitleInventoryResolver.MediaFile(ctx, fileID)
}

// deadlineInventoryResolver spends the caller's deadline while it resolves a
// session's track, as a slow database does under many watching sessions.
type deadlineInventoryResolver struct {
	stubSubtitleInventoryResolver
	spend func()
}

func (r *deadlineInventoryResolver) MediaFile(ctx context.Context, fileID int) (*models.MediaFile, error) {
	r.spend()
	return r.stubSubtitleInventoryResolver.MediaFile(ctx, fileID)
}

// newWatchedConn returns a connection waitForMessages can wait on.
func newWatchedConn() *dispatchTestConn {
	return &dispatchTestConn{written: make(chan struct{}, 16)}
}

// blockingInventoryResolver holds a track lookup until its context ends, as a
// stalled database does, and reports whether that context had a deadline.
type blockingInventoryResolver struct {
	stubSubtitleInventoryResolver
	hadDeadline chan bool
}

func (r *blockingInventoryResolver) MediaFile(ctx context.Context, _ int) (*models.MediaFile, error) {
	_, ok := ctx.Deadline()
	r.hadDeadline <- ok
	<-ctx.Done()
	return nil, ctx.Err()
}
