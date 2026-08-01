package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	req = req.WithContext(apimw.SetProfileID(req.Context(), "profile-1"))
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
