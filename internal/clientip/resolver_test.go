package clientip

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMiddlewareTrustBoundaryAndHotReload(t *testing.T) {
	trusted, err := ParseCIDRs("127.0.0.0/8,10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(trusted)
	resolve := func(remote, xff, realIP string) string {
		t.Helper()
		var got string
		h := Middleware(resolver)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = r.RemoteAddr
			if contextIP := FromContext(r.Context()); contextIP != got {
				t.Fatalf("context IP = %q, RemoteAddr = %q", contextIP, got)
			}
		}))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", xff)
		r.Header.Set("X-Real-IP", realIP)
		h.ServeHTTP(httptest.NewRecorder(), r)
		return got
	}

	if got := resolve("127.0.0.1:1234", "198.51.100.9", ""); got != "198.51.100.9" {
		t.Fatalf("trusted peer XFF = %q", got)
	}
	if got := resolve("203.0.113.7:1234", "198.51.100.9", ""); got != "203.0.113.7" {
		t.Fatalf("untrusted spoof = %q", got)
	}
	if got := resolve("127.0.0.1:1234", "10.1.1.1, 127.0.0.2", ""); got != "10.1.1.1" {
		t.Fatalf("all-trusted chain = %q", got)
	}
	if got := resolve("127.0.0.1:1234", "", "198.51.100.10"); got != "198.51.100.10" {
		t.Fatalf("X-Real-IP fallback = %q", got)
	}

	narrowed, err := ParseCIDRs("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	resolver.UpdateTrustedCIDRs(narrowed)
	if got := resolve("127.0.0.1:1234", "198.51.100.9", ""); got != "127.0.0.1" {
		t.Fatalf("hot reload did not narrow trust: %q", got)
	}
}

