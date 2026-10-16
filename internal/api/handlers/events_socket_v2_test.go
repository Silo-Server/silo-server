package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/gorilla/websocket"
)

// socketValidity is what the fixture validator answers: nil until a test
// stores the error the next periodic check should fail with.
type socketValidity struct{ err atomic.Pointer[error] }

func (v *socketValidity) fail(err error) { v.err.Store(&err) }

func socketTestHandler() (*EventsSocketV2, *socketValidity) {
	validity := new(socketValidity)
	h := &EventsSocketV2{Events: NewEventsHandler(evt.NewHub("socket-fixture", nil), nil, nil, nil, nil, nil, nil), Tickets: evt.NewSocketTicketStore(nil), checkInterval: time.Millisecond * 10}
	h.Validate = func(ctx context.Context, proof evt.SocketIdentity) (context.Context, *auth.Claims, error) {
		if err := validity.err.Load(); err != nil {
			return ctx, nil, *err
		}
		return ctx, &auth.Claims{UserID: proof.UserID, Role: proof.Role, SessionID: proof.SessionID}, nil
	}
	return h, validity
}
func socketTestTicket(t *testing.T, h *EventsSocketV2, expires time.Time) string {
	t.Helper()
	ticket, err := h.Tickets.Mint(t.Context(), evt.SocketIdentity{UserID: 7, Role: "user", SessionID: "session", ProfileID: "profile", AccessFingerprint: "scope", AccessExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}
func TestEventsSocketV2ProofOriginProtocolAndReplay(t *testing.T) {
	h, _ := socketTestHandler()
	server := httptest.NewServer(h)
	defer server.Close()
	h.PublicOrigin = server.URL
	ticket := socketTestTicket(t, h, time.Now().Add(time.Minute))
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "?channels=user_state"
	dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	conn, resp, err := dialer.DialContext(t.Context(), endpoint, http.Header{"Origin": []string{"https://other.example.test"}})
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || resp.StatusCode != 403 {
		t.Fatal("foreign origin accepted")
	}
	_ = resp.Body.Close()
	conn, resp, err = dialer.DialContext(t.Context(), endpoint, http.Header{"Origin": []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	if conn.Subprotocol() != EventsSocketProtocol {
		t.Fatal("credential protocol echoed")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(body), `"type":"hello"`) || strings.Contains(string(body), ticket) {
		t.Fatalf("hello: %s %v", body, err)
	}
	again, replay, err := dialer.DialContext(t.Context(), endpoint, nil)
	if again != nil {
		_ = again.Close()
	}
	if err == nil || replay.StatusCode != 401 {
		t.Fatal("ticket replay accepted")
	}
	_ = replay.Body.Close()
}
func TestEventsSocketV2ClosesOnAuthorityLoss(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "expired"}[expiry], func(t *testing.T) {
			h, validity := socketTestHandler()
			server := httptest.NewServer(h)
			defer server.Close()
			until := time.Now().Add(time.Minute)
			if expiry {
				until = time.Now().Add(150 * time.Millisecond)
			}
			ticket := socketTestTicket(t, h, until)
			dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
			conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"?channels=user_state", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			defer func() { _ = resp.Body.Close() }()
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatal("connection did not become live", err)
			}
			if !expiry {
				validity.fail(evt.ErrSocketTicket)
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			for {
				_, _, err := conn.ReadMessage()
				if err != nil {
					if !websocket.IsUnexpectedCloseError(err, websocket.CloseAbnormalClosure) && websocket.IsCloseError(err, websocket.CloseAbnormalClosure) {
						return
					}
					if strings.Contains(err.Error(), "timeout") {
						t.Fatal("authority loss did not close socket")
					}
					return
				}
			}
		})
	}
}

// socketSessionFixture is one session of account: valid or ended, and a
// remote-playback handoff bound to remoteProfile when that is set. Like the
// repository, it reports the account's current role.
type socketSessionFixture struct {
	valid         bool
	account       *socketUserFixture
	remoteProfile string
}

func (s *socketSessionFixture) ActiveSession(context.Context, string) (auth.SessionAuthority, bool, error) {
	if !s.valid {
		return auth.SessionAuthority{}, false, nil
	}
	if s.remoteProfile != "" {
		return auth.SessionAuthority{Role: models.RoleUser, Purpose: models.SessionPurposeRemotePlayback, BoundProfileID: s.remoteProfile}, true, nil
	}
	return auth.SessionAuthority{Role: s.account.user.Role, Purpose: models.SessionPurposeAccount}, true, nil
}

type socketUserFixture struct{ user models.User }

func (s *socketUserFixture) GetByID(context.Context, int) (*models.User, error) { return &s.user, nil }

type socketViewerFixture struct {
	scope access.Scope
	err   error
}

func (s *socketViewerFixture) Resolve(context.Context, access.ResolveInput) (access.Scope, error) {
	return s.scope, s.err
}

func TestEventsSocketV2ValidatesCurrentSessionAccountAndProfile(t *testing.T) {
	events, _ := socketTestHandler()
	users := &socketUserFixture{user: models.User{ID: 7, Role: "admin", Enabled: true}}
	sessions := &socketSessionFixture{valid: true, account: users}
	viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "secondary", ProfileVerified: true, PolicyRevision: 1}}
	primaryFlag := false
	h := NewEventsSocketV2(events.Events, events.Tickets, sessions, users, viewer, apimw.NewActingAdminDecider(func(context.Context, int, string) (bool, bool, error) { return primaryFlag, true, nil }, nil), "")
	proof := evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "admin", ProfileID: "secondary", AccessExpiresAt: time.Now().Add(time.Minute)}
	ticket, err := h.Mint(t.Context(), proof)
	if err != nil {
		t.Fatal(err)
	}
	proof, err = h.Tickets.Consume(t.Context(), ticket)
	if err != nil {
		t.Fatal(err)
	}
	_, claims, err := h.Validate(t.Context(), proof)
	if err != nil || claims.Role == "admin" {
		t.Fatal("secondary profile gained administrator channels")
	}
	// Access changes under a valid session are told apart from a dead
	// credential, and both still fail validation.
	accessChanged := func(name string) {
		t.Helper()
		_, _, err := h.Validate(t.Context(), proof)
		if !errors.Is(err, errSocketAccessChanged) || !errors.Is(err, evt.ErrSocketTicket) {
			t.Fatalf("%s: error = %v, want an access change", name, err)
		}
	}
	credentialEnded := func(name string) {
		t.Helper()
		_, _, err := h.Validate(t.Context(), proof)
		if !errors.Is(err, evt.ErrSocketTicket) || errors.Is(err, errSocketAccessChanged) {
			t.Fatalf("%s: error = %v, want an ended credential", name, err)
		}
	}
	primaryFlag = true
	accessChanged("changed administrator-profile authority")
	primaryFlag = false
	sessions.valid = false
	credentialEnded("revoked session")
	sessions.valid = true
	users.user.Enabled = false
	credentialEnded("disabled account")
	users.user.Enabled = true
	users.user.Role = "user"
	accessChanged("changed account role")
	users.user.Role = "admin"
	viewer.scope.PolicyRevision++
	accessChanged("changed viewer policy")
	viewer.scope.PolicyRevision--
	viewer.scope.ProfileVerified = false
	accessChanged("profile no longer verified")
	viewer.scope.ProfileVerified = true
	viewer.err = access.ErrProfileUnverified
	accessChanged("stale profile token")
	viewer.err = evt.ErrSocketTicket
	credentialEnded("lost profile proof")
	viewer.err = nil
	// Preferences that fell back to their defaults during a failed read are
	// not an access change, even though the fallback scope hashes
	// differently.
	viewer.scope.DisabledLibraryIDs = []int{3}
	accessChanged("hidden libraries changed")
	viewer.scope.PreferencesDegraded = true
	if _, _, err := h.Validate(t.Context(), proof); err != nil {
		t.Fatalf("degraded preferences: error = %v, want the check skipped", err)
	}
	// Nor does a ticket carry a fingerprint of fallback preferences.
	if _, err := h.Mint(t.Context(), evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "admin", ProfileID: "secondary", AccessExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, evt.ErrSocketTicket) {
		t.Fatalf("mint with degraded preferences: error = %v, want refused", err)
	}
}

