package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	controlInstallation      = "0f0e5c2e-2c6b-4b39-9c8b-3a5f9a2b7d11"
	controlOtherInstallation = "6c0a7b1e-8d2f-4a35-b0c1-2e4d5f6a7b8c"
)

// controlSocketFixture stands up a playback handler with a hub, tracker and
// dispatcher and one owner-started session.
type controlSocketFixture struct {
	handler *PlaybackControlSocketV2
	pb      *PlaybackHandler
	manager *playback.SessionManager
	hub     *playback.RealtimeHub
	session *playback.Session
	server  *httptest.Server
	invalid *atomic.Bool
}

func newControlSocketFixture(t *testing.T) *controlSocketFixture {
	t.Helper()
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(7, "profile-7", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	hub := playback.NewRealtimeHub()
	tracker := playback.NewCommandTracker()
	t.Cleanup(tracker.Close)
	pb := NewPlaybackHandler(manager)
	pb.RealtimeHub = hub
	pb.CommandTracker = tracker
	pb.CommandDispatcher = playback.NewCommandDispatcher(manager, hub, tracker)
	pb.InstallationID = controlInstallation

	f := &controlSocketFixture{pb: pb, manager: manager, hub: hub, session: session, invalid: new(atomic.Bool)}
	h := &PlaybackControlSocketV2{Playback: pb, Tickets: NewPlaybackControlTicketStore(nil), checkInterval: 10 * time.Millisecond, lanes: map[string]*playbackControlLane{}}
	h.Validate = func(ctx context.Context, proof evt.SocketIdentity) (context.Context, *auth.Claims, error) {
		if f.invalid.Load() {
			return ctx, nil, evt.ErrSocketTicket
		}
		return access.SetScope(ctx, access.Scope{}), &auth.Claims{UserID: proof.UserID, Role: proof.Role, SessionID: proof.SessionID}, nil
	}
	f.handler = h
	router := chi.NewRouter()
	router.Get("/api/v2/playback/sessions/{session_id}/control/ws", h.ServeHTTP)
	f.server = httptest.NewServer(router)
	t.Cleanup(f.server.Close)
	h.PublicOrigin = f.server.URL
	return f
}

func (f *controlSocketFixture) identity(userID int, profileID string) evt.SocketIdentity {
	return evt.SocketIdentity{UserID: userID, Role: "user", SessionID: "login-" + profileID, ProfileID: profileID, AccessExpiresAt: time.Now().Add(time.Minute)}
}

func (f *controlSocketFixture) mint(t *testing.T, installation string) string {
	t.Helper()
	ticket, _, err := f.handler.Mint(t.Context(), f.identity(7, "profile-7"), f.session.ID, installation)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func (f *controlSocketFixture) dial(t *testing.T, ticket string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	endpoint := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/api/v2/playback/sessions/" + f.session.ID + "/control/ws"
	dialer := websocket.Dialer{Subprotocols: []string{PlaybackControlSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	if headers == nil {
		headers = http.Header{"Origin": []string{f.server.URL}}
	}
	conn, resp, err := dialer.DialContext(t.Context(), endpoint, headers)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	if resp != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	return conn, resp, err
}

func (f *controlSocketFixture) hello(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.WriteJSON(playback.HelloEnvelope{Type: playback.RealtimeMessageTypeHello, SessionID: f.session.ID, Client: playback.HelloClientInfo{Name: "silo-test", Version: "1"}}); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		s, err := f.manager.GetSession(f.session.ID)
		return err == nil && s.HasRealtimeConnection
	}, "session did not become control-ready after hello")
}

func waitForCondition(t *testing.T, cond func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(message)
}

func TestControlSocketMintAdmitsOnlyTheOwner(t *testing.T) {
	f := newControlSocketFixture(t)
	ctx := t.Context()
	if _, _, err := f.handler.Mint(ctx, f.identity(7, "profile-7"), f.session.ID, controlInstallation); err != nil {
		t.Fatal(err)
	}
	// A bridge-started client presents no installation and is admitted on
	// account and profile alone.
	if _, _, err := f.handler.Mint(ctx, f.identity(7, "profile-7"), f.session.ID, ""); err != nil {
		t.Fatalf("bridge mint: %v", err)
	}
	for name, tc := range map[string]struct {
		identity     evt.SocketIdentity
		session      string
		installation string
		want         error
	}{
		"other account":      {f.identity(8, "profile-7"), f.session.ID, controlInstallation, ErrPlaybackControlSocketNotOwner},
		"other profile":      {f.identity(7, "profile-8"), f.session.ID, controlInstallation, ErrPlaybackControlSocketNotOwner},
		"unknown session":    {f.identity(7, "profile-7"), "missing", controlInstallation, playback.ErrSessionNotFound},
		"wrong installation": {f.identity(7, "profile-7"), f.session.ID, controlOtherInstallation, ErrPlaybackControlSocketInstallation},
	} {
		if _, _, err := f.handler.Mint(ctx, tc.identity, tc.session, tc.installation); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	// A stopped session is unknown to the handshake.
	if err := f.manager.StopSession(f.session.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.handler.Mint(ctx, f.identity(7, "profile-7"), f.session.ID, controlInstallation); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("stopped session err = %v", err)
	}
	// Login authority is validated before any session lookup.
	f.invalid.Store(true)
	if _, _, err := f.handler.Mint(ctx, f.identity(7, "profile-7"), f.session.ID, ""); !errors.Is(err, evt.ErrSocketTicket) {
		t.Fatalf("invalid login err = %v", err)
	}
}

func TestControlSocketHandshakeProofOriginReplayAndStaleBinding(t *testing.T) {
	f := newControlSocketFixture(t)
	ticket := f.mint(t, controlInstallation)

	// Foreign origin is refused before the credential is consumed.
	conn, resp, err := f.dial(t, ticket, http.Header{"Origin": []string{"https://other.example.test"}}) //nolint:bodyclose // dial registers t.Cleanup to close the response body
	if err == nil || conn != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatal("foreign origin accepted")
	}
	// Proof in the URL is refused before the credential is consumed.
	bad := websocket.Dialer{Subprotocols: []string{PlaybackControlSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	_, resp, err = bad.DialContext(t.Context(), "ws"+strings.TrimPrefix(f.server.URL, "http")+"/api/v2/playback/sessions/"+f.session.ID+"/control/ws?token=x", nil)
	if err == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatal("token in URL accepted")
	}
	_ = resp.Body.Close()

	// The installation changed between mint and upgrade: stale, and the
	// credential is spent.
	f.pb.InstallationID = controlOtherInstallation
	conn, resp, err = f.dial(t, ticket, nil) //nolint:bodyclose // dial registers t.Cleanup to close the response body
	if err == nil || conn != nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale installation accepted: %v %v", err, resp)
	}
	f.pb.InstallationID = controlInstallation
	if _, resp, err = f.dial(t, ticket, nil); err == nil || resp.StatusCode != http.StatusUnauthorized { //nolint:bodyclose // dial registers t.Cleanup to close the response body
		t.Fatal("consumed credential replayed")
	}

	// A fresh credential connects, selects only the protocol, and the hello
	// makes the session control-ready.
	ticket = f.mint(t, controlInstallation)
	conn, _, err = f.dial(t, ticket, nil) //nolint:bodyclose // dial registers t.Cleanup to close the response body
	if err != nil {
		t.Fatal(err)
	}
	if conn.Subprotocol() != PlaybackControlSocketProtocol {
		t.Fatalf("selected %q", conn.Subprotocol())
	}
	f.hello(t, conn)
	if _, resp, err := f.dial(t, ticket, nil); err == nil || resp.StatusCode != http.StatusUnauthorized { //nolint:bodyclose // dial registers t.Cleanup to close the response body
		t.Fatal("credential reused for reconnect")
	}
}

func TestControlSocketNonOwnerAndForeignSessionRefused(t *testing.T) {
	f := newControlSocketFixture(t)
	// A credential minted by the owner cannot be presented for another session.
	other, err := f.manager.StartSession(9, "profile-9", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	ticket := f.mint(t, controlInstallation)
	endpoint := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/api/v2/playback/sessions/" + other.ID + "/control/ws"
	dialer := websocket.Dialer{Subprotocols: []string{PlaybackControlSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	conn, resp, err := dialer.DialContext(t.Context(), endpoint, http.Header{"Origin": []string{f.server.URL}})
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatal("credential accepted for a foreign session")
	}
	_ = resp.Body.Close()

	// Ownership changes between mint and upgrade are refused at upgrade too:
	// simulate by a credential whose identity no longer matches the session.
	stored := PlaybackControlTicket{Identity: f.identity(8, "profile-7"), Binding: PlaybackControlBinding{PlaybackSessionID: f.session.ID, InstallationID: controlInstallation}}
	stored.Identity.AccessFingerprint = "scope"
	forged, err := f.handler.Tickets.Mint(t.Context(), stored)
	if err != nil {
		t.Fatal(err)
	}
	conn, resp, err = f.dial(t, forged, nil) //nolint:bodyclose // dial registers t.Cleanup to close the response body
	if err == nil || conn != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatal("non-owner credential upgraded")
	}
}

func TestControlSocketReconnectResumesOnlySameOwnerAndInstallation(t *testing.T) {
	f := newControlSocketFixture(t)
	first, _, err := f.dial(t, f.mint(t, controlInstallation), nil) //nolint:bodyclose // dial registers t.Cleanup to close the response body
	if err != nil {
		t.Fatal(err)
	}
	f.hello(t, first)

	// While the lane is held by an installation-bound client, a bridge client
	// (no installation) cannot even mint.
	if _, _, err := f.handler.Mint(t.Context(), f.identity(7, "profile-7"), f.session.ID, ""); !errors.Is(err, ErrPlaybackControlSocketLaneHeld) {
		t.Fatalf("other installation err = %v", err)
	}

	// The same owner and installation reconnects and takes over the lane; the
	// old connection's frames are no longer routed and it is closed.
	second, _, err := f.dial(t, f.mint(t, controlInstallation), nil) //nolint:bodyclose // dial registers t.Cleanup to close the response body
	if err != nil {
		t.Fatal(err)
	}
	f.hello(t, second)
	command, err := playback.NewCommandEnvelope(f.session.ID, "11111111-1111-4111-8111-111111111111", playback.CommandPause, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.hub.Send(f.session.ID, command); err != nil {
		t.Fatal(err)
	}
	if err := second.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, data, err := second.ReadMessage()
	if err != nil || !strings.Contains(string(data), `"command_id":"11111111-1111-4111-8111-111111111111"`) {
		t.Fatalf("second connection did not receive the command: %s %v", data, err)
	}
	if err := first.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.ReadMessage(); err == nil {
		t.Fatal("first connection still receives frames after takeover")
	}
}

func TestControlSocketAckAndResultRouteToRegistrationOwner(t *testing.T) {
	f := newControlSocketFixture(t)
	conn, _, err := f.dial(t, f.mint(t, controlInstallation), nil) //nolint:bodyclose // dial registers t.Cleanup to close the response body
	if err != nil {
		t.Fatal(err)
	}
	f.hello(t, conn)

	// A tracked stop is completed by the owner's result: the tracker clears
	// and the session is stopped through the registration owner's path.
	const commandID = "22222222-2222-4222-8222-222222222222"
	command, err := playback.NewCommandEnvelope(f.session.ID, commandID, playback.CommandStop, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.pb.rememberRealtimeCommand(commandID, f.session.ID, playback.CommandStop)
	result := f.pb.CommandDispatcher.DispatchToSession(command, time.Second, func() { t.Error("fallback fired for an acknowledged command") })
	if result.DispatchErr != nil || !result.Delivered || !result.Tracked {
		t.Fatalf("dispatch = %+v", result)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var received playback.CommandEnvelope
	if err := json.Unmarshal(data, &received); err != nil || received.CommandID != commandID {
		t.Fatalf("received %s %v", data, err)
	}
	if err := conn.WriteJSON(playback.AckEnvelope{Type: playback.RealtimeMessageTypeAck, CommandID: commandID, SessionID: f.session.ID, Status: playback.RealtimeAckStatusAccepted}); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		state, ok := f.pb.CommandTracker.Status(commandID)
		return ok && state == playback.CommandStateAccepted
	}, "ack was not routed to the tracker")
	// A result naming another session is refused without touching the record.
	if err := conn.WriteJSON(playback.ResultEnvelope{Type: playback.RealtimeMessageTypeResult, CommandID: commandID, SessionID: "other", Status: playback.RealtimeResultStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(playback.ResultEnvelope{Type: playback.RealtimeMessageTypeResult, CommandID: commandID, SessionID: f.session.ID, Status: playback.RealtimeResultStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		_, err := f.manager.GetSession(f.session.ID)
		return errors.Is(err, playback.ErrSessionNotFound)
	}, "completed stop result did not end the session")
	if _, ok := f.pb.getRealtimeCommand(commandID); ok {
		t.Fatal("command record survived its result")
	}
}

func TestControlSocketClosesOnAuthorityOrSessionLoss(t *testing.T) {
	for _, loss := range []string{"login", "session"} {
		t.Run(loss, func(t *testing.T) {
			f := newControlSocketFixture(t)
			conn, _, err := f.dial(t, f.mint(t, controlInstallation), nil) //nolint:bodyclose // dial registers t.Cleanup to close the response body
			if err != nil {
				t.Fatal(err)
			}
			f.hello(t, conn)
			if loss == "login" {
				f.invalid.Store(true)
			} else if err := f.manager.StopSession(f.session.ID); err != nil {
				t.Fatal(err)
			}
			if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := conn.ReadMessage(); err == nil {
				t.Fatal("connection survived authority loss")
			}
			if loss == "login" {
				waitForCondition(t, func() bool {
					s, err := f.manager.GetSession(f.session.ID)
					return err == nil && !s.HasRealtimeConnection
				}, "session stayed control-ready after the socket closed")
			}
		})
	}
}

func TestControlSocketTicketStoreBoundsAndValidation(t *testing.T) {
	store := NewPlaybackControlTicketStore(nil)
	ctx := context.Background()
	base := PlaybackControlTicket{Identity: evt.SocketIdentity{UserID: 7, Role: "user", SessionID: "login", ProfileID: "profile", AccessFingerprint: "scope", AccessExpiresAt: time.Now().Add(time.Minute)}, Binding: PlaybackControlBinding{PlaybackSessionID: "s"}}
	for name, mutate := range map[string]func(*PlaybackControlTicket){
		"no fingerprint": func(t *PlaybackControlTicket) { t.Identity.AccessFingerprint = "" },
		"no profile":     func(t *PlaybackControlTicket) { t.Identity.ProfileID = "" },
		"no session":     func(t *PlaybackControlTicket) { t.Binding.PlaybackSessionID = "" },
		"expired access": func(t *PlaybackControlTicket) { t.Identity.AccessExpiresAt = time.Now().Add(-time.Second) },
		"no login":       func(t *PlaybackControlTicket) { t.Identity.SessionID = "" },
		"no account":     func(t *PlaybackControlTicket) { t.Identity.UserID = 0 },
	} {
		ticket := base
		mutate(&ticket)
		if _, err := store.Mint(ctx, ticket); !errors.Is(err, evt.ErrSocketTicket) {
			t.Fatalf("%s minted: %v", name, err)
		}
	}
	value, err := store.Mint(ctx, base)
	if err != nil || len(value) != playbackControlTicketLength {
		t.Fatal(value, err)
	}
	for _, bad := range []string{"", "short", strings.Repeat("!", playbackControlTicketLength)} {
		if _, err := store.Consume(ctx, bad); !errors.Is(err, evt.ErrSocketTicket) {
			t.Fatalf("consumed %q", bad)
		}
	}
	got, err := store.Consume(ctx, value)
	if err != nil || got.Binding.PlaybackSessionID != "s" || got.Identity.TicketExpiresAt.IsZero() {
		t.Fatal(got, err)
	}
	if _, err := store.Consume(ctx, value); !errors.Is(err, evt.ErrSocketTicket) {
		t.Fatal("credential consumed twice")
	}
}
