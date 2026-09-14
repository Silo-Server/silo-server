package netaccess

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareValidTokenSetsPathAndStripsHeader(t *testing.T) {
	registry := NewRegistry()
	token, err := registry.Issue(7, "tailscale")
	if err != nil {
		t.Fatal(err)
	}
	var (
		gotPath   Path
		gotHeader []string
	)
	handler := Middleware(registry)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = PathFromContext(r.Context())
		gotHeader = r.Header.Values(IngressTokenHeader)
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v2/system", nil)
	req.Header.Set(IngressTokenHeader, token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if gotPath.Provider != "tailscale" || gotPath.IsDefault() {
		t.Fatalf("path = %+v, want provider tailscale", gotPath)
	}
	if len(gotHeader) != 0 {
		t.Fatalf("ingress header reached the handler: %v", gotHeader)
	}
}

func TestMiddlewareUnknownTokenIsForbidden(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Issue(7, "tailscale"); err != nil {
		t.Fatal(err)
	}
	called := false
	handler := Middleware(registry)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	for name, token := range map[string]string{
		"unknown": "not-a-token",
		"empty":   "",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(IngressTokenHeader, token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			if called {
				t.Fatal("handler ran for a rejected token")
			}
		})
	}
	t.Run("duplicate header", func(t *testing.T) {
		token, _ := registry.Issue(7, "tailscale")
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Add(IngressTokenHeader, token)
		req.Header.Add(IngressTokenHeader, token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || called {
			t.Fatalf("duplicate header: status=%d called=%v", rec.Code, called)
		}
	})
	t.Run("revoked", func(t *testing.T) {
		token, _ := registry.Issue(7, "tailscale")
		if !registry.Revoke(7, token) {
			t.Fatal("current token not revoked")
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(IngressTokenHeader, token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || called {
			t.Fatalf("revoked token: status=%d called=%v", rec.Code, called)
		}
	})
}

func TestMiddlewareMissingHeaderKeepsDefaultPath(t *testing.T) {
	for name, registry := range map[string]*Registry{"registry": NewRegistry(), "nil registry": nil} {
		t.Run(name, func(t *testing.T) {
			var gotPath Path
			handler := Middleware(registry)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = PathFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !gotPath.IsDefault() {
				t.Fatalf("path = %+v, want default", gotPath)
			}
		})
	}
}

func TestRegistryIssueRotatesAndRejectsInvalidInput(t *testing.T) {
	registry := NewRegistry()
	first, err := registry.Issue(3, "tailscale")
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Issue(3, "tailscale")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("reissued token did not rotate")
	}
	if _, ok := registry.Lookup(first); ok {
		t.Fatal("stale token still resolves")
	}
	if registry.Revoke(3, first) {
		t.Fatal("stale token revoked the current one")
	}
	if _, ok := registry.Lookup(second); !ok {
		t.Fatal("current token lost to a stale revoke")
	}
	if ingress, ok := registry.Lookup(second); !ok || ingress.InstallationID != 3 || ingress.Provider != "tailscale" {
		t.Fatalf("lookup = %+v, %v", ingress, ok)
	}
	if got, ok := registry.IngressToken(3); !ok || got != second {
		t.Fatalf("IngressToken = %q, %v", got, ok)
	}
	if _, err := registry.Issue(-1, "tailscale"); err == nil {
		t.Fatal("negative installation id accepted")
	}
	if _, err := registry.Issue(4, ""); err == nil {
		t.Fatal("empty provider accepted")
	}
}

func TestStatusCacheConnectedOrigins(t *testing.T) {
	cache := NewStatusCache()
	if _, changed := cache.Report(Status{InstallationID: 1, Provider: "tailscale", State: StateConnecting}); !changed {
		t.Fatal("first report not a transition")
	}
	if got := cache.ConnectedOrigins(); len(got) != 0 {
		t.Fatalf("connecting provider leaked origins: %v", got)
	}
	previous, changed := cache.Report(Status{
		InstallationID: 1, Provider: "tailscale", State: StateConnected,
		Origin:    "https://Silo.tail1234.ts.net/",
		Listeners: []Listener{{Name: "api", Origin: "https://silo.tail1234.ts.net"}, {Name: "jellyfin", Origin: "https://silo.tail1234.ts.net:8096"}},
	})
	if !changed || previous.State != StateConnecting {
		t.Fatalf("transition: previous=%+v changed=%v", previous, changed)
	}
	cache.Report(Status{InstallationID: 2, Provider: "netbird", State: StateError, Origin: "https://broken.example"})
	got := cache.ConnectedOrigins()
	want := []string{"https://silo.tail1234.ts.net", "https://silo.tail1234.ts.net:8096"}
	if len(got) != len(want) {
		t.Fatalf("origins = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("origins = %v, want %v", got, want)
		}
	}
	cache.Forget(1)
	if got := cache.ConnectedOrigins(); len(got) != 0 {
		t.Fatalf("forgotten provider still listed: %v", got)
	}
}

func TestNodeNetworkAccessConnectedOrigin(t *testing.T) {
	report := NodeNetworkAccess{
		"tailscale": {State: StateConnected, Origin: "https://Proxy-1.tail1234.ts.net/"},
		"netbird":   {State: StateConnecting, Origin: "https://proxy-1.netbird.example"},
		"broken":    {State: StateConnected, Origin: "not a url"},
	}
	if got, ok := report.ConnectedOrigin("tailscale"); !ok || got != "https://proxy-1.tail1234.ts.net" {
		t.Fatalf("tailscale origin = %q, %v", got, ok)
	}
	for _, provider := range []string{"netbird", "broken", "missing", ""} {
		if got, ok := report.ConnectedOrigin(provider); ok || got != "" {
			t.Fatalf("%q origin = %q, %v; want none", provider, got, ok)
		}
	}
	if got, ok := NodeNetworkAccess(nil).ConnectedOrigin("tailscale"); ok || got != "" {
		t.Fatalf("nil report origin = %q, %v", got, ok)
	}
}

func TestNodeNetworkAccessNormalized(t *testing.T) {
	if got := (NodeNetworkAccess{"  ": {State: StateConnected}}).Normalized(); got != nil {
		t.Fatalf("blank-only report normalized to %v, want nil", got)
	}
	got := NodeNetworkAccess{" tailscale ": {State: " connected ", Origin: " https://a.example ", Hostname: " a "}}.Normalized()
	want := NodeNetworkAccess{"tailscale": {State: StateConnected, Origin: "https://a.example", Hostname: "a"}}
	if len(got) != 1 || got["tailscale"] != want["tailscale"] {
		t.Fatalf("normalized = %#v, want %#v", got, want)
	}
}

func TestStatusCacheNodeNetworkAccess(t *testing.T) {
	cache := NewStatusCache()
	if got := cache.NodeNetworkAccess(); got != nil {
		t.Fatalf("empty cache reported %v", got)
	}
	cache.Report(Status{InstallationID: 1, Provider: "tailscale", State: StateConnected, Origin: "https://p.tail.ts.net", Hostname: "p.tail.ts.net", AuthURL: "https://login.example/secret"})
	cache.Report(Status{InstallationID: 2, Provider: "netbird", State: StateAwaitingAuthorization})
	got := cache.NodeNetworkAccess()
	if len(got) != 2 {
		t.Fatalf("report = %#v, want two providers", got)
	}
	ts := got["tailscale"]
	if ts.State != StateConnected || ts.Origin != "https://p.tail.ts.net" || ts.Hostname != "p.tail.ts.net" || ts.UpdatedAt.IsZero() {
		t.Fatalf("tailscale entry = %#v", ts)
	}
	if got["netbird"].State != StateAwaitingAuthorization || got["netbird"].Origin != "" {
		t.Fatalf("netbird entry = %#v", got["netbird"])
	}
	if origin, ok := got.ConnectedOrigin("netbird"); ok || origin != "" {
		t.Fatal("an unauthorized provider yielded an origin")
	}
}

func TestBrokerRevokeOfOldTokenKeepsReplacementStatus(t *testing.T) {
	b := NewBroker()
	old, err := b.Issue(7, "stub")
	if err != nil {
		t.Fatal(err)
	}
	b.Report(Status{InstallationID: 7, Provider: "stub", State: StateConnected, Origin: "https://old.example.test"})
	// The replacement process is issued its token and pushes its status
	// before the old process's revoke runs.
	fresh, err := b.Issue(7, "stub")
	if err != nil {
		t.Fatal(err)
	}
	b.Report(Status{InstallationID: 7, Provider: "stub", State: StateConnected, Origin: "https://new.example.test"})
	b.Revoke(7, old)
	if got, ok := b.Status.Get(7); !ok || got.Origin != "https://new.example.test" {
		t.Fatalf("stale revoke cleared the replacement's status: %+v %v", got, ok)
	}
	if tok, ok := b.IngressToken(7); !ok || tok != fresh {
		t.Fatalf("stale revoke removed the replacement's token")
	}
	b.Revoke(7, fresh)
	if _, ok := b.Status.Get(7); ok {
		t.Fatal("current revoke left the status behind")
	}
}

func TestStatusCacheNodeNetworkAccessLowestInstallationOwnsDuplicateSlug(t *testing.T) {
	c := NewStatusCache()
	c.Report(Status{InstallationID: 9, Provider: "tailscale", State: StateConnected, Origin: "https://nine.example.test"})
	c.Report(Status{InstallationID: 4, Provider: "tailscale", State: StateConnected, Origin: "https://four.example.test"})
	for i := 0; i < 20; i++ {
		if got := c.NodeNetworkAccess()["tailscale"].Origin; got != "https://four.example.test" {
			t.Fatalf("iteration %d: origin = %q, want the lowest installation's", i, got)
		}
	}
}
