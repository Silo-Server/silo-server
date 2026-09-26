package apiv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

type fakeAdminLogsSocket struct {
	available bool
	calls     int
	identity  evt.SocketIdentity
	err       error
	served    int
}

func (f *fakeAdminLogsSocket) Available() bool { return f.available }
func (f *fakeAdminLogsSocket) Mint(_ context.Context, identity evt.SocketIdentity) (string, error) {
	f.calls++
	f.identity = identity
	if f.err != nil {
		return "", f.err
	}
	return strings.Repeat("c", 43), nil
}
func (f *fakeAdminLogsSocket) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.served++
	http.Error(w, "invalid log stream credential", http.StatusUnauthorized)
}

func adminLogsSocketDeps(f *fakeAdminLogsSocket) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.AdminLogsSocket = f
	admin := &auth.Claims{UserID: 2, Role: "admin", SessionID: "s2", TokenType: auth.TokenTypeAccess, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
	member := &auth.Claims{UserID: 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
	unbounded := &auth.Claims{UserID: 2, Role: "admin", SessionID: "s2", TokenType: auth.TokenTypeAccess}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{map[string]*auth.Claims{adminToken: admin, memberToken: member, "tok-unbounded": unbounded}}, fakeSessions{map[string]bool{"s1": true, "s2": true}}, nil, nil)
	return deps
}

func TestAdminLogsSocketTicketAndCapabilities(t *testing.T) {
	f := &fakeAdminLogsSocket{available: true}
	deps := adminLogsSocketDeps(f)
	h := NewHandler(deps)
	ticketPath := Prefix + "/admin/logs/ws-ticket"
	capPath := Prefix + "/admin/logs/ws/capabilities"
	requireProblem(t, do(t, h, http.MethodPost, ticketPath, "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, capPath, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("non-admin reached the mint")
	}
	rec := do(t, h, http.MethodGet, capPath, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":true`) || !strings.Contains(rec.Body.String(), `"protocol":"silo.admin-logs.v2"`) || !strings.Contains(rec.Body.String(), `"streams":["app","audit"]`) || rec.Header().Get("Cache-Control") != cachePrivateNoCache {
		t.Fatal(rec.Code, rec.Body.String(), rec.Header())
	}
	rec = do(t, h, http.MethodPost, ticketPath, "", bearer(adminToken))
	if rec.Code != 200 || f.calls != 1 || f.identity.UserID != 2 || f.identity.SessionID != "s2" || f.identity.Role != "admin" || !strings.Contains(rec.Body.String(), `"protocol":"silo.admin-logs.v2"`) || !strings.Contains(rec.Body.String(), `"max_connection_seconds":300`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("delegation: %d %s %+v", rec.Code, rec.Body.String(), f.identity)
	}
	requireProblem(t, do(t, h, http.MethodPost, ticketPath, "", bearer("tok-unbounded")), TypePermissionDenied)
	f.err = handlers.ErrAdminLogsSocketForbidden
	requireProblem(t, do(t, h, http.MethodPost, ticketPath, "", bearer(adminToken)), TypePermissionDenied)
	f.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Log stream is unavailable"}
	requireProblem(t, do(t, h, http.MethodPost, ticketPath, "", bearer(adminToken)), TypeDependencyUnavailable)
	f.err = nil
	f.available = false
	requireProblem(t, do(t, h, http.MethodPost, ticketPath, "", bearer(adminToken)), TypeDependencyUnavailable)
	rec = do(t, h, http.MethodGet, capPath, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) || !strings.Contains(rec.Body.String(), `"streams":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminLogsSocket = nil
	h = NewHandler(deps)
	requireProblem(t, do(t, h, http.MethodPost, ticketPath, "", bearer(adminToken)), TypeDependencyUnavailable)
	rec = do(t, h, http.MethodGet, Prefix+"/admin/logs/ws?stream=app", "", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("raw route without service: %d", rec.Code)
	}
}

// The raw route delegates to the service and a real handshake upgrades through
// the v2 router with the bridge's frame shapes.
func TestAdminLogsSocketUpgradeThroughV2Router(t *testing.T) {
	deps := pilotDeps(nil, nil)
	store := evt.NewSocketTicketStore(nil)
	socket := &handlers.AdminLogsSocketV2{Logs: handlers.NewAdminLogsHandler(nil, nil, logstream.NewHub("v2-logs-fixture", nil)), Tickets: store, Validate: func(ctx context.Context, proof evt.SocketIdentity) (context.Context, *auth.Claims, error) {
		return ctx, &auth.Claims{UserID: proof.UserID, Role: "admin", SessionID: proof.SessionID}, nil
	}}
	deps.AdminLogsSocket = socket
	ticket, err := store.Mint(t.Context(), evt.SocketIdentity{UserID: 2, SessionID: "s2", AccessFingerprint: "scope", Role: "admin", EffectiveRole: "admin", AccessExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(deps))
	defer server.Close()
	dialer := websocket.Dialer{Subprotocols: []string{handlers.AdminLogsSocketProtocol, "silo.ticket." + ticket}}
	conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+Prefix+"/admin/logs/ws?stream=audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 101 || conn.Subprotocol() != handlers.AdminLogsSocketProtocol {
		t.Fatal("v2 upgrade contract failed")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, body, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(body), `"stream":"audit"`) || !strings.Contains(string(body), `"type":"error"`) {
		t.Fatalf("first frame (no repository wired): %s %v", body, err)
	}
}
