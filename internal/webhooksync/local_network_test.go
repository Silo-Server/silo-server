package webhooksync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/netguard"
)

type staticSettings map[string]string

func (s staticSettings) Get(_ context.Context, key string) (string, error) { return s[key], nil }

func TestCreatePlexConnectionRefusesLocalAddressWithoutTrust(t *testing.T) {
	svc := &Service{providers: map[string]Provider{ProviderPlex: NewPlexProvider(historyimport.NewPlexClient())}}
	_, err := svc.CreateConnection(t.Context(), 7, CreateConnectionInput{
		Provider: ProviderPlex, ServerID: "server", ServerName: "Plex", BaseURL: "http://192.168.1.10:32400",
		AccessToken: "token", DefaultProfileID: "p",
	}, "")
	if !errors.Is(err, netguard.ErrPrivateDestination) {
		t.Fatalf("error = %v, want ErrPrivateDestination", err)
	}
}

// The Plex provider calls the connection's server; the call goes through
// the guarded client, so only a trusted context reaches a local server.
func TestPlexProviderReachesLocalServerOnlyWhenTrusted(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"Account":[{"id":1,"name":"owner"}]}}`))
	}))
	defer server.Close()
	provider := NewPlexProvider(historyimport.NewPlexClient())
	conn := &Connection{UserID: 7, BaseURL: server.URL, AccessToken: "token"}

	untrusted := historyimport.NewLocalNetworkAccess(staticSettings{}, nil)
	if _, _, err := provider.DiscoverUsers(untrusted.Context(t.Context(), conn.UserID), conn, nil); !errors.Is(err, netguard.ErrPrivateDestination) {
		t.Fatalf("untrusted error = %v, want ErrPrivateDestination", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the refused server received a request")
	}

	trusted := historyimport.NewLocalNetworkAccess(staticSettings{historyimport.SettingAllowPrivateDestinations: "true"}, nil)
	users, _, err := provider.DiscoverUsers(trusted.Context(t.Context(), conn.UserID), conn, nil)
	if err != nil || len(users) != 1 {
		t.Fatalf("trusted discovery = %v, %v", users, err)
	}
}