// readUntilClosed reads frames until the server ends the connection and
// returns the text frames and the read error that ended it.
func readUntilClosed(t *testing.T, conn *websocket.Conn) ([]string, error) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var frames []string
	for {
		_, body, err := conn.ReadMessage()
		if err != nil {
			if strings.Contains(err.Error(), "timeout") {
				t.Fatal("connection was not closed")
			}
			return frames, err
		}
		frames = append(frames, strings.TrimSpace(string(body)))
	}
}

func TestEventsSocketV2SignalsAccessChangeBeforeClosing(t *testing.T) {
	for name, tc := range map[string]struct {
		err        error
		wantSignal bool
	}{
		"access changed":  {err: errSocketAccessChanged, wantSignal: true},
		"session revoked": {err: evt.ErrSocketTicket},
	} {
		t.Run(name, func(t *testing.T) {
			h, validity := socketTestHandler()
			server := httptest.NewServer(h)
			defer server.Close()
			ticket := socketTestTicket(t, h, time.Now().Add(time.Minute))
			dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
			conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"?channels=user_state", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			defer func() { _ = resp.Body.Close() }()
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatal("connection did not become live", err)
			}

			validity.fail(tc.err)
			frames, err := readUntilClosed(t, conn)

			signaled := slices.Contains(frames, `{"type":"access_changed"}`)
			var closeErr *websocket.CloseError
			closedForAccess := errors.As(err, &closeErr) && closeErr.Code == EventsCloseAccessChanged && closeErr.Text == "access_changed"
			if signaled != tc.wantSignal || closedForAccess != tc.wantSignal {
				t.Fatalf("frames %q, close %v; want access_changed frame and close code 4001 = %v", frames, err, tc.wantSignal)
			}
			if tc.wantSignal && frames[len(frames)-1] != `{"type":"access_changed"}` {
				t.Fatalf("frames %q: access_changed must be the last frame before the close", frames)
			}
		})
	}
}

