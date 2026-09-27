package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/gorilla/websocket"
)

func adminLogsSocketFixture(t *testing.T) (*AdminLogsSocketV2, *logstream.Hub, *atomic.Bool, *atomic.Bool) {
	t.Helper()
	hub := logstream.NewHub("admin-logs-fixture", nil)
	invalid, demoted := new(atomic.Bool), new(atomic.Bool)
	h := &AdminLogsSocketV2{Logs: NewAdminLogsHandler(nil, nil, hub), Tickets: evt.NewSocketTicketStore(nil), checkInterval: 10 * time.Millisecond}
	h.Validate = func(ctx context.Context, proof evt.SocketIdentity) (context.Context, *auth.Claims, error) {
		if invalid.Load() {
			return ctx, nil, evt.ErrSocketTicket
		}
		role := proof.Role
		if demoted.Load() {
			role = "user"
		}
		return ctx, &auth.Claims{UserID: proof.UserID, Role: role, SessionID: proof.SessionID}, nil
	}
	return h, hub, invalid, demoted
}

func adminLogsTicket(t *testing.T, h *AdminLogsSocketV2, role string) string {
	t.Helper()
	ticket, err := h.Tickets.Mint(t.Context(), evt.SocketIdentity{UserID: 7, Role: role, EffectiveRole: role, SessionID: "session", ProfileID: "profile", AccessFingerprint: "scope", AccessExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

// Mint admits only an effective administrator; the ticket store never sees a
// demoted or invalid session.
func TestAdminLogsSocketV2MintRequiresAdministrator(t *testing.T) {
	h, _, invalid, demoted := adminLogsSocketFixture(t)
	identity := evt.SocketIdentity{UserID: 7, Role: "admin", SessionID: "session", ProfileID: "profile", AccessExpiresAt: time.Now().Add(time.Minute)}
	if _, err := (&AdminLogsSocketV2{}).Mint(t.Context(), identity); err == nil {
		t.Fatal("unwired mint accepted")
	}
	demoted.Store(true)
	if _, err := h.Mint(t.Context(), identity); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("secondary-profile mint err = %v", err)
	}
	demoted.Store(false)
	invalid.Store(true)
	if _, err := h.Mint(t.Context(), identity); err == nil {
		t.Fatal("invalid session minted")
	}
}

// The handshake refuses URL proof, foreign origins, missing protocol, a bad
// stream selection and a malformed upgrade before consuming the credential;
// a good upgrade echoes only the protocol and answers with the snapshot frame,
// and the spent credential cannot be replayed.
func TestAdminLogsSocketV2HandshakeAndReplay(t *testing.T) {
	h, hub, _, _ := adminLogsSocketFixture(t)
	server := httptest.NewServer(h)
	defer server.Close()
	h.PublicOrigin = server.URL
	ticket := adminLogsTicket(t, h, "admin")
	base := "ws" + strings.TrimPrefix(server.URL, "http")
	dialer := websocket.Dialer{Subprotocols: []string{AdminLogsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	refuse := func(t *testing.T, url string, header http.Header, dial websocket.Dialer, want int) {
		t.Helper()
		conn, resp, err := dial.DialContext(t.Context(), url, header)
		if conn != nil {
			_ = conn.Close()
		}
		if err == nil || resp == nil || resp.StatusCode != want {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Fatalf("%s: status %d err %v, want %d", url, code, err, want)
		}
		_ = resp.Body.Close()
	}
	refuse(t, base+"?stream=app&token=abc", nil, dialer, 400)
	refuse(t, base+"?stream=app", http.Header{"Origin": []string{"https://other.example.test"}}, dialer, 403)
	refuse(t, base+"?stream=app", nil, websocket.Dialer{}, 400)
	refuse(t, base+"?stream=nope", nil, dialer, 400)
	refuse(t, base+"?stream=app&user_id=abc", nil, dialer, 400)
	// Still unspent: the good handshake succeeds.
	conn, resp, err := dialer.DialContext(t.Context(), base+"?stream=app", http.Header{"Origin": []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 101 || conn.Subprotocol() != AdminLogsSocketProtocol {
		t.Fatalf("upgrade: %d %q", resp.StatusCode, conn.Subprotocol())
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, body, err := conn.ReadMessage()
	// No repository is wired in this fixture: the shared loop reports that as
	// the bridge does, with an error frame, proving the loop is the same code.
	if err != nil || !strings.Contains(string(body), `"type":"error"`) || strings.Contains(string(body), ticket) {
		t.Fatalf("first frame: %s %v", body, err)
	}
	again, replay, err := dialer.DialContext(t.Context(), base+"?stream=app", nil)
	if again != nil {
		_ = again.Close()
	}
	if err == nil || replay.StatusCode != 401 {
		t.Fatal("ticket replay accepted")
	}
	_ = replay.Body.Close()
	// A credential whose session is no longer an administrator is refused at upgrade.
	demotedHandler, _, _, demoted := adminLogsSocketFixture(t)
	demotedServer := httptest.NewServer(demotedHandler)
	defer demotedServer.Close()
	stale := adminLogsTicket(t, demotedHandler, "admin")
	demoted.Store(true)
	refuse(t, "ws"+strings.TrimPrefix(demotedServer.URL, "http")+"?stream=app", nil, websocket.Dialer{Subprotocols: []string{AdminLogsSocketProtocol, eventsTicketProtocolPrefix + stale}}, 403)
	_ = hub
}

// Losing administrator authority or the session closes an open stream.
func TestAdminLogsSocketV2ClosesOnAuthorityLoss(t *testing.T) {
	for name, trip := range map[string]func(invalid, demoted *atomic.Bool){"revoked": func(i, _ *atomic.Bool) { i.Store(true) }, "demoted": func(_, d *atomic.Bool) { d.Store(true) }} {
		t.Run(name, func(t *testing.T) {
			h, _, invalid, demoted := adminLogsSocketFixture(t)
			server := httptest.NewServer(h)
			defer server.Close()
			ticket := adminLogsTicket(t, h, "admin")
			dialer := websocket.Dialer{Subprotocols: []string{AdminLogsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
			conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"?stream=audit", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			defer func() { _ = resp.Body.Close() }()
			trip(invalid, demoted)
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					if strings.Contains(err.Error(), "timeout") {
						t.Fatal("authority loss did not close the stream")
					}
					return
				}
			}
		})
	}
}

// The bridge route keeps its answers: 503 without a hub, 400 bad_request for a
// bad stream, and it still upgrades with the shared origin check.
func TestHandleLogStreamWebSocketKeepsBridgeStatuses(t *testing.T) {
	rec := httptest.NewRecorder()
	NewAdminLogsHandler(nil, nil, nil).HandleLogStreamWebSocket(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/logs/ws?stream=app", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no hub: %d", rec.Code)
	}
	h := NewAdminLogsHandler(nil, nil, logstream.NewHub("bridge-fixture", nil))
	rec = httptest.NewRecorder()
	h.HandleLogStreamWebSocket(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/logs/ws?stream=nope", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"bad_request"`) || !strings.Contains(rec.Body.String(), "Invalid stream") {
		t.Fatalf("bad stream: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.HandleLogStreamWebSocket(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/logs/ws?stream=app&user_id=abc", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "user_id") {
		t.Fatalf("bad filter: %d %s", rec.Code, rec.Body.String())
	}
	server := httptest.NewServer(http.HandlerFunc(h.HandleLogStreamWebSocket))
	defer server.Close()
	conn, resp, err := websocket.DefaultDialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"?stream=app&token=legacy", nil)
	if err != nil {
		t.Fatalf("bridge upgrade with URL token: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = resp.Body.Close()
}
