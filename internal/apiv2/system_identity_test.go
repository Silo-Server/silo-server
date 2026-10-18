package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
)

type fakeServerIdentity struct {
	id  string
	err error
}

func (f fakeServerIdentity) ServerID(context.Context) (string, error) { return f.id, f.err }

const fixtureServerID = "3f2a9d5e-6b1c-4c7e-9a0d-2f4b8c1e7a35"

type connectionsDoc struct {
	State      string `json:"state"`
	Allowed    bool   `json:"allowed"`
	ServerID   string `json:"server_id"`
	Current    ServerAccessPath
	Endpoints  []ServerEndpoint  `json:"endpoints"`
	LocalRoute *ServerLocalRoute `json:"local_route"`
}

func decodeConnections(t *testing.T, rec *httptest.ResponseRecorder) connectionsDoc {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var doc connectionsDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestGetServerIdentity(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.ServerIdentity = fakeServerIdentity{id: fixtureServerID}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, Prefix+"/system/identity", "", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"server_id":"`+fixtureServerID+`"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	// Public: credentials and profile headers change nothing, and the
	// document links to it from discovery.
	rec = do(t, h, http.MethodGet, Prefix+"/system/identity", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, Prefix+"/system/info", "", nil)
	var info struct {
		Links struct {
			Identity string `json:"identity"`
		} `json:"links"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || info.Links.Identity != Prefix+"/system/identity" {
		t.Fatalf("links.identity = %q err = %v", info.Links.Identity, err)
	}
}

func TestGetServerIdentityFailures(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.ServerIdentity = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/system/identity", "", nil), TypeDependencyUnavailable)
	identity := &fakeServerIdentity{err: serveridentity.ErrUnavailable}
	deps.ServerIdentity = identity
	h := newTestHandler(t, deps)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/system/identity", "", nil), TypeDependencyUnavailable)
	identity.err = errors.New("db down")
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/system/identity", "", nil), TypeInternalError)
	// The connections document depends on the same identity.
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/system/connections", "", bearer(memberToken)), TypeInternalError)
}

func TestGetServerConnections(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.ServerIdentity = fakeServerIdentity{id: fixtureServerID}
	deps.NetworkAccess = newFakeNetworkAccess()
	cache := netaccess.NewStatusCache()
	cache.Report(netaccess.Status{InstallationID: 7, Provider: "stub", State: netaccess.StateConnected, Origin: "HTTPS://Silo.Overlay.Example.Test/"})
	publicURL := "https://silo.example.test/"
	localURL := "HTTP://192.168.1.20:8080/"
	const pin = "n4bQgYhMfWWaL+qgxVrQFaO/TxsrC4Is0V1sFbDwCgg="
	deps.ServerConnections = ServerConnections{
		PublicURL:   func() string { return publicURL },
		LocalURL:    func() string { return localURL },
		Providers:   cache,
		LocalTLSPin: pin,
	}
	h := newTestHandler(t, deps)
	path := Prefix + "/system/connections"

	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)

	rec := do(t, h, http.MethodGet, path, "", bearer(memberToken))
	doc := decodeConnections(t, rec)
	if doc.State != StateAvailable || !doc.Allowed || doc.ServerID != fixtureServerID {
		t.Fatalf("head = %+v", doc)
	}
	if doc.Current.Kind != AccessPathDefault || doc.Current.Provider != "" {
		t.Fatalf("current = %+v", doc.Current)
	}
	if doc.LocalRoute == nil || *doc.LocalRoute != (ServerLocalRoute{SPKISHA256: pin, URL: "http://192.168.1.20:8080"}) {
		t.Fatalf("local_route = %+v", doc.LocalRoute)
	}
	want := []ServerEndpoint{
		{Kind: EndpointKindPublic, URL: "https://silo.example.test"},
		{Kind: EndpointKindProvider, Provider: "down", DisplayName: "Down Overlay", State: netaccess.StateUnavailable},
		{Kind: EndpointKindProvider, Provider: "stub", DisplayName: "Stub Overlay", State: netaccess.StateConnected, URL: "https://silo.overlay.example.test"},
	}
	if len(doc.Endpoints) != len(want) {
		t.Fatalf("endpoints = %s", rec.Body.String())
	}
	for i := range want {
		if doc.Endpoints[i] != want[i] {
			t.Fatalf("endpoint %d = %+v, want %+v", i, doc.Endpoints[i], want[i])
		}
	}
	if rec.Header().Get("ETag") == "" || rec.Header().Get("Cache-Control") != cachePrivateNoCache {
		t.Fatalf("headers = %v", rec.Header())
	}
	etag := rec.Header().Get("ETag")
	if rec := do(t, h, http.MethodGet, path, "", with(bearer(memberToken), "If-None-Match", etag)); rec.Code != http.StatusNotModified {
		t.Fatalf("revalidation = %d", rec.Code)
	}

	// A provider that disconnects loses its url but stays listed; the
	// revision moves with it.
	cache.Report(netaccess.Status{InstallationID: 7, Provider: "stub", State: netaccess.StateDisconnected, Origin: "https://silo.overlay.example.test"})
	rec = do(t, h, http.MethodGet, path, "", with(bearer(memberToken), "If-None-Match", etag))
	doc = decodeConnections(t, rec)
	if doc.Endpoints[2].URL != "" || doc.Endpoints[2].State != netaccess.StateDisconnected {
		t.Fatalf("disconnected endpoint = %+v", doc.Endpoints[2])
	}
	// A provider state outside the published vocabulary is coerced to error.
	cache.Report(netaccess.Status{InstallationID: 7, Provider: "stub", State: "rebooting", Origin: "https://silo.overlay.example.test"})
	if doc = decodeConnections(t, do(t, h, http.MethodGet, path, "", bearer(memberToken))); doc.Endpoints[2].State != netaccess.StateError {
		t.Fatalf("coerced state = %+v", doc.Endpoints[2])
	}

	// No public or local URL: the endpoint and the local url are absent,
	// never manufactured. An unparsable one is treated the same. The pin
	// stays: LAN discovery still supplies local addresses.
	for _, raw := range []string{"", "not a url", "ftp://silo.example.test"} {
		publicURL, localURL = raw, raw
		doc = decodeConnections(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)))
		if len(doc.Endpoints) != 2 || doc.Endpoints[0].Kind != EndpointKindProvider {
			t.Fatalf("url=%q endpoints = %+v", raw, doc.Endpoints)
		}
		if doc.LocalRoute == nil || doc.LocalRoute.URL != "" || doc.LocalRoute.SPKISHA256 != pin {
			t.Fatalf("url=%q local_route = %+v", raw, doc.LocalRoute)
		}
	}
}

func TestGetServerConnectionsCurrentPath(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.ServerIdentity = fakeServerIdentity{id: fixtureServerID}
	h := newTestHandler(t, deps)
	r := httptest.NewRequest(http.MethodGet, Prefix+"/system/connections", nil)
	r.Header.Set("Authorization", "Bearer "+memberToken)
	r = r.WithContext(netaccess.WithPath(r.Context(), netaccess.Path{Provider: "tailscale"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	doc := decodeConnections(t, rec)
	if doc.Current.Kind != EndpointKindProvider || doc.Current.Provider != "tailscale" {
		t.Fatalf("current = %+v", doc.Current)
	}
	// Nothing wired beyond identity: an empty, still-available document
	// with no local route.
	if doc.State != StateAvailable || len(doc.Endpoints) != 0 || doc.LocalRoute != nil {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestGetServerConnectionsProviderListFailure(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.ServerIdentity = fakeServerIdentity{id: fixtureServerID}
	f := newFakeNetworkAccess()
	f.listErr = errors.New("manifest store down")
	deps.NetworkAccess = f
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/system/connections", "", bearer(memberToken)), TypeInternalError)
}
