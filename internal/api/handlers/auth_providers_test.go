package handlers

import (
	"context"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type stubLoginProvider struct{}

func (stubLoginProvider) Authenticate(context.Context, auth.Credentials) (*models.User, error) {
	return nil, auth.ErrInvalidCredentials
}

func (stubLoginProvider) ValidateSession(context.Context, string) (bool, error) {
	return false, nil
}

func newAuthProviderHandlerForTest(oauthRoutesAvailable bool) *AuthHandler {
	service := auth.NewService(nil, nil, nil, nil, nil, nil, nil)
	provider := stubLoginProvider{}

	service.RegisterProvider(auth.LoginProviderInfo{
		ID:          "local",
		DisplayName: "Local",
		Mode:        "credentials",
		Default:     true,
	}, provider)
	service.RegisterProvider(auth.LoginProviderInfo{
		ID:             "plugin:41:oidc",
		DisplayName:    "OIDC",
		Mode:           "oauth",
		InstallationID: 41,
	}, provider)

	handler := NewAuthHandler(service, nil, nil)
	handler.SetOAuthRoutesAvailable(oauthRoutesAvailable)
	return handler
}

// OAuth providers are offered only while the OAuth routes are served.
func TestDiscoverProvidersFollowsOAuthRoutes(t *testing.T) {
	for _, available := range []bool{false, true} {
		discovery, err := newAuthProviderHandlerForTest(available).DiscoverProviders(context.Background())
		if err != nil {
			t.Fatalf("oauth routes %v: %v", available, err)
		}
		var ids []string
		for _, provider := range discovery.Providers {
			ids = append(ids, provider.ID)
			if provider.ID == "plugin:41:oidc" && (provider.Mode != "oauth" || provider.InstallationID != 41) {
				t.Fatalf("OAuth provider = %+v", provider)
			}
		}
		want := []string{"local"}
		if available {
			want = append(want, "plugin:41:oidc")
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("oauth routes %v: providers = %v, want %v", available, ids, want)
		}
	}
}
