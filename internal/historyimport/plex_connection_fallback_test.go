package historyimport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/netguard"
	"github.com/google/uuid"
)

type plexTestRoundTripFunc func(*http.Request) (*http.Response, error)

func (f plexTestRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestPlexServerProviderFallsBackToReachableConnection(t *testing.T) {
	t.Parallel()

	unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	unreachableURL := unreachable.URL
	unreachable.Close()

	var sectionItemRequests atomic.Int32
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Plex-Token"); got != "server-token" {
			t.Errorf("X-Plex-Token = %q, want server-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"movie","title":"Movies"}]}}`))
			return
		}
		if r.URL.Path == "/library/sections/1/all" {
			sectionItemRequests.Add(1)
		}
		_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
	}))
	defer working.Close()

	provider := NewPlexServerProvider(newUnthrottledPlexClient(), []string{unreachableURL, working.URL}, "server-token")
	records, warnings, err := provider.Fetch(trustLoopback(context.Background()))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(records) != 0 || len(warnings) != 0 {
		t.Fatalf("records = %v, warnings = %v; want both empty", records, warnings)
	}
	// The section listings after the race must go to the connection that won
	// it; a warning above would mean they went to the refused address.
	if got := sectionItemRequests.Load(); got == 0 {
		t.Fatal("the working connection received no section item requests after winning the race")
	}
}

func TestPlexServerProviderUsesFirstRespondingConnection(t *testing.T) {
	t.Parallel()

	slowStarted := make(chan struct{})
	client := NewPlexClient()
	client.httpClient = &http.Client{Transport: plexTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "slow-plex.example" {
			close(slowStarted)
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		<-slowStarted
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"MediaContainer":{}}`)),
			Request:    req,
		}, nil
	})}
	provider := NewPlexServerProvider(client, []string{
		"https://slow-plex.example",
		"https://fast-plex.example",
	}, "server-token")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := provider.fetchLibrarySections(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fetchLibrarySections: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fetchLibrarySections waited for the slow connection instead of using the first response")
	}
	if provider.baseURL != "https://fast-plex.example" {
		t.Fatalf("selected base URL = %q, want fast connection", provider.baseURL)
	}
}

func TestFetchPlexLibrarySectionsRejectsMissingMediaContainer(t *testing.T) {
	t.Parallel()

	notPlex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer notPlex.Close()

	_, err := NewPlexClient().FetchLibrarySections(trustLoopback(context.Background()), notPlex.URL, "server-token")
	if err == nil || !strings.Contains(err.Error(), "MediaContainer") {
		t.Fatalf("FetchLibrarySections error = %v, want missing MediaContainer rejection", err)
	}
}

func TestFetchPlexSectionItemsAcceptsVideoAndPaginates(t *testing.T) {
	t.Parallel()

	plexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch got := r.URL.Query().Get("X-Plex-Container-Start"); got {
		case "0":
			_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":2,"Video":[{"ratingKey":"movie-1","type":"movie","title":"First"}]}}`))
		case "1":
			_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":2,"Video":[{"ratingKey":"movie-2","type":"movie","title":"Second"}]}}`))
		default:
			t.Errorf("X-Plex-Container-Start = %q, want 0 or 1", got)
			http.Error(w, "unexpected offset", http.StatusBadRequest)
		}
	}))
	defer plexServer.Close()

	items, err := NewPlexClient().FetchSectionItems(trustLoopback(context.Background()), plexServer.URL, "server-token", "1", 1)
	if err != nil {
		t.Fatalf("FetchSectionItems: %v", err)
	}
	if len(items) != 2 || items[0].RatingKey != "movie-1" || items[1].RatingKey != "movie-2" {
		t.Fatalf("items = %+v, want both Video pages", items)
	}
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

// TestPlexSessionCandidatesKeepLegacyLocalURL covers a Plex session persisted
// before ConnectionURLs existed: its stored JSON decodes with that field empty,
// so the stored LocalURL is the only address the run has left.
func TestPlexSessionCandidatesKeepLegacyLocalURL(t *testing.T) {
	t.Parallel()

	server := PlexServer{LocalURL: "https://192-168-1-5.hash.plex.direct:32400", HasLocalURL: true}
	got := plexSessionCandidates(server)
	if !slices.Equal(got, []string{server.LocalURL}) {
		t.Fatalf("candidates = %v, want the stored local address %q", got, server.LocalURL)
	}
}