func TestEventsSocketV2MalformedHandshakeDoesNotConsumeProof(t *testing.T) {
	for _, kind := range []string{"protocol", "key", "version", "body", "query-token"} {
		t.Run(kind, func(t *testing.T) {
			h, _ := socketTestHandler()
			ticket := socketTestTicket(t, h, time.Now().Add(time.Minute))
			req := httptest.NewRequest(http.MethodGet, "http://example.test/events/ws", nil)
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Sec-WebSocket-Version", "13")
			req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			req.Header.Set("Sec-WebSocket-Protocol", EventsSocketProtocol+", "+eventsTicketProtocolPrefix+ticket)
			switch kind {
			case "protocol":
				req.Header.Set("Sec-WebSocket-Protocol", eventsTicketProtocolPrefix+ticket)
			case "key":
				req.Header.Set("Sec-WebSocket-Key", "bad")
			case "version":
				req.Header.Set("Sec-WebSocket-Version", "12")
			case "body":
				req.ContentLength = 1
			case "query-token":
				req.URL.RawQuery = "token=forbidden"
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 400 {
				t.Fatal("malformed handshake accepted", rec.Code)
			}
			if _, err := h.Tickets.Consume(t.Context(), ticket); err != nil {
				t.Fatal("malformed handshake consumed proof")
			}
		})
	}
}

// A remote-playback handoff's ticket validates as its bound profile with the
// account role "user", even on an admin account, and a ticket naming another
// profile is refused.
func TestSocketValidatorBindsRemotePlayback(t *testing.T) {
	events, _ := socketTestHandler()
	users := &socketUserFixture{user: models.User{ID: 7, Role: "admin", Enabled: true}}
	sessions := &socketSessionFixture{valid: true, account: users, remoteProfile: "kid"}
	viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "kid", ProfileVerified: true, PolicyRevision: 1}}
	h := NewEventsSocketV2(events.Events, events.Tickets, sessions, users, viewer, apimw.NewActingAdminDecider(func(context.Context, int, string) (bool, bool, error) { return true, true, nil }, nil), "")
	identity := evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "user", ProfileID: "kid", AccessExpiresAt: time.Now().Add(time.Minute)}
	_, claims, err := h.Validate(t.Context(), identity)
	if err != nil || claims.Role != "user" || !claims.IsRemotePlayback() || claims.IsOwnLoginSession() {
		t.Fatalf("handoff ticket = %+v, %v", claims, err)
	}
	identity.ProfileID = "parent"
	if _, _, err := h.Validate(t.Context(), identity); !errors.Is(err, errSocketAccessChanged) {
		t.Fatalf("ticket for another profile: %v", err)
	}
	// A ticket an older release minted with the admin role is an access
	// change: the client fetches a new one.
	identity.ProfileID, identity.Role = "kid", "admin"
	if _, _, err := h.Validate(t.Context(), identity); !errors.Is(err, errSocketAccessChanged) {
		t.Fatalf("admin ticket for a handoff: %v", err)
	}
}

// The Watch Together socket refuses a handoff's ticket, at the handshake and
// on every recheck, even one naming the bound profile that the events socket
// accepts: one minted before the session was bound, or by an older node.
func TestWatchTogetherSocketRefusesRemotePlayback(t *testing.T) {
	users := &socketUserFixture{user: models.User{ID: 7, Role: "user", Enabled: true}}
	sessions := &socketSessionFixture{valid: true, account: users, remoteProfile: "kid"}
	viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "kid", ProfileVerified: true, PolicyRevision: 1}}
	primary := func(context.Context, int, string) (bool, bool, error) { return false, true, nil }
	identity := evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "user", ProfileID: "kid", AccessExpiresAt: time.Now().Add(time.Minute)}
	room := NewWatchTogetherSocketV2(nil, nil, sessions, users, viewer, apimw.NewActingAdminDecider(primary, nil), "")
	if _, _, err := room.Validate(t.Context(), identity); !errors.Is(err, evt.ErrSocketTicket) {
		t.Fatalf("handoff room ticket: %v", err)
	}
	sessions.remoteProfile = ""
	if _, claims, err := room.Validate(t.Context(), identity); err != nil || claims.IsRemotePlayback() {
		t.Fatalf("ordinary room ticket = %+v, %v", claims, err)
	}
}

