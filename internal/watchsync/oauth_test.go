package watchsync

import (
	"context"
	"errors"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestOAuthRedirectURIRequiresHTTPS(t *testing.T) {
	service := NewService(nil, NewRegistry())
	if _, err := service.OAuthRedirectURI(); !errors.Is(err, ErrPublicURLRequired) {
		t.Fatalf("missing public URL = %v", err)
	}
	service.SetPublicURL("http://silo.example")
	if _, err := service.OAuthRedirectURI(); !errors.Is(err, ErrPublicURLNotHTTPS) {
		t.Fatalf("http public URL = %v", err)
	}
	service.SetPublicURL("https://silo.example/")
	got, err := service.OAuthRedirectURI()
	if err != nil || got != "https://silo.example"+OAuthCallbackPath {
		t.Fatalf("redirect = %q, %v", got, err)
	}
}

func TestPluginProviderStartsAuthorizationCode(t *testing.T) {
	client := &fakeWatchSyncPluginClient{authorizeResponse: &pluginv1.WatchSyncInitAuthorizeResponse{
		AuthorizationUrl: "https://trakt.tv/oauth/authorize?client_id=app",
		ProviderState:    []byte("verifier"),
	}}
	provider := testPluginProvider(t, client)
	provider.authMethod = AuthMethodAuthorizationCode
	authorizationURL, state, err := provider.StartAuthorization(context.Background(), "https://silo.example/cb", "state-1")
	if err != nil || authorizationURL != "https://trakt.tv/oauth/authorize?client_id=app" || string(state) != "verifier" {
		t.Fatalf("start = %q %q %v", authorizationURL, state, err)
	}
	if client.authorizeRequest.GetRedirectUri() != "https://silo.example/cb" || client.authorizeRequest.GetState() != "state-1" {
		t.Fatalf("request = %v", client.authorizeRequest)
	}
	client.authorizeResponse.AuthorizationUrl = "http://trakt.tv/oauth/authorize"
	if _, _, err := provider.StartAuthorization(context.Background(), "https://silo.example/cb", "state-1"); err == nil {
		t.Fatal("expected an http authorization URL to be rejected")
	}
}