// TestPlexSessionCandidatesPromoteRemoteAndKeepAdvertisedOrder is the list the
// enqueue transaction recomputes to bind a run to its session, so its exact
// contents and order are part of that check, not a cosmetic detail.
func TestPlexSessionCandidatesPromoteRemoteAndKeepAdvertisedOrder(t *testing.T) {
	t.Parallel()

	server := PlexServer{
		RemoteURL: "https://remote.plex.direct:32400",
		LocalURL:  "https://local.plex.direct:32400",
		ConnectionURLs: []string{
			"https://local.plex.direct:32400",
			"https://remote.plex.direct:32400",
			"https://relay.plex.direct:443",
		},
	}
	want := []string{
		"https://remote.plex.direct:32400",
		"https://local.plex.direct:32400",
		"https://relay.plex.direct:443",
	}
	if got := plexSessionCandidates(server); !slices.Equal(got, want) {
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

// TestAllowedPlexCandidatesExplainAllPrivate keeps the specific refusal when
// nothing is left, so the user sees why the run was not started.
func TestAllowedPlexCandidatesExplainAllPrivate(t *testing.T) {
	t.Parallel()

	service := &Service{}
	_, err := service.allowedPlexCandidates(t.Context(), 7, []string{"https://192.168.1.5:32400", "https://10.0.0.5:32400"})
	if !errors.Is(err, netguard.ErrPrivateDestination) {
		t.Fatalf("error = %v, want the private destination refusal", err)
	}
	if _, ok := ServerAddressMessage(err); !ok {
		t.Fatalf("error %v has no user-facing address message", err)
	}
}

// TestPlexRunProbesSkipRefusedConnections drives the race through the guarded
// transport without local network access: the loopback candidate is refused
// at dial time and the run still reaches the public stand-in.
func TestPlexRunProbesSkipRefusedConnections(t *testing.T) {
	t.Parallel()

	var loopbackRequests atomic.Int32
	loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		loopbackRequests.Add(1)
		_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
	}))
	defer loopback.Close()

	client := NewPlexClient()
	guarded := client.httpClient.Transport
	client.httpClient.Transport = plexTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "public-plex.example" {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"MediaContainer":{}}`)), Request: req}, nil
		}
		return guarded.RoundTrip(req)
	})
	provider := NewPlexServerProvider(client, []string{loopback.URL, "https://public-plex.example"}, "server-token")
	if _, err := provider.fetchLibrarySections(context.Background()); err != nil {
		t.Fatalf("fetchLibrarySections: %v", err)
	}
	if provider.baseURL != "https://public-plex.example" {
		t.Fatalf("selected base URL = %q, want the public connection", provider.baseURL)
	}
	if got := loopbackRequests.Load(); got != 0 {
		t.Fatalf("loopback received %d requests without local network access, want 0", got)
	}
}

func TestPlexRunFailsWhenEveryConnectionFails(t *testing.T) {
	t.Parallel()

	client := NewPlexClient()
	client.httpClient = &http.Client{Transport: plexTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	provider := NewPlexServerProvider(client, []string{"https://a.example", "https://b.example"}, "server-token")
	_, err := provider.fetchLibrarySections(context.Background())
	if err == nil || !strings.Contains(err.Error(), "connection 1") || !strings.Contains(err.Error(), "connection 2") {
		t.Fatalf("error = %v, want both connection failures", err)
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

// TestPlexSessionRunBindsToAdvertisedAddresses runs the enqueue transaction's
// check: a session-backed run may carry the advertised list minus addresses
// admission dropped, and nothing the session did not advertise.
func TestPlexSessionRunBindsToAdvertisedAddresses(t *testing.T) {
	repo := personalQueueRepository(t)
	const (
		remote = "https://remote.plex.direct:32400"
		local  = "https://192-168-1-5.hash.plex.direct:32400"
		relay  = "https://relay.plex.direct:443"
	)
	admission := func(candidates ...string) personalRunAdmission {
		t.Helper()
		session, err := repo.CreatePlexSession(t.Context(), PlexSession{
			ID: uuid.NewString(), UserID: 1, PinID: "pin", PinCode: "code", AuthToken: "private-account-token", ExpiresAt: time.Now().Add(time.Hour),
			Servers: []PlexServer{{ClientIdentifier: "server", AccessToken: "private-server-token", RemoteURL: remote, LocalURL: local, ConnectionURLs: []string{local, remote, relay}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return personalRunAdmission{
			UserID: 1, ProfileID: "p", SourceType: SourceTypePlex, ConnectionMode: ConnectionModePlexOAuth,
			PlexSession: session, SelectedServerID: "server",
			Credentials: plexRunCredentials(candidates, "private-server-token", session.AuthToken),
		}
	}

	filtered := admission(remote, relay)
	run, err := repo.enqueuePersonalRun(t.Context(), filtered)
	if err != nil {
		t.Fatalf("enqueue with the LAN address dropped: %v", err)
	}
	credential, err := repo.readPersonalRunCredentials(t.Context(), claimPersonalForTest(t, repo, run.ID))
	if err != nil || !slices.Equal(credential.candidates(), []string{remote, relay}) {
		t.Fatalf("stored candidates = %v (%v), want remote then relay", credential.candidates(), err)
	}

	if _, err := repo.enqueuePersonalRun(t.Context(), admission(remote, "https://attacker.example")); !errors.Is(err, ErrPersonalSessionChanged) {
		t.Fatalf("enqueue with an unadvertised address: %v, want ErrPersonalSessionChanged", err)
	}
}

// TestAllowedPlexCandidatesCapAfterThePolicy covers a server advertising more
// LAN addresses than a run races, with the relay last: an account without
// local network access must still get the relay, and the cap still holds.
func TestAllowedPlexCandidatesCapAfterThePolicy(t *testing.T) {
	t.Parallel()

	alternatives := make([]string, 0, 11)
	for i := range 10 {
		alternatives = append(alternatives, "https://192.168.1."+strconv.Itoa(10+i)+":32400")
	}
	alternatives = append(alternatives, "https://1.1.1.1:443")
	service := &Service{}
	got, err := service.allowedPlexCandidates(t.Context(), 7, plexSecureBaseURLCandidates("https://9.9.9.9:32400", alternatives))
	if err != nil {
		t.Fatalf("allowedPlexCandidates: %v", err)
	}
	if want := []string{"https://9.9.9.9:32400", "https://1.1.1.1:443"}; !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}

	public := make([]string, 0, 12)
	for i := range 12 {
		public = append(public, "https://1.1.1."+strconv.Itoa(i+1)+":443")
	}
	got, err = service.allowedPlexCandidates(t.Context(), 7, plexSecureBaseURLCandidates("", public))
	if err != nil || len(got) != MaxPlexConnectionCandidates || got[0] != public[0] {
		t.Fatalf("candidates = %v (%v), want the first %d public addresses", got, err, MaxPlexConnectionCandidates)
	}
}

// TestPlexRunRefusesBlankAddresses covers addresses that normalize to nothing:
// they must be refused as invalid input, not reach credential construction.
func TestPlexRunRefusesBlankAddresses(t *testing.T) {
	t.Parallel()

	service := &Service{}
	for _, input := range []CreateRunInput{
		{Source: SourceTypePlex, ProfileID: "p", PlexBaseURL: " ", PlexToken: "server-token"},
		{Source: SourceTypePlex, ProfileID: "p", PlexBaseURL: "/", PlexBaseURLs: []string{" ", "//"}, PlexToken: "server-token"},
	} {
		if _, err := service.preparePersonalRun(t.Context(), 7, input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("preparePersonalRun(%q, %q) error = %v, want ErrInvalidInput", input.PlexBaseURL, input.PlexBaseURLs, err)
		}
	}
}
