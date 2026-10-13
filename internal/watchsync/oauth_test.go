package watchsync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestOAuthRedirectURIIgnoresQueryAndFragment(t *testing.T) {
	service := NewService(nil, NewRegistry())
	service.SetPublicURL("https://silo.example/base?source=web#home")
	got, err := service.OAuthRedirectURI()
	if err != nil {
		t.Fatal(err)
	}
	want := "https://silo.example/base" + OAuthCallbackPath
	if got != want {
		t.Fatalf("redirect = %q, want %q", got, want)
	}
	if strings.Contains(got, "?") || strings.Contains(got, "#") {
		t.Fatalf("redirect kept query or fragment: %q", got)
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

func TestFinishOAuthRequiresBinderAndClaimsOnce(t *testing.T) {
	repo := newServiceFakeRepo()
	registry := NewRegistry()
	provider := &oauthAuthProviderStub{
		authURL: "https://trakt.tv/oauth/authorize?client_id=app",
		state:   []byte("verifier"),
		tokens:  TokenSet{AccessToken: "access", RefreshToken: "refresh"},
		account: ProviderAccount{ID: "acct", Username: "user"},
	}
	registry.Register(provider)
	service := NewService(repo, registry)
	service.SetPublicURL("https://silo.example")

	started, err := service.StartOAuth(context.Background(), 7, "profile-1", provider.Key())
	if err != nil {
		t.Fatal(err)
	}
	if started.BinderCookie.Name == "" || started.BinderCookie.Value == "" || !started.BinderCookie.HttpOnly {
		t.Fatalf("binder cookie = %#v", started.BinderCookie)
	}
	sessionID := strings.TrimPrefix(started.BinderCookie.Name, oauthBinderCookiePrefix)
	_ = sessionID
	var stored DeviceAuthSession
	for _, session := range repo.sessions {
		stored = session
	}
	if stored.VerificationURL != started.RedirectURI {
		t.Fatalf("stored redirect = %q, want %q", stored.VerificationURL, started.RedirectURI)
	}
	if _, err := service.FinishOAuth(context.Background(), stored.ID, "code-1", ""); !errors.Is(err, ErrAuthSessionMismatch) {
		t.Fatalf("missing binder = %v", err)
	}
	providerKey, err := service.FinishOAuth(context.Background(), stored.ID, "code-1", started.BinderCookie.Value)
	if err != nil || providerKey != provider.Key() {
		t.Fatalf("finish = %q %v", providerKey, err)
	}
	if _, err := service.FinishOAuth(context.Background(), stored.ID, "code-2", started.BinderCookie.Value); !errors.Is(err, ErrAuthSessionCompleted) {
		t.Fatalf("second finish = %v", err)
	}
	if provider.exchangeRedirect != started.RedirectURI {
		t.Fatalf("exchange redirect = %q, want %q", provider.exchangeRedirect, started.RedirectURI)
	}
}

func TestClaimAuthSessionRejectsExpired(t *testing.T) {
	repo := newServiceFakeRepo()
	expired := time.Now().Add(-time.Minute)
	repo.sessions["s1"] = DeviceAuthSession{
		ID:        "s1",
		Provider:  "trakt",
		UserCode:  oauthSessionUserCodePrefix + "hash",
		ExpiresAt: expired,
	}
	if _, err := repo.ClaimAuthSession(context.Background(), "s1", time.Now()); !errors.Is(err, ErrAuthSessionExpired) {
		t.Fatalf("claim expired = %v", err)
	}
}

type oauthAuthProviderStub struct {
	authURL          string
	state            []byte
	tokens           TokenSet
	account          ProviderAccount
	exchangeRedirect string
}

func (p *oauthAuthProviderStub) Key() string         { return "trakt" }
func (p *oauthAuthProviderStub) DisplayName() string { return "Trakt" }
func (p *oauthAuthProviderStub) AuthMethod() string  { return AuthMethodAuthorizationCode }
func (p *oauthAuthProviderStub) Capabilities() Capabilities {
	return Capabilities{}
}
func (p *oauthAuthProviderStub) StartAuthorization(context.Context, string, string) (string, []byte, error) {
	return p.authURL, p.state, nil
}
func (p *oauthAuthProviderStub) ExchangeAuthorizationCode(_ context.Context, redirectURI, _ string, _ []byte) (TokenSet, error) {
	p.exchangeRedirect = redirectURI
	return p.tokens, nil
}
func (p *oauthAuthProviderStub) LookupAccount(context.Context, ServerConfig, Connection) (ProviderAccount, error) {
	return p.account, nil
}
func (p *oauthAuthProviderStub) StartDeviceAuth(context.Context, ServerConfig) (DeviceAuthSession, error) {
	return DeviceAuthSession{}, errors.New("unused")
}
func (p *oauthAuthProviderStub) PollDeviceAuth(context.Context, ServerConfig, DeviceAuthSession) (TokenSet, error) {
	return TokenSet{}, errors.New("unused")
}
func (p *oauthAuthProviderStub) RefreshToken(context.Context, ServerConfig, Connection) (TokenSet, error) {
	return TokenSet{}, errors.New("unused")
}
