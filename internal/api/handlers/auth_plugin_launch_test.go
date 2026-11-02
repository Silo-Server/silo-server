package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

func TestPluginLaunchCookieCarriesValidatedProfile(t *testing.T) {
	jwt := auth.NewJWTService("plugin-launch-test-secret", time.Minute, time.Hour)
	accessToken, err := jwt.GenerateAccessToken(7, "user", "session-1")
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}
	handler := NewAuthHandler(nil, jwt, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/plugin-launch", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	ctx := apimw.SetProfileID(req.Context(), "profile-1")
	ctx = access.SetScope(ctx, access.Scope{UserID: 7, ProfileID: "profile-1", ProfileVerified: true, ProfilePINRevision: 3})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.HandlePluginLaunch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	response := rec.Result()
	defer func() { _ = response.Body.Close() }()
	var pluginCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == auth.PluginAccessCookieName {
			pluginCookie = cookie
			break
		}
	}
	if pluginCookie == nil {
		t.Fatal("plugin access cookie was not set")
	}
	claims, err := jwt.ValidateToken(pluginCookie.Value)
	if err != nil {
		t.Fatalf("validating plugin cookie: %v", err)
	}
	if claims.ProfileID != "profile-1" || claims.TokenType != auth.TokenTypePluginAccess {
		t.Fatalf("plugin claims = %#v", claims)
	}
	// The verified profile's PIN revision is bound, so renewal can tell when
	// the PIN changes.
	if claims.ProfilePINRevision == nil || *claims.ProfilePINRevision != 3 {
		t.Fatalf("profile_pin_revision = %v, want 3", claims.ProfilePINRevision)
	}
}

func TestPluginLaunchPreservesProfileOptionalCompatibility(t *testing.T) {
	jwt := auth.NewJWTService("plugin-launch-test-secret", time.Minute, time.Hour)
	accessToken, err := jwt.GenerateAccessToken(7, "user", "session-1")
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}
	handler := NewAuthHandler(nil, jwt, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/plugin-launch", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	handler.HandlePluginLaunch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	response := rec.Result()
	defer func() { _ = response.Body.Close() }()
	var pluginCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == auth.PluginAccessCookieName {
			pluginCookie = cookie
			break
		}
	}
	if pluginCookie == nil {
		t.Fatal("plugin access cookie was not set")
	}
	claims, err := jwt.ValidateToken(pluginCookie.Value)
	if err != nil {
		t.Fatalf("validating plugin cookie: %v", err)
	}
	if claims.ProfileID != "" || claims.TokenType != auth.TokenTypePluginAccess {
		t.Fatalf("plugin claims = %#v", claims)
	}
}
