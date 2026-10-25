package abs

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

// refreshCookieFrom runs handler behind clientip.Middleware with a resolver
// that trusts trusted, and returns the refresh_token Set-Cookie it wrote.
func refreshCookieFrom(t *testing.T, trusted string, req *http.Request, handler http.HandlerFunc) *http.Cookie {
	t.Helper()
	var cidrs []*net.IPNet
	if trusted != "" {
		_, cidr, err := net.ParseCIDR(trusted)
		if err != nil {
			t.Fatalf("parse CIDR: %v", err)
		}
		cidrs = append(cidrs, cidr)
	}
	rec := httptest.NewRecorder()
	clientip.Middleware(clientip.NewResolver(cidrs))(handler).ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "refresh_token" {
			return c
		}
	}
	t.Fatalf("no refresh_token cookie in response; headers=%v", rec.Header())
	return nil
}

// The refresh cookie is Secure exactly when the client reached the server over
// HTTPS, including through a trusted TLS-terminating proxy, and the logout
// cookie that deletes it carries the same flag so a browser lets it replace a
// Secure cookie. A forged X-Forwarded-Proto from an untrusted peer and a
// plain-HTTP LAN request both leave it non-Secure.
func TestRefreshCookieSecure_FollowsRequestScheme(t *testing.T) {
	h, _, _ := newRefreshTestHandler(t)
	login := func(w http.ResponseWriter, r *http.Request) {
		setRefreshCookie(w, r, "refresh-value", time.Hour)
	}

	cases := []struct {
		name       string
		trusted    string
		proto      string
		wantSecure bool
	}{
		{name: "trusted proxy https", trusted: "192.0.2.0/24", proto: "https", wantSecure: true},
		{name: "trusted proxy http", trusted: "192.0.2.0/24", proto: "http", wantSecure: false},
		{name: "plain http", trusted: "", proto: "", wantSecure: false},
		{name: "untrusted peer forges https", trusted: "", proto: "https", wantSecure: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newReq := func(path string) *http.Request {
				// httptest requests arrive from 192.0.2.1.
				req := httptest.NewRequest(http.MethodPost, path, nil)
				if tc.proto != "" {
					req.Header.Set("X-Forwarded-Proto", tc.proto)
				}
				return req
			}

			set := refreshCookieFrom(t, tc.trusted, newReq("/login"), login)
			if set.Secure != tc.wantSecure {
				t.Errorf("login cookie Secure = %v, want %v", set.Secure, tc.wantSecure)
			}
			if !set.HttpOnly || set.SameSite != http.SameSiteLaxMode {
				t.Errorf("login cookie HttpOnly=%v SameSite=%v, want true/Lax", set.HttpOnly, set.SameSite)
			}

			del := refreshCookieFrom(t, tc.trusted, newReq("/logout"), h.handleLogout)
			if del.MaxAge >= 0 {
				t.Errorf("logout cookie MaxAge = %d, want negative (delete)", del.MaxAge)
			}
			if del.Secure != set.Secure || del.Path != set.Path || del.SameSite != set.SameSite || del.HttpOnly != set.HttpOnly {
				t.Errorf("logout cookie attrs (Secure=%v Path=%q SameSite=%v HttpOnly=%v) differ from login (Secure=%v Path=%q SameSite=%v HttpOnly=%v)",
					del.Secure, del.Path, del.SameSite, del.HttpOnly, set.Secure, set.Path, set.SameSite, set.HttpOnly)
			}
		})
	}
}

// A trusted proxy that sends several X-Forwarded-Proto values leaves the
// scheme ambiguous; a TLS connection still gets a Secure cookie.
func TestRefreshCookieSecure_AmbiguousProxySchemeKeepsTLS(t *testing.T) {
	login := func(w http.ResponseWriter, r *http.Request) {
		setRefreshCookie(w, r, "refresh-value", time.Hour)
	}
	for _, tc := range []struct {
		name       string
		target     string
		wantSecure bool
	}{
		{name: "tls connection", target: "https://example.test/login", wantSecure: true},
		{name: "plain connection", target: "/login", wantSecure: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.target, nil)
			req.Header.Add("X-Forwarded-Proto", "http")
			req.Header.Add("X-Forwarded-Proto", "https")
			got := refreshCookieFrom(t, "192.0.2.0/24", req, login)
			if got.Secure != tc.wantSecure {
				t.Errorf("Secure = %v, want %v", got.Secure, tc.wantSecure)
			}
		})
	}
}

// Without clientip.Middleware, the connection's TLS state alone decides, and
// X-Forwarded-Proto is ignored.
func TestRefreshCookieSecure_WithoutMiddlewareUsesTLS(t *testing.T) {
	forged := httptest.NewRequest(http.MethodPost, "/login", nil)
	forged.Header.Set("X-Forwarded-Proto", "https")
	if refreshCookieSecure(forged) {
		t.Error("plain request with X-Forwarded-Proto: https but no middleware: Secure = true, want false")
	}
	tlsReq := httptest.NewRequest(http.MethodPost, "https://example.test/login", nil)
	if tlsReq.TLS == nil {
		t.Fatal("httptest did not attach TLS state for an https URL")
	}
	if !refreshCookieSecure(tlsReq) {
		t.Error("TLS request without middleware: Secure = false, want true")
	}
}
