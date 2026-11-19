package apiv2

import (
	"context"

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
