package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

func testSessionRealtimeClient(handler *PlaybackHandler, sessionID string) *sessionRealtimeClient {
	return &sessionRealtimeClient{
		handler:       handler,
		connectionCtx: context.Background(),
		sessionID:     sessionID,
	}
}

type recordingMarkerSnapshotNotifier struct {
	calls atomic.Int32
}

func (n *recordingMarkerSnapshotNotifier) MarkersUpdated(context.Context, *models.MediaFile) {}

func (n *recordingMarkerSnapshotNotifier) SendSessionSnapshotFromLoader(
	context.Context,
	*playback.RealtimeRegistration,
	int,
	playback.MarkerSnapshotFileLoader,
) (bool, error) {
	n.calls.Add(1)
	return true, nil
}

func waitForMarkerSnapshotCalls(t *testing.T, notifier *recordingMarkerSnapshotNotifier, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if notifier.calls.Load() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("marker snapshot calls = %d, want %d", notifier.calls.Load(), want)
}

func TestHandleSessionWebSocket_RequiresHelloBeforeRealtimeReady(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, mapPlaybackFileResolver{files: map[int]*models.MediaFile{
		100: {ID: 100},
	}})
	handler.RealtimeHub = playback.NewRealtimeHub()
	notifier := &recordingMarkerSnapshotNotifier{}
	handler.MarkerUpdateNotifier = notifier
	telemetryConfig := streamtelemetry.DefaultConfig("websocket-test")
	telemetryConfig.Enabled = true
	handler.StreamTelemetry = streamtelemetry.NewRegistry(telemetryConfig, streamtelemetry.NewLocalStore(), nil)
	seedRoute := streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyNative, Method: http.MethodGet,
		Pattern: "/stream/{session_id}", Class: streamtelemetry.ClassPlayback,
		Role: streamtelemetry.RoleViewerEgress, CapRelevant: true, Enrolled: true}
	handler.StreamTelemetry.Observe(seedRoute)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		attachPlaybackSession(r.Context(), session, nil)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream/"+session.ID, nil))

	router := chi.NewRouter()
	router.Get("/playback/ws/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		ctx := apimw.SetClaims(r.Context(), &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess})
		handler.HandleSessionWebSocket(w, r.WithContext(ctx))
	})

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/playback/ws/" + session.ID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial websocket: %v", err)
	}
	defer conn.Close()

	got, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession before hello: %v", err)
	}
	if got.HasRealtimeConnection {
		t.Fatal("session should not be realtime-ready before hello")
	}
	if got.HasWebSocket {
		t.Fatal("session should not report playback control before hello")
	}

	if err := conn.WriteJSON(playback.HelloEnvelope{
		Type:      playback.RealtimeMessageTypeHello,
		SessionID: session.ID,
		Client: playback.HelloClientInfo{
			Name:    "ios",
			Version: "1.0.0",
		},
		Capabilities: playback.HelloCapabilities{
			Commands: []playback.CommandName{
				playback.CommandPause,
				playback.CommandUnpause,
				playback.CommandStop,
				playback.CommandTerminate,
			},
		},
	}); err != nil {
		t.Fatalf("WriteJSON hello: %v", err)
	}

	waitForPlaybackRealtimeState(t, sessionMgr, session.ID, true)
	waitForTelemetryRealtimeState(t, handler.StreamTelemetry, session.ID, true)

	if err := conn.Close(); err != nil {
		t.Fatalf("Close websocket: %v", err)
	}

	waitForPlaybackRealtimeState(t, sessionMgr, session.ID, false)
	waitForTelemetryRealtimeState(t, handler.StreamTelemetry, session.ID, false)
}

func TestSendCurrentMarkerSnapshotLoadsPersistedSharedMarkers(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	introStart, introEnd := 4.0, 58.0
	creditsStart, creditsEnd := 3500.0, 3600.0
	resolver := mapPlaybackFileResolver{files: map[int]*models.MediaFile{
		100: {
			ID:           100,
			IntroStart:   &introStart,
			IntroEnd:     &introEnd,
			CreditsStart: &creditsStart,
			CreditsEnd:   &creditsEnd,
		},
	}}
	handler := NewPlaybackHandler(sessionMgr, resolver)
	handler.RealtimeHub = playback.NewRealtimeHub()
	handler.MarkerUpdateNotifier = playback.NewMarkerUpdateNotifier(sessionMgr, handler.RealtimeHub)
	conn := &adminPlaybackControlTestConn{}
	registration := handler.RealtimeHub.Register(session.ID, conn)
	if registration == nil {
		t.Fatal("expected realtime registration")
	}
	defer handler.RealtimeHub.Unregister(registration)

	handler.sendCurrentMarkerSnapshot(context.Background(), registration, session.ID)

	if len(conn.messages) != 1 {
		t.Fatalf("snapshot messages = %d, want 1", len(conn.messages))
	}
	event, ok := conn.messages[0].(playback.EventEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want playback.EventEnvelope", conn.messages[0])
	}
	var payload playback.MarkersUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.Intro == nil || payload.Intro.Start != introStart || payload.Intro.End != introEnd {
		t.Fatalf("intro snapshot = %#v, want persisted intro", payload.Intro)
	}
	if payload.Credits == nil || payload.Credits.Start != creditsStart || payload.Credits.End != creditsEnd {
		t.Fatalf("credits snapshot = %#v, want persisted credits", payload.Credits)
	}
}

