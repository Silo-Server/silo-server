package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// fakeNetworkAccess stands in for *plugins.Service: one provider ("stub") on
// the api host whose plugin answers, and an optional second provider whose
// plugin is not running.
type fakeNetworkAccess struct {
	providers   []plugins.NetworkAccessProvider
	unavailable map[string]bool
	listErr     error
	connects    []string
	disconnects []string
	hosts       [][]string
	state       string
}

func newFakeNetworkAccess() *fakeNetworkAccess {
	return &fakeNetworkAccess{
		providers: []plugins.NetworkAccessProvider{
			{InstallationID: 7, CapabilityID: "stub", Provider: "stub", DisplayName: "Stub Overlay"},
			{InstallationID: 9, CapabilityID: "down", Provider: "down", DisplayName: "Down Overlay"},
		},
		unavailable: map[string]bool{"down": true},
		state:       netaccess.StateDisconnected,
	}
}

func (f *fakeNetworkAccess) ListNetworkAccessProviders(context.Context) ([]plugins.NetworkAccessProvider, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.providers, nil
}

func (f *fakeNetworkAccess) report(slug, state string) (plugins.NetworkAccessReport, error) {
	for _, provider := range f.providers {
		if provider.Provider != slug {
			continue
		}
		host := plugins.NetworkAccessHost{ID: plugins.HostScopeAPI, Role: "api", Name: "Living Room"}
		status := netaccess.Status{InstallationID: provider.InstallationID, Provider: slug}
		if f.unavailable[slug] {
			status.State = netaccess.StateUnavailable
			status.Error = "plugin process is backoff: plugin process exited"
		} else {
			status.State = state
			status.Hostname = "silo.overlay.example.test"
			status.Origin = "https://silo.overlay.example.test"
			status.Addresses = []string{"127.0.0.1"}
			status.ProviderVersion = "stub 0.1.0"
			status.UpdatedAt = fixedTime()
			if state == netaccess.StateAwaitingAuthorization {
				status.AuthURL = "https://login.example.test/a/abc"
			}
		}
		return plugins.NetworkAccessReport{Provider: provider, Hosts: []plugins.NetworkAccessHostStatus{{Host: host, Status: status}}}, nil
	}
	return plugins.NetworkAccessReport{}, plugins.ErrNetworkAccessProviderNotFound
}

func (f *fakeNetworkAccess) NetworkAccessStatus(_ context.Context, provider string) (plugins.NetworkAccessReport, error) {
	return f.report(provider, f.state)
}

func (f *fakeNetworkAccess) checkHosts(hosts []string) error {
	f.hosts = append(f.hosts, hosts)
	for _, id := range hosts {
		if id != plugins.HostScopeAPI {
			return plugins.ErrNetworkAccessHostUnknown
		}
	}
	return nil
}

func (f *fakeNetworkAccess) ConnectNetworkAccess(_ context.Context, provider string, hosts []string) (plugins.NetworkAccessReport, error) {
	if _, err := f.report(provider, ""); err != nil {
		return plugins.NetworkAccessReport{}, err
	}
	if err := f.checkHosts(hosts); err != nil {
		return plugins.NetworkAccessReport{}, err
	}
	f.connects = append(f.connects, provider)
	return f.report(provider, netaccess.StateAwaitingAuthorization)
}

func (f *fakeNetworkAccess) DisconnectNetworkAccess(_ context.Context, provider string, hosts []string) (plugins.NetworkAccessReport, error) {
	if _, err := f.report(provider, ""); err != nil {
		return plugins.NetworkAccessReport{}, err
	}
	if err := f.checkHosts(hosts); err != nil {
		return plugins.NetworkAccessReport{}, err
	}
	f.disconnects = append(f.disconnects, provider)
	return f.report(provider, netaccess.StateDisconnected)
}

type networkAccessStatusDoc struct {
	Provider string `json:"provider"`
	Hosts    []struct {
		Host struct {
			ID   string `json:"id"`
			Role string `json:"role"`
			Name string `json:"name"`
		} `json:"host"`
		State           string   `json:"state"`
		Hostname        string   `json:"hostname"`
		Origin          string   `json:"origin"`
		Addresses       []string `json:"addresses"`
		AuthURL         string   `json:"auth_url"`
		Error           string   `json:"error"`
		ProviderVersion string   `json:"provider_version"`
		UpdatedAt       string   `json:"updated_at"`
	} `json:"hosts"`
}

func decodeNetworkAccessStatus(t *testing.T, body string) networkAccessStatusDoc {
	t.Helper()
	var doc networkAccessStatusDoc
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return doc
}

