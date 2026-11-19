package historyimport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type plexTestRoundTripFunc func(*http.Request) (*http.Response, error)

func (f plexTestRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestPlexRedirectDropsTokenCrossHost covers what net/http will not do
// for us: X-Plex-Token is a custom header, so the stdlib carries it to whatever
// host a redirect names. Only Authorization, Cookie, and WWW-Authenticate are
// stripped, which would leak the user's Plex credential to an attacker-chosen
// redirect target.
func TestPlexRedirectDropsTokenCrossHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		from      string
		to        string
		wantToken bool
	}{
		{name: "same host", from: "https://plex.example.com/a", to: "https://plex.example.com/b", wantToken: true},
		{name: "same host different port", from: "https://plex.example.com/a", to: "https://plex.example.com:32400/b", wantToken: true},
		{name: "case insensitive host", from: "https://Plex.Example.com/a", to: "https://plex.example.com/b", wantToken: true},
		{name: "cross host", from: "https://plex.example.com/a", to: "https://evil.example/b", wantToken: false},
		{name: "sibling subdomain", from: "https://plex.example.com/a", to: "https://other.example.com/b", wantToken: false},
		{name: "same host downgrade to http", from: "https://plex.example.com/a", to: "http://plex.example.com/b", wantToken: false},
		{name: "cleartext origin same host", from: "http://plex.local:32400/a", to: "http://plex.local:32400/b", wantToken: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			checkRedirect := NewPlexClient().httpClient.CheckRedirect
			previous := httptest.NewRequest(http.MethodGet, test.from, nil)
			redirected := httptest.NewRequest(http.MethodGet, test.to, nil)
			redirected.Header.Set("X-Plex-Token", "account-token")

			if err := checkRedirect(redirected, []*http.Request{previous}); err != nil {
				t.Fatalf("CheckRedirect: %v", err)
			}
			if got := redirected.Header.Get("X-Plex-Token") != ""; got != test.wantToken {
				t.Fatalf("token forwarded = %v, want %v", got, test.wantToken)
			}
		})
	}
}

// TestPlexRedirectKeepsTokenStrippedAfterSameHostHop drives the real
// net/http redirect machinery. Deleting X-Plex-Token inside CheckRedirect only
// clears it for that one hop: the stdlib re-copies the *original* request's
// headers onto every redirect, and only its own sensitive-header list survives
// that. Comparing each destination against the previous hop therefore hands an
// attacker host the token the moment it redirects to itself.
func TestPlexRedirectKeepsTokenStrippedAfterSameHostHop(t *testing.T) {
	t.Parallel()

	const originHost = "plex.example.com"
	const attackerHost = "evil.example"

	var mu sync.Mutex
	tokensByURL := map[string]string{}
	client := &http.Client{
		CheckRedirect: checkPlexRedirect,
		Transport: plexTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			tokensByURL[req.URL.String()] = req.Header.Get("X-Plex-Token")
			mu.Unlock()

			response := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("{}")),
				Request:    req,
			}
			switch req.URL.String() {
			case "https://" + originHost + "/library/sections":
				response.StatusCode = http.StatusFound
				response.Header.Set("Location", "https://"+attackerHost+"/hop1")
			case "https://" + attackerHost + "/hop1":
				response.StatusCode = http.StatusFound
				response.Header.Set("Location", "https://"+attackerHost+"/hop2")
			}
			return response, nil
		}),
	}

	req, err := http.NewRequest(http.MethodGet, "https://"+originHost+"/library/sections", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Plex-Token", "server-access-token")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("redirect chain: %v", err)
	}
	_ = resp.Body.Close()

	if got := tokensByURL["https://"+originHost+"/library/sections"]; got == "" {
		t.Fatal("origin request lost its Plex token")
	}
	for _, path := range []string{"/hop1", "/hop2"} {
		if got := tokensByURL["https://"+attackerHost+path]; got != "" {
			t.Fatalf("%s received the Plex token %q, want it stripped for the whole cross-host chain", path, got)
		}
	}
}

