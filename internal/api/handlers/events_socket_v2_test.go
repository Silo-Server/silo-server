package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
)

// socketValidity is what the fixture validator answers: nil until a test
// stores the error the next periodic check should fail with.
type socketValidity struct{ err atomic.Pointer[error] }

func (v *socketValidity) fail(err error) { v.err.Store(&err) }

func socketTestHandler() (*EventsSocketV2, *socketValidity) {
	validity := new(socketValidity)
	h := &EventsSocketV2{Events: NewEventsHandler(evt.NewHub("socket-fixture", nil), nil, nil, nil, nil, nil), Tickets: evt.NewSocketTicketStore(nil, evt.SocketAudienceEvents), checkInterval: time.Millisecond * 10}
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

// Every way the server ends an open events socket sends a close frame whose
// code and reason tell the client what to do next; an access change also
// sends the access_changed frame first.
func TestEventsSocketV2ClosesWithCodeForCause(t *testing.T) {
	for name, tc := range map[string]struct {
		cause      error
		lifetime   time.Duration
		wantCode   int
		wantReason string
	}{
		"access changed":     {cause: errSocketAccessChanged, wantCode: SocketCloseAccessChanged, wantReason: "access_changed"},
		"credential invalid": {cause: errSocketCredentialInvalid, wantCode: SocketCloseCredentialInvalid, wantReason: "credential_invalid"},
		"plain refusal":      {cause: evt.ErrSocketTicket, wantCode: SocketCloseCredentialInvalid, wantReason: "credential_invalid"},
		"check failed":       {cause: socketAuthorityUnavailable(context.DeadlineExceeded), wantCode: SocketCloseAuthorityUnavailable, wantReason: "authority_unavailable"},
		"lifetime ended":     {lifetime: 150 * time.Millisecond, wantCode: SocketCloseLifetimeEnded, wantReason: "lifetime_ended"},
	} {
		t.Run(name, func(t *testing.T) {
			h, validity := socketTestHandler()
			server := httptest.NewServer(h)
			defer server.Close()
			lifetime := tc.lifetime
			if lifetime == 0 {
				lifetime = time.Minute
			}
			ticket := socketTestTicket(t, h, time.Now().Add(lifetime))
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
			if tc.cause != nil {
				validity.fail(tc.cause)
			}
			frames, err := readUntilClosed(t, conn)

			var closeErr *websocket.CloseError
			if !errors.As(err, &closeErr) || closeErr.Code != tc.wantCode || closeErr.Text != tc.wantReason {
				t.Fatalf("close = %v, want %d %q", err, tc.wantCode, tc.wantReason)
			}
			signaled := slices.Contains(frames, `{"type":"access_changed"}`)
			if wantSignal := tc.wantCode == SocketCloseAccessChanged; signaled != wantSignal {
				t.Fatalf("frames %q: access_changed frame = %v, want %v", frames, signaled, wantSignal)
			}
			if signaled && frames[len(frames)-1] != `{"type":"access_changed"}` {
				t.Fatalf("frames %q: access_changed must be the last frame before the close", frames)
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

func (s *socketSessionFixture) ActiveSession(context.Context, string, int) (auth.SessionAuthority, bool, error) {
	if !s.valid {
		return auth.SessionAuthority{}, false, nil
	}
	if s.remoteProfile != "" {
		return auth.SessionAuthority{Role: models.RoleUser, Purpose: models.SessionPurposeRemotePlayback, BoundProfileID: s.remoteProfile}, true, nil
	}
	return auth.SessionAuthority{Role: s.account.user.Role, Purpose: models.SessionPurposeAccount}, true, nil
}

// socketUserFixture is one account; err, when set, is the read's failure.
type socketUserFixture struct {
	user models.User
	err  error
}

func (s *socketUserFixture) GetByID(context.Context, int) (*models.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &s.user, nil
}

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
	h := NewEventsSocketV2(events.Events, events.Tickets, sessions, users, viewer, apimw.NewActingAdminDecider(func(context.Context, int, string) (bool, bool, error) { return primaryFlag, true, nil }, nil), nil, "")
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
	if _, err := h.Mint(t.Context(), evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "admin", ProfileID: "secondary", AccessExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, evt.ErrSocketAuthorityUnavailable) {
		t.Fatalf("mint with degraded preferences: error = %v, want refused as retryable", err)
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
	h := NewEventsSocketV2(events.Events, events.Tickets, sessions, users, viewer, apimw.NewActingAdminDecider(func(context.Context, int, string) (bool, bool, error) { return true, true, nil }, nil), nil, "")
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
	h := NewEventsSocketV2(events.Events, events.Tickets, sessions, users, viewer, decide, nil, "")
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
	// Refused, but as a check that failed: the credential may still hold.
	if _, _, err := h.Validate(t.Context(), demoted); !errors.Is(err, evt.ErrSocketAuthorityUnavailable) {
		t.Fatalf("failed household lookup: %v, want the check refused as unavailable", err)
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
	h := NewEventsSocketV2(events.Events, events.Tickets, sessions, users, viewer, decide, nil, "")
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

// socketKeyFixture is one API key's current configuration; nil once deleted.
// err, when set, is the read's failure.
type socketKeyFixture struct {
	key atomic.Pointer[models.APIKeyMetadata]
	err error
}

func (s *socketKeyFixture) GetMetadataByID(_ context.Context, id int64) (*models.APIKeyMetadata, error) {
	if s.err != nil {
		return nil, s.err
	}
	key := s.key.Load()
	if key == nil || key.ID != id {
		return nil, auth.ErrAPIKeyNotFound
	}
	copied := *key
	return &copied, nil
}

func (s *socketKeyFixture) edit(change func(*models.APIKeyMetadata)) {
	key := *s.key.Load()
	change(&key)
	key.Revision++
	s.key.Store(&key)
}

// monitorKeySocket is an events socket whose account 7 is an enabled
// administrator holding monitor key 3 at revision 40. acting is the
// acting-admin decision's answer.
type monitorKeySocket struct {
	h        *EventsSocketV2
	users    *socketUserFixture
	sessions *socketSessionFixture
	viewer   *socketViewerFixture
	keys     *socketKeyFixture
	acting   *atomic.Bool
	identity evt.SocketIdentity
}

func newMonitorKeySocket(t *testing.T) *monitorKeySocket {
	events, _ := socketTestHandler()
	f := &monitorKeySocket{users: &socketUserFixture{user: models.User{ID: 7, Role: "admin", Enabled: true}}, keys: new(socketKeyFixture), acting: new(atomic.Bool)}
	f.sessions = &socketSessionFixture{valid: true, account: f.users}
	f.viewer = &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileVerified: true, PolicyRevision: 1}}
	f.keys.key.Store(&models.APIKeyMetadata{ID: 3, UserID: 7, Scopes: []string{auth.ScopeAdminMonitorRead}, Revision: 40})
	f.acting.Store(true)
	decide := func(_ context.Context, in apimw.ActingAdminRequest) (bool, error) {
		if in.Claims.TokenType != auth.TokenTypeAPIKey || in.ProfileID != "" {
			t.Errorf("acting-admin decision for a key got %+v, profile %q", in.Claims, in.ProfileID)
		}
		return f.acting.Load(), nil
	}
	f.h = NewEventsSocketV2(events.Events, events.Tickets, f.sessions, f.users, f.viewer, decide, f.keys, "")
	f.h.checkInterval = 10 * time.Millisecond
	f.identity = evt.SocketIdentity{UserID: 7, Role: "admin", APIKeyID: 3, AccessExpiresAt: time.Now().Add(evt.APIKeySocketMaxLifetime)}
	return f
}

// An admin:monitor:read key opens the events socket as itself: bound to the
// key's revision at mint, rechecked against the key and its account, held
// to the monitoring channels, and refused by the login-only sockets.
func TestEventsSocketV2AdmitsMonitorKey(t *testing.T) {
	f := newMonitorKeySocket(t)
	h, users, keys, acting, identity := f.h, f.users, f.keys, f.acting, f.identity
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
	proof := mint()
	if proof.APIKeyRevision != 40 || proof.SessionID != "" || proof.MaxLifetime() != evt.APIKeySocketMaxLifetime {
		t.Fatalf("key ticket = %+v", proof)
	}
	_, claims, err := h.Validate(t.Context(), proof)
	if err != nil || claims.Role != "admin" || claims.TokenType != auth.TokenTypeAPIKey || claims.SessionID != "" {
		t.Fatalf("key claims = %+v, %v", claims, err)
	}
	if channels := allowedChannelsForClaims(claims); !slices.Equal(channels, []evt.EventChannel{evt.ChannelCatalog, evt.ChannelSessions, evt.ChannelDownloads}) {
		t.Fatalf("monitor key channels = %v", channels)
	}
	login := newSocketAuthorityValidator(f.sessions, users, f.viewer, apimw.NewActingAdminDecider(nil, nil))
	withSession := proof
	withSession.SessionID = "session"
	for _, keyProof := range []evt.SocketIdentity{proof, withSession} {
		if _, _, err := login(t.Context(), keyProof); !errors.Is(err, evt.ErrSocketTicket) {
			t.Fatalf("login-only socket validator accepted a key: %v", err)
		}
	}

	accessChanged := func(name string) {
		t.Helper()
		if _, _, err := h.Validate(t.Context(), proof); !errors.Is(err, errSocketAccessChanged) {
			t.Fatalf("%s: error = %v, want an access change", name, err)
		}
	}
	credentialEnded := func(name string) {
		t.Helper()
		if _, _, err := h.Validate(t.Context(), proof); !errors.Is(err, errSocketCredentialInvalid) {
			t.Fatalf("%s: error = %v, want an ended credential", name, err)
		}
	}
	// A failed read says nothing about the credential: it must not be
	// reported as revoked.
	uncheckable := func(name string) {
		t.Helper()
		if _, _, err := h.Validate(t.Context(), proof); !errors.Is(err, evt.ErrSocketAuthorityUnavailable) || errors.Is(err, evt.ErrSocketTicket) {
			t.Fatalf("%s: error = %v, want an unavailable check", name, err)
		}
	}
	users.user.Enabled = false
	credentialEnded("disabled account")
	users.user.Enabled = true
	users.err = auth.ErrNotFound
	credentialEnded("deleted account")
	users.err = context.DeadlineExceeded
	uncheckable("account read timed out")
	users.err = nil
	keys.err = errors.New("connection refused")
	uncheckable("key read failed")
	keys.err = nil
	users.user.Role = "user"
	accessChanged("demoted account")
	users.user.Role = "admin"
	acting.Store(false)
	accessChanged("acting-admin decision refused")
	acting.Store(true)
	keys.edit(func(k *models.APIKeyMetadata) { k.Label = "renamed" })
	accessChanged("edited key")
	proof = mint()
	keys.edit(func(k *models.APIKeyMetadata) { k.Scopes = []string{auth.ScopeAdminSessionsControl} })
	accessChanged("monitor scope removed")
	if _, err := h.Mint(t.Context(), identity); err == nil {
		t.Fatal("key without the monitor scope minted")
	}
}

// A user key opens the events socket as the profile it acts as: with the user
// role even on an admin account, held to the channels of its event scopes, and
// rechecked against the key, its binding and the profile's viewer scope.
func TestEventsSocketV2AdmitsUserKey(t *testing.T) {
	f := newMonitorKeySocket(t)
	h, keys := f.h, f.keys
	f.viewer.scope = access.Scope{UserID: 7, ProfileID: "kid", ProfileVerified: true, PolicyRevision: 1}
	keys.key.Store(&models.APIKeyMetadata{ID: 3, UserID: 7, ProfileID: "kid", Scopes: []string{auth.ScopeWatchRead}, Revision: 40})
	identity := evt.SocketIdentity{UserID: 7, Role: "user", APIKeyID: 3, ProfileID: "kid", AccessExpiresAt: time.Now().Add(evt.APIKeySocketMaxLifetime)}
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
	proof := mint()
	if proof.AccessFingerprint == "" || proof.APIKeyRevision != 40 || proof.ProfileID != "kid" {
		t.Fatalf("user key ticket = %+v", proof)
	}
	_, claims, err := h.Validate(t.Context(), proof)
	if err != nil || claims.Role != "user" || claims.TokenType != auth.TokenTypeAPIKey || claims.APIKeyProfileID != "kid" {
		t.Fatalf("user key claims = %+v, %v", claims, err)
	}
	if channels := allowedChannelsForClaims(claims); !slices.Equal(channels, []evt.EventChannel{evt.ChannelUserState}) {
		t.Fatalf("watch:read key channels = %v", channels)
	}
	both := *claims
	both.APIKeyScopes = []string{auth.ScopeCatalogRead, auth.ScopeWatchRead, auth.ScopeNotifications, auth.ScopeRequests}
	if channels := allowedChannelsForClaims(&both); !slices.Equal(channels, []evt.EventChannel{evt.ChannelCatalog, evt.ChannelUserState, evt.ChannelNotifications}) {
		t.Fatalf("user key channels = %v", channels)
	}
	accessChanged := func(name string, proof evt.SocketIdentity) {
		t.Helper()
		if _, _, err := h.Validate(t.Context(), proof); !errors.Is(err, errSocketAccessChanged) {
			t.Fatalf("%s: error = %v, want an access change", name, err)
		}
	}
	other := proof
	other.ProfileID = "parent"
	accessChanged("another profile than the key's", other)
	f.viewer.scope.PolicyRevision = 2
	accessChanged("changed viewer scope", proof)
	f.viewer.scope.PolicyRevision = 1
	keys.edit(func(k *models.APIKeyMetadata) { k.Scopes = []string{auth.ScopeRequests} })
	accessChanged("event scope removed", proof)
	if _, err := h.Mint(t.Context(), identity); err == nil {
		t.Fatal("key without an event scope minted")
	}
	keys.key.Store(nil)
	if _, _, err := h.Validate(t.Context(), proof); !errors.Is(err, errSocketCredentialInvalid) {
		t.Fatalf("revoked key: %v", err)
	}
}

// A revoked key closes an open monitoring socket at the next recheck with
// credential_invalid, so the client stops reconnecting with it.
func TestEventsSocketV2ClosesWhenKeyRevoked(t *testing.T) {
	f := newMonitorKeySocket(t)
	h, keys := f.h, f.keys
	server := httptest.NewServer(h)
	defer server.Close()
	ticket, err := h.Mint(t.Context(), f.identity)
	if err != nil {
		t.Fatal(err)
	}
	dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"?channels=sessions,jobs", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, hello, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(hello), `"available_channels":["catalog","sessions","downloads"]`) {
		t.Fatalf("hello: %s %v", hello, err)
	}
	_, subscribed, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(subscribed), `"channels":["sessions"]`) || !strings.Contains(string(subscribed), `"channel":"jobs"`) {
		t.Fatalf("subscribed: %s %v", subscribed, err)
	}
	keys.key.Store(nil)
	_, err = readUntilClosed(t, conn)
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != SocketCloseCredentialInvalid || closeErr.Text != "credential_invalid" {
		t.Fatalf("revoked key closed with %v, want 4002 credential_invalid", err)
	}
}

// The sessions channel is a cue to re-read GET /api/v2/admin/sessions: its
// subscribe snapshot and sessions.replaced frames carry null, never rows.
func TestEventsSocketV2SessionsFramesCarryNoRows(t *testing.T) {
	f := newMonitorKeySocket(t)
	server := httptest.NewServer(f.h)
	defer server.Close()
	ticket, err := f.h.Mint(t.Context(), f.identity)
	if err != nil {
		t.Fatal(err)
	}
	dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"?channels=sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	read := func(want string) map[string]json.RawMessage {
		t.Helper()
		for {
			var frame map[string]json.RawMessage
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("waiting for %s: %v", want, err)
			}
			if string(frame["type"]) == `"`+want+`"` {
				return frame
			}
		}
	}
	if snapshot := read("snapshot"); string(snapshot["data"]) != "null" {
		t.Fatalf("sessions snapshot data = %s, want null", snapshot["data"])
	}
	if err := f.h.Events.hub.PublishJSON(t.Context(), evt.ChannelSessions, "sessions.replaced", nil, evt.PublishOptions{AdminOnly: true}); err != nil {
		t.Fatal(err)
	}
	event := read("event")
	if string(event["event"]) != `"sessions.replaced"` || string(event["data"]) != "null" {
		t.Fatalf("sessions.replaced frame = %s %s, want null data", event["event"], event["data"])
	}
}