func TestClientIPForwardingHeaders(t *testing.T) {
	trusted, err := ParseCIDRs("127.0.0.0/8,10.0.0.0/8,::1/128,fd00::/8")
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(trusted)

	tests := []struct {
		name   string
		remote string
		xff    []string
		realIP []string
		want   string
	}{
		{name: "no headers from trusted peer", remote: "127.0.0.1:1234", want: "127.0.0.1"},
		{name: "untrusted peer ignores XFF", remote: "203.0.113.7:1234", xff: []string{"198.51.100.9"}, want: "203.0.113.7"},
		{name: "untrusted peer ignores X-Real-IP", remote: "203.0.113.7:1234", realIP: []string{"198.51.100.9"}, want: "203.0.113.7"},
		{name: "single entry", remote: "127.0.0.1:1234", xff: []string{"198.51.100.9"}, want: "198.51.100.9"},
		{name: "appended spoof on one line", remote: "127.0.0.1:1234", xff: []string{"192.0.2.66, 198.51.100.9"}, want: "198.51.100.9"},
		{name: "proxy adds its own line after a spoofed line", remote: "127.0.0.1:1234", xff: []string{"192.0.2.66", "198.51.100.9"}, want: "198.51.100.9"},
		{name: "spoofed list line then proxy line", remote: "127.0.0.1:1234", xff: []string{"192.0.2.66, 192.0.2.67", "198.51.100.9"}, want: "198.51.100.9"},
		{name: "spoofed trusted address before proxy line", remote: "127.0.0.1:1234", xff: []string{"10.9.9.9", "198.51.100.9"}, want: "198.51.100.9"},
		{name: "two trusted hops across lines", remote: "127.0.0.1:1234", xff: []string{"192.0.2.66", "198.51.100.9, 10.0.0.2"}, want: "198.51.100.9"},
		{name: "all trusted returns leftmost", remote: "127.0.0.1:1234", xff: []string{"10.1.1.1, 127.0.0.2"}, want: "10.1.1.1"},
		{name: "all trusted across lines returns leftmost", remote: "127.0.0.1:1234", xff: []string{"10.1.1.1", "10.0.0.2"}, want: "10.1.1.1"},
		{name: "empty entries are skipped", remote: "127.0.0.1:1234", xff: []string{"", "198.51.100.9,"}, want: "198.51.100.9"},
		{name: "entry with port", remote: "127.0.0.1:1234", xff: []string{"192.0.2.66", "198.51.100.9:4433"}, want: "198.51.100.9"},
		{name: "unparsable entry stops the walk", remote: "127.0.0.1:1234", xff: []string{"192.0.2.66, unknown"}, want: "127.0.0.1"},
		{name: "unparsable entry behind trusted hop", remote: "127.0.0.1:1234", xff: []string{"192.0.2.66, unknown, 10.0.0.2"}, want: "10.0.0.2"},
		{name: "IPv6 peer and entry", remote: "[::1]:1234", xff: []string{"2001:db8::66", "2001:db8::9"}, want: "2001:db8::9"},
		{name: "IPv6 entry is normalized", remote: "[::1]:1234", xff: []string{"2001:DB8:0:0::9"}, want: "2001:db8::9"},
		{name: "bracketed IPv6 entry with port", remote: "[::1]:1234", xff: []string{"2001:db8::66, [2001:db8::9]:443"}, want: "2001:db8::9"},
		{name: "bracketed IPv6 entry without port", remote: "[::1]:1234", xff: []string{"[2001:db8::9]"}, want: "2001:db8::9"},
		{name: "IPv6 trusted hop", remote: "[::1]:1234", xff: []string{"2001:db8::66", "2001:db8::9, fd00::2"}, want: "2001:db8::9"},
		{name: "IPv6 all trusted", remote: "[::1]:1234", xff: []string{"fd00::5, fd00::2"}, want: "fd00::5"},
		{name: "IPv4 entry behind IPv6 peer", remote: "[::1]:1234", xff: []string{"198.51.100.9"}, want: "198.51.100.9"},
		{name: "X-Real-IP without XFF", remote: "127.0.0.1:1234", realIP: []string{"198.51.100.10"}, want: "198.51.100.10"},
		{name: "X-Real-IP IPv6 without XFF", remote: "[::1]:1234", realIP: []string{" 2001:db8::10 "}, want: "2001:db8::10"},
		{name: "X-Real-IP when XFF is empty", remote: "127.0.0.1:1234", xff: []string{""}, realIP: []string{"198.51.100.10"}, want: "198.51.100.10"},
		{name: "X-Real-IP when XFF names no address", remote: "127.0.0.1:1234", xff: []string{"unknown"}, realIP: []string{"198.51.100.10"}, want: "198.51.100.10"},
		{name: "XFF entry wins over X-Real-IP", remote: "127.0.0.1:1234", xff: []string{"198.51.100.9"}, realIP: []string{"192.0.2.66"}, want: "198.51.100.9"},
		{name: "trusted XFF entry wins over X-Real-IP", remote: "127.0.0.1:1234", xff: []string{"10.1.1.1"}, realIP: []string{"192.0.2.66"}, want: "10.1.1.1"},
		{name: "trusted hop before unparsable entry wins over X-Real-IP", remote: "127.0.0.1:1234", xff: []string{"unknown, 10.0.0.2"}, realIP: []string{"192.0.2.66"}, want: "10.0.0.2"},
		{name: "repeated X-Real-IP is ignored", remote: "127.0.0.1:1234", realIP: []string{"192.0.2.66", "198.51.100.10"}, want: "127.0.0.1"},
		{name: "list-valued X-Real-IP is ignored", remote: "127.0.0.1:1234", realIP: []string{"192.0.2.66, 198.51.100.10"}, want: "127.0.0.1"},
		{name: "invalid X-Real-IP is ignored", remote: "127.0.0.1:1234", realIP: []string{"unknown"}, want: "127.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			for _, v := range tc.realIP {
				r.Header.Add("X-Real-IP", v)
			}
			if got := resolver.ClientIP(r); got != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// A proxy that adds its own X-Forwarded-For line after the client's sends two
// header lines on the wire; the resolver must read past the first.
func TestMiddlewareReadsEveryForwardedForLine(t *testing.T) {
	trusted, err := ParseCIDRs("127.0.0.0/8,::1/128")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Middleware(NewResolver(trusted))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, FromContext(r.Context()))
	})))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: silo\r\n"+
		"X-Forwarded-For: 192.0.2.66\r\n"+
		"X-Forwarded-For: 198.51.100.9\r\n"+
		"Connection: close\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "198.51.100.9" {
		t.Fatalf("client IP = %q, want the proxy-added line's 198.51.100.9", got)
	}
}