func TestPlexBaseURLCandidatesPreservesPrimaryAndBoundsFallbacks(t *testing.T) {
	t.Parallel()

	got := plexBaseURLCandidates(" https://preferred.example/ ", []string{
		"https://preferred.example",
		"https://fallback-1.example/",
		"",
		"https://fallback-2.example",
		"https://fallback-3.example",
		"https://fallback-4.example",
		"https://fallback-5.example",
		"https://fallback-6.example",
		"https://fallback-7.example",
		"https://fallback-8.example",
	}, MaxPlexConnectionCandidates)

	if len(got) != MaxPlexConnectionCandidates {
		t.Fatalf("candidate count = %d, want %d: %v", len(got), MaxPlexConnectionCandidates, got)
	}
	if got[0] != "https://preferred.example" || got[1] != "https://fallback-1.example" {
		t.Fatalf("candidate order = %v, want preferred then advertised fallbacks", got)
	}
}

// TestPlexSecureCandidatesKeepSecureAddressesWithinTheCap covers the topology
// this change exists for: the promoted remote address is down and the
// connection that still works, the Plex relay, is advertised last, behind
// more cleartext entries than the cap allows. Applying the bound before
// discarding those would drop the relay unprobed.
func TestPlexSecureCandidatesKeepSecureAddressesWithinTheCap(t *testing.T) {
	t.Parallel()

	alternatives := make([]string, 0, 11)
	for i := range 10 {
		alternatives = append(alternatives, "http://192.168.1.10:3240"+strconv.Itoa(i))
	}
	alternatives = append(alternatives, "https://relay.plex.direct:443")

	got := plexSecureBaseURLCandidates("https://down.plex.direct:32400", alternatives)
	want := []string{"https://down.plex.direct:32400", "https://relay.plex.direct:443"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

// TestPlexSecureCandidatesKeepCleartextOnlyServers leaves a server with no
// HTTPS address alone, as a single cleartext plex_base_url was before.
func TestPlexSecureCandidatesKeepCleartextOnlyServers(t *testing.T) {
	t.Parallel()

	got := plexSecureBaseURLCandidates("http://plex.local:32400", []string{"http://plex.local:32401"})
	want := []string{"http://plex.local:32400", "http://plex.local:32401"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestPlexRedirectStopsLongChains(t *testing.T) {
	t.Parallel()

	previous := httptest.NewRequest(http.MethodGet, "https://plex.example.com/a", nil)
	chain := make([]*http.Request, 10)
	for i := range chain {
		chain[i] = previous
	}
	next := httptest.NewRequest(http.MethodGet, "https://plex.example.com/b", nil)
	if err := checkPlexRedirect(next, chain); err == nil {
		t.Fatal("10-hop redirect chain was accepted, want it stopped")
	}
}

// TestAllowedPlexCandidatesSkipPrivateAddresses covers an account without
// local network access whose server plex.tv lists with both LAN and public
// addresses: the LAN ones are skipped instead of refusing the run.
func TestAllowedPlexCandidatesSkipPrivateAddresses(t *testing.T) {
	t.Parallel()

	service := &Service{}
	got, err := service.allowedPlexCandidates(t.Context(), 7, []string{
		"https://192.168.1.5:32400",
		"https://1.1.1.1:32400",
		"https://127.0.0.1:32400",
		"https://9.9.9.9:443",
	})
	if err != nil {
		t.Fatalf("allowedPlexCandidates: %v", err)
	}
	if want := []string{"https://1.1.1.1:32400", "https://9.9.9.9:443"}; !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

// TestPlexSessionCredentialMayOnlyNameAdvertisedAddresses is the rule the
// enqueue transaction applies to a session-backed run.
func TestPlexSessionCredentialMayOnlyNameAdvertisedAddresses(t *testing.T) {
	t.Parallel()

	advertised := []string{"https://remote.plex.direct:32400", "https://local.plex.direct:32400", "https://relay.plex.direct:443"}
	if !containsAll(advertised, []string{"https://relay.plex.direct:443"}) {
		t.Fatal("a filtered subset of the advertised list was refused")
	}
	if containsAll(advertised, []string{"https://remote.plex.direct:32400", "https://attacker.example"}) {
		t.Fatal("a credential naming an unadvertised address was accepted")
	}
	if containsAll(advertised, nil) {
		t.Fatal("a credential with no address was accepted")
	}
}