func TestNetworkAccessStatusOfCoercesUnknownState(t *testing.T) {
	base := plugins.NetworkAccessReport{Provider: plugins.NetworkAccessProvider{Provider: "stub"}, Hosts: []plugins.NetworkAccessHostStatus{{
		Host:   plugins.NetworkAccessHost{ID: plugins.HostScopeAPI, Role: "api", Name: "API"},
		Status: netaccess.Status{State: "reconnecting"},
	}}}
	got := networkAccessStatusOf(base)
	if got.Hosts[0].State != "error" || got.Hosts[0].RawState != "reconnecting" {
		t.Fatalf("unknown state = %+v", got.Hosts[0])
	}
	base.Hosts[0].Status.State = netaccess.StateConnected
	got = networkAccessStatusOf(base)
	if got.Hosts[0].State != netaccess.StateConnected || got.Hosts[0].RawState != "" {
		t.Fatalf("known state = %+v", got.Hosts[0])
	}
}

func TestNetworkAccessCapabilities(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := newFakeNetworkAccess()
	deps.NetworkAccess = f
	h := newTestHandler(t, deps)
	path := Prefix + "/network-access/capabilities"

	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)

	rec := do(t, h, http.MethodGet, path, "", bearer(memberToken))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var doc struct {
		State     string `json:"state"`
		Allowed   bool   `json:"allowed"`
		Providers []struct {
			Provider       string `json:"provider"`
			DisplayName    string `json:"display_name"`
			InstallationID string `json:"installation_id"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.State != StateAvailable || !doc.Allowed || len(doc.Providers) != 2 || doc.Providers[0].Provider != "stub" || doc.Providers[0].DisplayName != "Stub Overlay" || doc.Providers[0].InstallationID != "7" {
		t.Fatalf("capabilities = %s", rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("capability document has no ETag")
	}

	// No provider installed: not_configured, providers is an empty array.
	f.providers = nil
	rec = do(t, h, http.MethodGet, path, "", bearer(memberToken))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"not_configured"`) || !strings.Contains(rec.Body.String(), `"providers":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}

	f.listErr = errors.New("db down")
	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypeInternalError)

	// Not wired at all (worker modes): still 200 not_configured.
	deps.NetworkAccess = nil
	rec = do(t, newTestHandler(t, deps), http.MethodGet, path, "", bearer(memberToken))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"not_configured"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestAdminNetworkAccessStatus(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := newFakeNetworkAccess()
	deps.NetworkAccess = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/network-access/stub/status"

	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)

	rec := do(t, h, http.MethodGet, path, "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	doc := decodeNetworkAccessStatus(t, rec.Body.String())
	if doc.Provider != "stub" || len(doc.Hosts) != 1 {
		t.Fatalf("status = %+v", doc)
	}
	host := doc.Hosts[0]
	if host.Host.ID != "api" || host.Host.Role != "api" || host.Host.Name != "Living Room" {
		t.Fatalf("host = %+v", host.Host)
	}
	if host.State != "disconnected" || host.Hostname != "silo.overlay.example.test" || host.Origin != "https://silo.overlay.example.test" || len(host.Addresses) != 1 || host.ProviderVersion != "stub 0.1.0" || host.AuthURL != "" {
		t.Fatalf("host status = %+v", host)
	}
	if host.UpdatedAt != NewInstant(fixedTime()).String() {
		t.Fatalf("updated_at = %q", host.UpdatedAt)
	}

	// A host whose plugin is not running: unavailable with the reason, no
	// updated_at, addresses still an array.
	rec = do(t, h, http.MethodGet, Prefix+"/admin/network-access/down/status", "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	doc = decodeNetworkAccessStatus(t, rec.Body.String())
	if len(doc.Hosts) != 1 || doc.Hosts[0].State != "unavailable" || !strings.Contains(doc.Hosts[0].Error, "backoff") || doc.Hosts[0].UpdatedAt != "" || !strings.Contains(rec.Body.String(), `"addresses":[]`) {
		t.Fatalf("unavailable host = %s", rec.Body.String())
	}

	// Unknown provider is 404; a slug the pattern refuses is 422.
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/network-access/netbird/status", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/network-access/Not%20A%20Slug/status", "", bearer(adminToken)), TypeValidationFailed)

	deps.NetworkAccess = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

func TestAdminNetworkAccessConnectDisconnect(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := newFakeNetworkAccess()
	deps.NetworkAccess = f
	h := newTestHandler(t, deps)
	connect := Prefix + "/admin/network-access/stub/connect"
	disconnect := Prefix + "/admin/network-access/stub/disconnect"

	requireProblem(t, do(t, h, http.MethodPost, connect, "", bearer(memberToken)), TypePermissionDenied)
	if len(f.connects) != 0 {
		t.Fatal("unauthorized connect reached the service")
	}

	// No body: every host. 202 with the state reached and the auth URL.
	rec := do(t, h, http.MethodPost, connect, "", bearer(adminToken))
	if rec.Code != http.StatusAccepted {
		t.Fatal(rec.Code, rec.Body.String())
	}
	doc := decodeNetworkAccessStatus(t, rec.Body.String())
	if len(f.connects) != 1 || f.hosts[0] != nil || len(doc.Hosts) != 1 || doc.Hosts[0].State != "awaiting_authorization" || doc.Hosts[0].AuthURL != "https://login.example.test/a/abc" {
		t.Fatalf("connect: hosts=%v body=%s", f.hosts, rec.Body.String())
	}

	// Named host.
	rec = do(t, h, http.MethodPost, connect, `{"hosts":["api"]}`, bearer(adminToken))
	if rec.Code != http.StatusAccepted || len(f.connects) != 2 || len(f.hosts[1]) != 1 || f.hosts[1][0] != "api" {
		t.Fatal(rec.Code, rec.Body.String(), f.hosts)
	}
	// Empty body object behaves like no body.
	if rec = do(t, h, http.MethodPost, connect, `{}`, bearer(adminToken)); rec.Code != http.StatusAccepted || f.hosts[2] != nil {
		t.Fatal(rec.Code, rec.Body.String(), f.hosts)
	}

	// An unknown host and an empty host list are validation failures that
	// name body.hosts.
	p := requireProblem(t, do(t, h, http.MethodPost, connect, `{"hosts":["node:99"]}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.hosts" {
		t.Fatalf("unknown host problem = %+v", p)
	}
	p = requireProblem(t, do(t, h, http.MethodPost, connect, `{"hosts":[]}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.hosts" {
		t.Fatalf("empty hosts problem = %+v", p)
	}
	requireProblem(t, do(t, h, http.MethodPost, connect, `{"hosts":`, bearer(adminToken)), TypeMalformedRequest)

	// Disconnect mirrors connect.
	rec = do(t, h, http.MethodPost, disconnect, "", bearer(adminToken))
	if rec.Code != http.StatusAccepted || len(f.disconnects) != 1 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if doc = decodeNetworkAccessStatus(t, rec.Body.String()); doc.Hosts[0].State != "disconnected" || doc.Hosts[0].AuthURL != "" {
		t.Fatalf("disconnect = %s", rec.Body.String())
	}

	// A host whose plugin is not running still answers 202: the per-host
	// row says unavailable, the command was acknowledged, not applied.
	rec = do(t, h, http.MethodPost, Prefix+"/admin/network-access/down/connect", "", bearer(adminToken))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"state":"unavailable"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}

	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/admin/network-access/netbird/connect", "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/admin/network-access/netbird/disconnect", "", bearer(adminToken)), TypeNotFound)

	deps.NetworkAccess = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, connect, "", bearer(adminToken)), TypeDependencyUnavailable)
}

// TestNetworkAccessStatusOfDefaultsState: a status the service could not fill
// in (zero value) is rendered unavailable rather than an empty enum value.
func TestNetworkAccessStatusOfDefaultsState(t *testing.T) {
	report := plugins.NetworkAccessReport{
		Provider: plugins.NetworkAccessProvider{Provider: "stub"},
		Hosts:    []plugins.NetworkAccessHostStatus{{Host: plugins.NetworkAccessHost{ID: "api", Role: "api"}}},
	}
	out := networkAccessStatusOf(report)
	if out.Provider != "stub" || len(out.Hosts) != 1 || out.Hosts[0].State != netaccess.StateUnavailable || out.Hosts[0].UpdatedAt != nil || out.Hosts[0].Addresses == nil {
		t.Fatalf("status = %+v", out)
	}
	report.Hosts[0].Status = netaccess.Status{State: netaccess.StateConnected, UpdatedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}
	if out = networkAccessStatusOf(report); out.Hosts[0].UpdatedAt == nil || out.Hosts[0].UpdatedAt.String() != "2026-03-04T05:06:07.000Z" {
		t.Fatalf("updated_at = %v", out.Hosts[0].UpdatedAt)
	}
}

// An explicit null selector must not be read as "every host".
func TestNetworkAccessCommandRejectsExplicitNullHosts(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := newFakeNetworkAccess()
	deps.NetworkAccess = f
	h := newTestHandler(t, deps)
	connect := Prefix + "/admin/network-access/stub/connect"
	p := requireProblem(t, do(t, h, http.MethodPost, connect, `{"hosts":null}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.hosts" {
		t.Fatalf("problem = %+v", p)
	}
	if len(f.connects) != 0 {
		t.Fatal("null hosts reached the service")
	}
}

// A literal null body is malformed, not "omitted": it must not fan out to
// every host.
func TestNetworkAccessCommandRejectsNullBody(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := newFakeNetworkAccess()
	deps.NetworkAccess = f
	h := newTestHandler(t, deps)
	connect := Prefix + "/admin/network-access/stub/connect"
	p := requireProblem(t, do(t, h, http.MethodPost, connect, `null`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body" {
		t.Fatalf("problem = %+v", p)
	}
	if len(f.connects) != 0 {
		t.Fatal("null body reached the service")
	}
	// An omitted body still means every host.
	if rec := do(t, h, http.MethodPost, connect, "", bearer(adminToken)); rec.Code != http.StatusAccepted || len(f.connects) != 1 {
		t.Fatalf("omitted body: %d, connects=%d", rec.Code, len(f.connects))
	}
}
