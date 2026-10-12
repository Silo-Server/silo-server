package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

func TestSetupTokenRequired(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		required bool
	}{
		{"no address", t.Context(), true},
		{"lan", clientip.SetContext(t.Context(), "192.168.1.20"), false},
		{"loopback", clientip.SetContext(t.Context(), "::1"), false},
		{"internet", clientip.SetContext(t.Context(), "203.0.113.9"), true},
		{"carrier-grade nat", clientip.SetContext(t.Context(), "100.64.1.2"), true},
		{"network access provider", netaccess.WithPath(clientip.SetContext(t.Context(), "10.0.0.5"), netaccess.Path{Provider: "tailscale"}), true},
	} {
		if got := SetupTokenRequired(tc.ctx); got != tc.required {
			t.Errorf("%s: SetupTokenRequired = %v, want %v", tc.name, got, tc.required)
		}
	}
}

// A browser request that another site started never earns the local
// exemption, even from a local address; requests without browser headers,
// such as a native app's, keep it.
func TestSetupAccessFor(t *testing.T) {
	lan := clientip.SetContext(t.Context(), "192.168.1.20")
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		headers map[string]string
		local   bool
	}{
		{"lan app, no browser headers", lan, nil, true},
		{"lan browser, same origin", lan, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://silo.lan:8090"}, true},
		{"lan browser, cross site", lan, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://attacker.example"}, false},
		{"lan browser, foreign origin only", lan, map[string]string{"Origin": "https://attacker.example"}, false},
		{"internet browser, same origin", clientip.SetContext(t.Context(), "203.0.113.9"), map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://silo.lan:8090/api/v1/auth/setup", nil)
		for k, v := range tc.headers {
			r.Header.Set(k, v)
		}
		access := SetupAccessFor(tc.ctx, r, "token")
		if access.Local != tc.local || access.Token != "token" {
			t.Errorf("%s: SetupAccessFor = %+v, want local %v", tc.name, access, tc.local)
		}
	}
	// A same-origin browser request from the local network keeps the
	// exemption only when the host it addressed cannot be a rebound public
	// name. An app request (no browser headers) is not a rebinding target.
	for _, tc := range []struct {
		host    string
		browser bool
		local   bool
	}{
		{"192.168.1.10:8090", true, true},
		{"[fd00::10]:8090", true, true},
		{"localhost:8090", true, true},
		{"nas:8090", true, true},
		{"silo.local:8090", true, true},
		{"silo.home.arpa", true, true},
		{"attacker.example:8090", true, false},
		{"silo.example.com", true, false},
		{"silo.example.com", false, true},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/api/v2/auth/setup", nil)
		if tc.browser {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Origin", "http://"+tc.host)
		}
		if got := SetupAccessFor(lan, r, "").Local; got != tc.local {
			t.Errorf("host %s (browser %v): local = %v, want %v", tc.host, tc.browser, got, tc.local)
		}
	}
	// Over plain HTTP the web app's status read carries neither Origin nor
	// Sec-Fetch-Site; its own client headers still mark it as a browser.
	for _, header := range [][2]string{{"X-Silo-Client", "Silo Web"}, {"X-Silo-Client-Family", "web"}} {
		r := httptest.NewRequest(http.MethodGet, "http://silo.example.com/api/v2/system/setup", nil)
		r.Header.Set(header[0], header[1])
		if SetupAccessFor(lan, r, "").Local {
			t.Errorf("%s: web app read by a public-looking name counted as local", header[0])
		}
	}
	if SetupAccessFor(lan, nil, "").Local {
		t.Error("a missing request counted as local")
	}
}

// Behind a trusted proxy that rewrites Host and sends X-Forwarded-Host, the
// Origin check compares against the host the browser addressed.
func TestSetupAccessForBehindHostRewritingProxy(t *testing.T) {
	_, proxies, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	mw := clientip.Middleware(clientip.NewResolver([]*net.IPNet{proxies}))
	for _, tc := range []struct {
		origin string
		local  bool
	}{
		{"http://silo.lan:8090", true},
		{"https://attacker.example", false},
	} {
		var got SetupAccess
		h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = SetupAccessFor(r.Context(), r, "")
		}))
		r := httptest.NewRequest(http.MethodPost, "http://10.0.0.2:8080/api/v1/auth/setup", nil)
		r.RemoteAddr = "10.0.0.1:41000"
		r.Header.Set("X-Forwarded-For", "192.168.1.20")
		r.Header.Set("X-Forwarded-Host", "silo.lan:8090")
		r.Header.Set("Origin", tc.origin)
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got.Local != tc.local {
			t.Errorf("Origin %s: local = %v, want %v", tc.origin, got.Local, tc.local)
		}
	}
}

