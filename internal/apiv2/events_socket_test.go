package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

type fakeEventsSocket struct {
	calls    int
	identity evt.SocketIdentity
}

func (f *fakeEventsSocket) Mint(_ context.Context, identity evt.SocketIdentity) (string, error) {
	f.calls++
	f.identity = identity
	return strings.Repeat("a", 43), nil
}
func (*fakeEventsSocket) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "invalid realtime credential", http.StatusUnauthorized)
}
func TestEventsSocketTicketDelegatesLoginOrMonitorKey(t *testing.T) {
	deps := pilotDeps(nil, nil)
	fake := new(fakeEventsSocket)
	deps.EventsSocket = fake
	claims := &auth.Claims{UserID: 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{map[string]*auth.Claims{memberToken: claims}}, fakeSessions{map[string]string{"s1": "user"}}, nil, nil)
	h := NewHandler(deps)
	path := Prefix + "/events/ws-ticket"
	rec := do(t, h, http.MethodPost, path, "", profileOwner())
	if rec.Code != 200 || fake.calls != 1 || fake.identity.UserID != 1 || fake.identity.SessionID != "s1" || fake.identity.ProfileID != "p-owner" || !strings.Contains(rec.Body.String(), `"protocol":"silo.events.v2"`) {
		t.Fatalf("delegation: %d %s %+v", rec.Code, rec.Body.String(), fake)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response cacheable")
	}
	rec = do(t, h, http.MethodPost, path, "", bearer(memberToken))
	if rec.Code != 200 || fake.identity.ProfileID != "" {
		t.Fatal("account-only delegation refused", rec.Code)
	}
	claims.ExpiresAt = nil
	before := fake.calls
	rec = do(t, h, http.MethodPost, path, "", profileOwner())
	if rec.Code != 403 || fake.calls != before {
		t.Fatal("unbounded credential delegated")
	}

	// An admin:monitor:read key delegates itself, with no session or
	// profile, for the key socket lifetime. Other keys cannot mint.
	keys := fakeAPIKeys{keys: map[string]*models.APIKey{
		"sa_monitor":  {ID: 5, UserID: 2, Scopes: []string{auth.ScopeAdminMonitorRead}},
		"sa_unscoped": {ID: 6, UserID: 2},
		"sa_summary":  {ID: 7, UserID: 2, Scopes: []string{auth.ScopeAdminSessionsSummaryRead}},
		"sa_member":   {ID: 8, UserID: 1, Scopes: []string{auth.ScopeAdminMonitorRead}},
	}}
	deps.EventsCapability = handlers.NewEventsHandler(evt.NewHub("key-capability", nil), nil, nil, nil, nil, nil, nil)
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{}, fakeSessions{}, keys, fakeUsers{users: map[int]*models.User{1: {ID: 1, Role: "user", Enabled: true}, 2: {ID: 2, Role: "admin", Enabled: true}}})
	h = NewHandler(deps)
	rec = do(t, h, http.MethodPost, path, "", bearer("sa_monitor"))
	if rec.Code != 200 || fake.identity.APIKeyID != 5 || fake.identity.UserID != 2 || fake.identity.SessionID != "" || fake.identity.ProfileID != "" || !strings.Contains(rec.Body.String(), `"max_connection_seconds":14400`) {
		t.Fatalf("key delegation: %d %s %+v", rec.Code, rec.Body.String(), fake.identity)
	}
	before = fake.calls
	for _, refused := range []map[string]string{bearer("sa_unscoped"), bearer("sa_summary")} {
		requireProblem(t, do(t, h, http.MethodPost, path, "", refused), TypePermissionDenied)
	}
	rec = do(t, h, http.MethodPost, path, "", with(bearer("sa_member"), "X-Profile-Id", "p-owner"))
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "without a profile") {
		t.Fatalf("key with a profile: %d %s", rec.Code, rec.Body.String())
	}
	if fake.calls != before {
		t.Fatal("refused key minted")
	}
	allowed := func(headers map[string]string) bool {
		t.Helper()
		var body struct {
			Allowed *bool `json:"allowed"`
		}
		rec := do(t, h, http.MethodGet, Prefix+"/events/capabilities", "", headers)
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 || body.Allowed == nil {
			t.Fatalf("capability: %d %s", rec.Code, rec.Body.String())
		}
		return *body.Allowed
	}
	if !allowed(bearer("sa_monitor")) || allowed(with(bearer("sa_monitor"), "X-Profile-Id", "p-owner")) || allowed(bearer("sa_member")) || allowed(bearer("sa_unscoped")) {
		t.Fatal("capability allowed does not follow the key")
	}
}

func eventsSocketFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "events_socket_ticket", operationID: "createEventsSocketTicket", method: "POST", path: Prefix + "/events/ws-ticket", headers: bearer("tok-events"), status: 200, schema: "#/components/schemas/EventsSocketTicket", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "An account delegates its expiring login session to an opaque single-use realtime handshake credential."}}
}

func TestEventsSocketUpgradeThroughV2Router(t *testing.T) {
	deps := pilotDeps(nil, nil)
	store := evt.NewSocketTicketStore(nil, evt.SocketAudienceEvents)
	socket := &handlers.EventsSocketV2{Events: handlers.NewEventsHandler(evt.NewHub("v2-router-fixture", nil), nil, nil, nil, nil, nil, nil), Tickets: store, Validate: func(ctx context.Context, proof evt.SocketIdentity) (context.Context, *auth.Claims, error) {
		return ctx, &auth.Claims{UserID: proof.UserID, Role: "user", SessionID: proof.SessionID}, nil
	}}
	deps.EventsSocket = socket
	ticket, err := store.Mint(t.Context(), evt.SocketIdentity{UserID: 1, SessionID: "session", AccessFingerprint: "scope", Role: "user", AccessExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(deps))
	defer server.Close()
	dialer := websocket.Dialer{Subprotocols: []string{handlers.EventsSocketProtocol, "silo.ticket." + ticket}}
	conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+Prefix+"/events/ws?channels=user_state", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 101 || conn.Subprotocol() != handlers.EventsSocketProtocol {
		t.Fatal("v2 upgrade contract failed")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, body, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(body), `"type":"hello"`) {
		t.Fatalf("hello: %s %v", body, err)
	}
}