func TestSendCurrentMarkerSnapshotSkipsEmptyPersistedMarkers(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	handler := NewPlaybackHandler(sessionMgr, mapPlaybackFileResolver{files: map[int]*models.MediaFile{
		100: {ID: 100},
	}})
	handler.RealtimeHub = playback.NewRealtimeHub()
	handler.MarkerUpdateNotifier = playback.NewMarkerUpdateNotifier(sessionMgr, handler.RealtimeHub)
	conn := &adminPlaybackControlTestConn{}
	registration := handler.RealtimeHub.Register(session.ID, conn)
	if registration == nil {
		t.Fatal("expected realtime registration")
	}
	defer handler.RealtimeHub.Unregister(registration)

	handler.sendCurrentMarkerSnapshot(context.Background(), registration, session.ID)

	if len(conn.messages) != 0 {
		t.Fatalf("snapshot messages = %d, want none for an empty marker row", len(conn.messages))
	}
}

func TestSessionRealtimeClientRequestsMarkerSnapshotOncePerConnection(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	handler := NewPlaybackHandler(sessionMgr, mapPlaybackFileResolver{files: map[int]*models.MediaFile{
		100: {ID: 100},
	}})
	handler.RealtimeHub = playback.NewRealtimeHub()
	notifier := &recordingMarkerSnapshotNotifier{}
	handler.MarkerUpdateNotifier = notifier
	hello, err := json.Marshal(playback.HelloEnvelope{
		Type:      playback.RealtimeMessageTypeHello,
		SessionID: session.ID,
		Client: playback.HelloClientInfo{
			Name:    "ios",
			Version: "1.0.0",
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal(hello): %v", err)
	}

	registration := handler.registerSessionRealtimeConnection(session.ID, &adminPlaybackControlTestConn{})
	client := testSessionRealtimeClient(handler, session.ID)
	client.registration = registration
	if err := client.handleMessage(hello); err != nil {
		t.Fatalf("first hello: %v", err)
	}
	if !client.helloReceived {
		t.Fatal("initial hello was not recorded")
	}
	waitForMarkerSnapshotCalls(t, notifier, 1)
	if err := client.handleMessage(hello); err != nil {
		t.Fatalf("second hello: %v", err)
	}
	if got := notifier.calls.Load(); got != 1 {
		t.Fatalf("snapshot calls after repeated hello = %d, want 1", got)
	}

	replacementRegistration := handler.registerSessionRealtimeConnection(session.ID, &adminPlaybackControlTestConn{})
	replacement := testSessionRealtimeClient(handler, session.ID)
	replacement.registration = replacementRegistration
	if err := replacement.handleMessage(hello); err != nil {
		t.Fatalf("replacement connection hello: %v", err)
	}
	if !replacement.helloReceived {
		t.Fatal("replacement connection did not record its own hello")
	}
	waitForMarkerSnapshotCalls(t, notifier, 2)
}

func TestReplacementConnectionOwnsRealtimeReadiness(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	handler := NewPlaybackHandler(sessionMgr)
	handler.RealtimeHub = playback.NewRealtimeHub()
	hello, err := json.Marshal(playback.HelloEnvelope{
		Type: playback.RealtimeMessageTypeHello, SessionID: session.ID,
		Client: playback.HelloClientInfo{Name: "ios", Version: "1.0.0"},
	})
	if err != nil {
		t.Fatalf("json.Marshal(hello): %v", err)
	}

	oldRegistration := handler.registerSessionRealtimeConnection(session.ID, &adminPlaybackControlTestConn{})
	oldClient := testSessionRealtimeClient(handler, session.ID)
	oldClient.registration = oldRegistration
	if err := oldClient.handleMessage(hello); err != nil {
		t.Fatalf("old hello: %v", err)
	}
	waitForPlaybackRealtimeState(t, sessionMgr, session.ID, true)

	newRegistration := handler.registerSessionRealtimeConnection(session.ID, &adminPlaybackControlTestConn{})
	got, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession before replacement hello: %v", err)
	}
	if got.HasRealtimeConnection {
		t.Fatal("replacement socket was ready before its own hello")
	}

	newClient := testSessionRealtimeClient(handler, session.ID)
	newClient.registration = newRegistration
	if err := newClient.handleMessage(hello); err != nil {
		t.Fatalf("replacement hello: %v", err)
	}
	handler.unregisterSessionRealtimeConnection(session.ID, oldRegistration)
	waitForPlaybackRealtimeState(t, sessionMgr, session.ID, true)
}

func waitForTelemetryRealtimeState(t *testing.T, registry *streamtelemetry.Registry, sessionID string, want bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, session := range registry.Snapshot().Sessions {
			if session.SessionID == sessionID && session.RealtimeConnectionAlive == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("telemetry realtime state for %s did not become %t", sessionID, want)
}

func waitForPlaybackRealtimeState(t *testing.T, sessionMgr *playback.SessionManager, sessionID string, want bool) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session, err := sessionMgr.GetSession(sessionID)
		if err == nil && session != nil && session.HasRealtimeConnection == want && session.HasWebSocket == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	session, err := sessionMgr.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession after wait: %v", err)
	}
	t.Fatalf(
		"session realtime state = %v/%v, want %v/%v",
		session.HasRealtimeConnection,
		session.HasWebSocket,
		want,
		want,
	)
}