// Forwarding headers from a peer the resolver does not trust leave the
// resolved address at the proxy's own, so the request is not local; the
// default trusted list covers the IPv4 private ranges but not IPv6 ULA.
func TestSetupAccessForUntrustedProxy(t *testing.T) {
	defaults, err := clientip.ParseCIDRs(clientip.DefaultTrustedProxies)
	if err != nil {
		t.Fatal(err)
	}
	_, onlyOne, err := net.ParseCIDR("10.0.0.5/32")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		trusted []*net.IPNet
		peer    string
		xff     string
		local   bool
	}{
		{"untrusted ULA proxy forwarding an internet client", defaults, "[fd00::2]:443", "203.0.113.9", false},
		{"ULA client without forwarding headers", defaults, "[fd00::20]:51000", "", true},
		{"trusted proxy forwarding a LAN client", defaults, "10.0.0.1:443", "192.168.1.20", true},
		{"trusted proxy forwarding an internet client", defaults, "10.0.0.1:443", "203.0.113.9", false},
		{"private proxy outside a narrowed trusted list", []*net.IPNet{onlyOne}, "10.0.0.6:443", "203.0.113.9", false},
	} {
		var got SetupAccess
		h := clientip.Middleware(clientip.NewResolver(tc.trusted))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = SetupAccessFor(r.Context(), r, "")
		}))
		r := httptest.NewRequest(http.MethodPost, "http://192.168.1.10:8090/api/v2/auth/setup", nil)
		r.RemoteAddr = tc.peer
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got.Local != tc.local {
			t.Errorf("%s: local = %v, want %v", tc.name, got.Local, tc.local)
		}
	}
}

func setupTokenCount(t *testing.T, r *UserRepository) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(t.Context(), "SELECT count(*) FROM setup_tokens").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// claimWith runs ClaimInitialSetup with a provision that creates one account,
// so a successful claim leaves setup complete.
func claimWith(t *testing.T, r *UserRepository, access SetupAccess) error {
	t.Helper()
	return r.ClaimInitialSetup(t.Context(), access, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO users (username, email, password_hash, role) VALUES ('owner', 'owner@example.test', 'x', 'admin')`)
		return err
	})
}

func TestSetupTokenGatePostgres(t *testing.T) {
	r := adminAccountsDB(t)

	// Two processes booting on an empty server each get their own token, and
	// either one is accepted.
	first, err := r.IssueSetupToken(t.Context())
	if err != nil || len(first) != 26 {
		t.Fatalf("first token = %q, %v", first, err)
	}
	second, err := r.IssueSetupToken(t.Context())
	if err != nil || second == "" || second == first {
		t.Fatalf("second token = %q, %v", second, err)
	}
	if n := setupTokenCount(t, r); n != 2 {
		t.Fatalf("stored tokens = %d, want 2", n)
	}

	if err := claimWith(t, r, SetupAccess{}); !errors.Is(err, ErrSetupTokenRequired) {
		t.Fatalf("remote without token = %v, want ErrSetupTokenRequired", err)
	}
	if err := claimWith(t, r, SetupAccess{Token: "AAAAAAAAAAAAAAAAAAAAAAAAAA"}); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatalf("remote with wrong token = %v, want ErrSetupTokenInvalid", err)
	}
	// A process that shuts down takes its token with it.
	if err := r.RevokeSetupToken(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := claimWith(t, r, SetupAccess{Token: first}); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatalf("revoked token = %v, want ErrSetupTokenInvalid", err)
	}
	if needs, err := r.Count(t.Context()); err != nil || needs != 0 {
		t.Fatalf("refused claims created accounts: %d, %v", needs, err)
	}

	// Pasted with stray whitespace and in lower case, the live token works,
	// and setup consumes every stored token.
	if err := claimWith(t, r, SetupAccess{Token: "  " + strings.ToLower(second) + "\n"}); err != nil {
		t.Fatalf("claim with live token: %v", err)
	}
	if n := setupTokenCount(t, r); n != 0 {
		t.Fatalf("tokens left after setup = %d", n)
	}
	if err := claimWith(t, r, SetupAccess{Token: second}); !errors.Is(err, ErrSetupAlreadyComplete) {
		t.Fatalf("second claim = %v, want ErrSetupAlreadyComplete", err)
	}
	// A process that boots after setup issues nothing.
	if token, err := r.IssueSetupToken(t.Context()); err != nil || token != "" {
		t.Fatalf("token after setup = %q, %v", token, err)
	}
}

func TestSetupTokenGateLocalPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	if _, err := r.IssueSetupToken(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A local client needs no token, and its setup still clears the tokens.
	if err := claimWith(t, r, SetupAccess{Local: true}); err != nil {
		t.Fatalf("local claim: %v", err)
	}
	if n := setupTokenCount(t, r); n != 0 {
		t.Fatalf("tokens left after local setup = %d", n)
	}
}
