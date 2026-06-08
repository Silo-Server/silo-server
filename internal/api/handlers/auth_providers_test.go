package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func readProviderResponse(t *testing.T, handler *AuthHandler) []authProviderResponse {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/providers", nil)
	handler.HandleProviders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var providers []authProviderResponse
	if err := json.NewDecoder(rec.Body).Decode(&providers); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return providers
}

func TestAuthProvidersHideOAuthWhenOAuthRoutesUnavailable(t *testing.T) {
	providers := readProviderResponse(t, newAuthProviderHandlerForTest(false))

	if len(providers) != 1 {
		t.Fatalf("provider count = %d, want 1: %#v", len(providers), providers)
	}
	if providers[0].ID != "local" {
		t.Fatalf("provider ID = %q, want local", providers[0].ID)
	}
}

func TestAuthProvidersIncludeOAuthWhenOAuthRoutesAvailable(t *testing.T) {
	providers := readProviderResponse(t, newAuthProviderHandlerForTest(true))

	if len(providers) != 2 {
		t.Fatalf("provider count = %d, want 2: %#v", len(providers), providers)
	}

	var foundOAuth bool
	for _, provider := range providers {
		if provider.ID == "plugin:41:oidc" {
			foundOAuth = true
			if provider.Mode != "oauth" {
				t.Fatalf("OAuth provider mode = %q, want oauth", provider.Mode)
			}
			if provider.InstallationID != 41 {
				t.Fatalf("OAuth provider installation ID = %d, want 41", provider.InstallationID)
			}
		}
	}
	if !foundOAuth {
		t.Fatalf("OAuth provider missing from response: %#v", providers)
	}
}