// A profile-less admin session holds the administrator role only while its
// household does not require a profile, the rule the HTTP admin routes apply:
// a ticket minted after the household gains a PIN or a limit carries the user
// role, and one minted before it stops validating.
func TestSocketValidatorProfileLessAdminFollowsHouseholdRequirement(t *testing.T) {
	events, _ := socketTestHandler()
	users := &socketUserFixture{user: models.User{ID: 7, Role: "admin", Enabled: true}}
	sessions := &socketSessionFixture{valid: true, account: users}
	viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileVerified: true, PolicyRevision: 1}}
	var requiresProfile atomic.Bool
	var householdDown atomic.Bool
	decide := apimw.NewActingAdminDecider(
		func(context.Context, int, string) (bool, bool, error) { return true, true, nil },
		func(context.Context, int) (bool, error) {
			if householdDown.Load() {
				return false, errors.New("profile store down")
			}
			return requiresProfile.Load(), nil
		})
	h := NewEventsSocketV2(events.Events, events.Tickets, sessions, users, viewer, decide, "")
	identity := evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "admin", AccessExpiresAt: time.Now().Add(time.Minute)}
	mint := func() evt.SocketIdentity {
		t.Helper()
		ticket, err := h.Mint(t.Context(), identity)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := h.Tickets.Consume(t.Context(), ticket)
		if err != nil {
			t.Fatal(err)
		}
		return proof
	}

	open := mint()
	if _, claims, err := h.Validate(t.Context(), open); err != nil || claims.Role != "admin" {
		t.Fatalf("household without a requirement: %+v, %v", claims, err)
	}

	requiresProfile.Store(true)
	if _, _, err := h.Validate(t.Context(), open); !errors.Is(err, errSocketAccessChanged) {
		t.Fatalf("open admin socket after the household gained a PIN: %v, want an access change", err)
	}
	demoted := mint()
	if demoted.EffectiveRole != "user" {
		t.Fatalf("profile-less ticket effective role = %q, want user", demoted.EffectiveRole)
	}
	_, claims, err := h.Validate(t.Context(), demoted)
	if err != nil || claims.Role != "user" {
		t.Fatalf("profile-less ticket on a household that requires a profile: %+v, %v", claims, err)
	}
	if channels := allowedChannelsForClaims(claims); slices.Contains(channels, evt.ChannelSessions) {
		t.Fatalf("profile-less session offered the administrator channels: %v", channels)
	}

	householdDown.Store(true)
	if _, _, err := h.Validate(t.Context(), demoted); !errors.Is(err, evt.ErrSocketTicket) {
		t.Fatalf("failed household lookup: %v, want the credential refused", err)
	}
}

// The acting-admin decision sees the minting request's device, as the HTTP
// admin gate does, so a policy keyed on the device answers the same on both.
func TestSocketValidatorPassesDeviceToActingAdmin(t *testing.T) {
	events, _ := socketTestHandler()
	users := &socketUserFixture{user: models.User{ID: 7, Role: "admin", Enabled: true}}
	sessions := &socketSessionFixture{valid: true, account: users}
	viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "parent", ProfileVerified: true, PolicyRevision: 1}}
	var devices []string
	decide := func(_ context.Context, in apimw.ActingAdminRequest) (bool, error) {
		devices = append(devices, in.DeviceID)
		return in.DeviceID != "refused-device", nil
	}
	h := NewEventsSocketV2(events.Events, events.Tickets, sessions, users, viewer, decide, "")
	identity := evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "admin", ProfileID: "parent", DeviceID: "refused-device", AccessExpiresAt: time.Now().Add(time.Minute)}
	if _, claims, err := h.Validate(t.Context(), identity); err != nil || claims.Role != "user" {
		t.Fatalf("refused device: %+v, %v", claims, err)
	}
	identity.DeviceID = "tv-1"
	if _, claims, err := h.Validate(t.Context(), identity); err != nil || claims.Role != "admin" {
		t.Fatalf("allowed device: %+v, %v", claims, err)
	}
	if !slices.Equal(devices, []string{"refused-device", "tv-1"}) {
		t.Fatalf("decider saw devices %v", devices)
	}
}
