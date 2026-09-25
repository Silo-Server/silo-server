package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/gorilla/websocket"
)

func TestSocketOriginTrustedProxy(t *testing.T) {
	cidrs, _ := clientip.ParseCIDRs("127.0.0.0/8")
	resolver := clientip.NewResolver(cidrs)
	for _, tc := range []struct {
		name, origin, host, override, peer string
		proto                              []string
		want                               bool
	}{
		{"tls offload", "https://example.test:8443", "example.test:8443", "", "127.0.0.1:80", []string{"https"}, true},
		{"wrong port", "https://example.test", "example.test:8443", "", "127.0.0.1:80", []string{"https"}, false},
		{"wrong scheme", "http://example.test", "example.test", "", "127.0.0.1:80", []string{"https"}, false},
		{"wrong host", "https://foreign.test", "example.test", "", "127.0.0.1:80", []string{"https"}, false},
		{"spoof", "https://example.test", "example.test", "", "192.0.2.1:80", []string{"https"}, false},
		{"override", "https://public.test", "internal.test", "https://public.test", "127.0.0.1:80", []string{"https"}, true},
		{"override keeps request origin", "https://example.test", "example.test", "https://public.test", "127.0.0.1:80", []string{"https"}, true},
		{"override keeps lan origin", "http://192.0.2.10:8080", "192.0.2.10:8080", "https://public.test", "192.0.2.3:51000", nil, true},
		{"override lan wrong scheme", "https://192.0.2.10:8080", "192.0.2.10:8080", "https://public.test", "192.0.2.3:51000", nil, false},
		{"override lan wrong port", "http://192.0.2.10", "192.0.2.10:8080", "https://public.test", "192.0.2.3:51000", nil, false},
		{"override foreign host", "https://foreign.test", "example.test", "https://public.test", "127.0.0.1:80", []string{"https"}, false},
		{"override ambiguous", "https://example.test", "example.test", "https://public.test", "127.0.0.1:80", []string{"https", "http"}, false},
		{"override ambiguous keeps public", "https://public.test", "internal.test", "https://public.test", "127.0.0.1:80", []string{"https", "http"}, true},
		{"ambiguous", "https://example.test", "example.test", "", "127.0.0.1:80", []string{"https", "http"}, false},
		{"null", "null", "example.test", "", "127.0.0.1:80", nil, false},
		{"path", "http://example.test/path", "example.test", "", "127.0.0.1:80", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://example.test/", nil)
			r.Host = tc.host
			r.RemoteAddr = tc.peer
			r.Header.Set("Origin", tc.origin)
			r.Header["X-Forwarded-Proto"] = tc.proto
			r.Header.Set("X-Forwarded-Host", "foreign.test")
			clientip.Middleware(resolver)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if got := socketOriginAllowed(r, tc.override, nil); got != tc.want {
					t.Fatalf("allowed=%v want %v", got, tc.want)
				}
			})).ServeHTTP(httptest.NewRecorder(), r)
		})
	}
	r := httptest.NewRequest("GET", "http://example.test/", nil)
	if !socketOriginAllowed(r, "", nil) {
		t.Fatal("native originless request refused")
	}
	r.Header["Origin"] = []string{"http://example.test", "http://example.test"}
	if socketOriginAllowed(r, "", nil) {
		t.Fatal("multiple origins allowed")
	}
}

func TestEventsSocketV2TrustedProxyDenialPreservesTicket(t *testing.T) {
	h, _ := socketTestHandler()
	cidrs, _ := clientip.ParseCIDRs("127.0.0.0/8,::1/128")
	server := httptest.NewServer(clientip.Middleware(clientip.NewResolver(cidrs))(h))
	defer server.Close()
	ticket := socketTestTicket(t, h, time.Now().Add(time.Minute))
	dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "?channels=user_state"
	origin := "https" + strings.TrimPrefix(server.URL, "http")
	headers := http.Header{"Origin": []string{origin}, "X-Forwarded-Proto": []string{"https", "http"}, "X-Forwarded-For": []string{"198.51.100.1"}}
	conn, resp, err := dialer.DialContext(t.Context(), endpoint, headers)
	if conn != nil {
		conn.Close()
	}
	if resp != nil {
		defer resp.Body.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ambiguous proxy handshake: %v, %v", resp, err)
	}
	headers.Set("X-Forwarded-Proto", "https")
	conn, resp, err = dialer.DialContext(t.Context(), endpoint, headers)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.Subprotocol() != EventsSocketProtocol {
		t.Fatal("protocol mismatch")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(body), `"type":"hello"`) {
		t.Fatalf("hello=%s error=%v", body, err)
	}
}

// TestEventsSocketV2TraefikWebSocketForwardedProto covers #1089: Traefik
// forwards a TLS WebSocket upgrade with X-Forwarded-Proto: wss, and the
// browser's https origin must still pass the origin check and open the socket.
func TestEventsSocketV2TraefikWebSocketForwardedProto(t *testing.T) {
	h, _ := socketTestHandler()
	cidrs, _ := clientip.ParseCIDRs("127.0.0.0/8,::1/128")
	server := httptest.NewServer(clientip.Middleware(clientip.NewResolver(cidrs))(h))
	defer server.Close()
	ticket := socketTestTicket(t, h, time.Now().Add(time.Minute))
	dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "?channels=user_state"
	origin := "https" + strings.TrimPrefix(server.URL, "http")
	headers := http.Header{"Origin": []string{origin}, "X-Forwarded-Proto": []string{"wss"}, "X-Forwarded-For": []string{"198.51.100.1"}}
	conn, resp, err := dialer.DialContext(t.Context(), endpoint, headers)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("wss-forwarded handshake: %v, %v", resp, err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(body), `"type":"hello"`) {
		t.Fatalf("hello=%s error=%v", body, err)
	}
}
