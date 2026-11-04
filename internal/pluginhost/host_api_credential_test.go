package pluginhost_test

import (
	"context"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// credentialStore holds one key per installation and records which
// installations were asked for.
type credentialStore struct {
	keys  map[int]pluginhost.HostAPICredential
	asked []int
}

func (s *credentialStore) HostAPICredential(_ context.Context, installationID int) (pluginhost.HostAPICredential, error) {
	s.asked = append(s.asked, installationID)
	credential, ok := s.keys[installationID]
	if !ok {
		return pluginhost.HostAPICredential{}, fmt.Errorf("%w: an administrator hasn't approved this plugin's host API scopes", pluginhost.ErrHostAPINotGranted)
	}
	return credential, nil
}

// TestGetHostAPICredentialAnswersOnlyForTheCallingInstallation covers the
// RPC's authority: the installation is the one the host bound the process to,
// so a plugin only ever reads its own key; one with nothing approved, or a
// test run, gets FAILED_PRECONDITION; a host that issues no keys answers
// UNIMPLEMENTED.
func TestGetHostAPICredentialAnswersOnlyForTheCallingInstallation(t *testing.T) {
	store := &credentialStore{keys: map[int]pluginhost.HostAPICredential{
		5: {Token: "sa_five", Scopes: []string{"admin:monitor:read"}},
		6: {Token: "sa_six", Scopes: []string{"admin:users"}},
	}}
	hostInfo := func(context.Context) (pluginhost.HostInfo, error) {
		return pluginhost.HostInfo{Listeners: []pluginhost.HostListener{{Name: pluginhost.ListenerAPI, Address: "127.0.0.1:8080"}}}, nil
	}
	call := func(installationID int, source pluginhost.HostAPICredentialSource) (*pluginv1.GetHostAPICredentialResponse, error) {
		srv := pluginhost.NewRuntimeHostServerWithOptions(pluginhost.RuntimeHostOptions{
			PluginID: "silo.test", InstallationID: installationID, HostInfo: hostInfo, HostAPICredentials: source,
		})
		return srv.GetHostAPICredential(context.Background(), &pluginv1.GetHostAPICredentialRequest{})
	}

	resp, err := call(5, store)
	if err != nil {
		t.Fatalf("installation 5: %v", err)
	}
	if resp.GetToken() != "sa_five" || len(resp.GetScopes()) != 1 || resp.GetScopes()[0] != "admin:monitor:read" || resp.GetBaseUrl() != "http://127.0.0.1:8080" {
		t.Fatalf("installation 5 response = %+v", resp)
	}

	for _, tc := range []struct {
		name           string
		installationID int
		source         pluginhost.HostAPICredentialSource
		want           codes.Code
	}{
		{"nothing approved", 7, store, codes.FailedPrecondition},
		{"test run of a plugin", -3, store, codes.FailedPrecondition},
		{"host without the dependency", 5, nil, codes.Unimplemented},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := call(tc.installationID, tc.source)
			if status.Code(err) != tc.want {
				t.Fatalf("code = %v (%v), want %v", status.Code(err), err, tc.want)
			}
			if resp.GetToken() != "" {
				t.Fatalf("a refused call returned a token")
			}
		})
	}

	for _, asked := range store.asked {
		if asked != 5 && asked != 7 {
			t.Fatalf("the source was asked for installation %d; only the bound installations may be looked up", asked)
		}
	}
}
