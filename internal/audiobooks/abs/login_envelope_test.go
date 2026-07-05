package abs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestLoginEnvelope_HasRequiredKeys marshals the response and asserts every
// field the real ABS iOS client expects is present. Guards against silent
// regressions on the login/authorize envelope shape — iOS degrades to a
// half-broken mode when any of these are missing.
func TestLoginEnvelope_HasRequiredKeys(t *testing.T) {
	h, _, _ := newRefreshTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)

	env := h.loginEnvelope(req, time.Now(), "u1", "Display Name", "access.jwt", "refresh.jwt", true)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(body)

	topLevel := []string{
		`"user":`,
		`"userDefaultLibraryId":`,
		`"serverSettings":`,
		`"Source":`,
		`"ereaderDevices":`,
		`"libraries":`,
		`"accessToken":`,
		`"refreshToken":`,
	}
	for _, key := range topLevel {
		if !strings.Contains(js, key) {
			t.Errorf("top-level missing %s", key)
		}
	}

	user, ok := env["user"].(map[string]any)
	if !ok {
		t.Fatalf("user is not a map: %T", env["user"])
	}
	userKeys := []string{
		"id", "username", "type", "defaultLibraryId",
		"librariesAccessible", "itemTagsAccessible", "itemTagsSelected",
		"mediaProgress", "bookmarks", "seriesHideFromContinueListening",
		"isOldToken", "token", "lastSeen", "createdAt", "permissions",
	}
	for _, k := range userKeys {
		if _, present := user[k]; !present {
			t.Errorf("user missing key %q", k)
		}
	}

	perms, ok := user["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("user.permissions is not a map: %T", user["permissions"])
	}
	permKeys := []string{
		"download", "update", "delete", "upload",
		"accessAllLibraries", "accessAllTags", "accessExplicitContent",
		"selectedTagsNotAccessible",
	}
	for _, k := range permKeys {
		if _, present := perms[k]; !present {
			t.Errorf("permissions missing key %q", k)
		}
	}

	settings, ok := env["serverSettings"].(map[string]any)
	if !ok {
		t.Fatalf("serverSettings is not a map: %T", env["serverSettings"])
	}
	settingsKeys := []string{
		"id", "version", "buildNumber", "language", "scannerDisableWatcher",
		"sortingPrefixes", "chromecastEnabled", "authActiveAuthMethods",
	}
	for _, k := range settingsKeys {
		if _, present := settings[k]; !present {
			t.Errorf("serverSettings missing key %q", k)
		}
	}

	if user["token"] != "access.jwt" {
		t.Errorf("user.token = %v, want access.jwt", user["token"])
	}
	if env["accessToken"] != "access.jwt" {
		t.Errorf("accessToken = %v, want access.jwt", env["accessToken"])
	}
	if env["refreshToken"] != "refresh.jwt" {
		t.Errorf("refreshToken = %v, want refresh.jwt", env["refreshToken"])
	}
}

// TestLoginEnvelope_XReturnTokens_SurfacesOnUser covers the opt-in header:
// when x-return-tokens: true is set, the user object MUST include
// accessToken/refreshToken (some clients read from the user envelope rather
// than the top level).
func TestLoginEnvelope_XReturnTokens_SurfacesOnUser(t *testing.T) {
	h, _, _ := newRefreshTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.Header.Set("x-return-tokens", "true")

	env := h.loginEnvelope(req, time.Now(), "u1", "Display Name", "access.jwt", "refresh.jwt", true)
	user, ok := env["user"].(map[string]any)
	if !ok {
		t.Fatalf("user is not a map: %T", env["user"])
	}
	if user["accessToken"] != "access.jwt" {
		t.Errorf("user.accessToken = %v, want access.jwt (x-return-tokens not honored)", user["accessToken"])
	}
	if user["refreshToken"] != "refresh.jwt" {
		t.Errorf("user.refreshToken = %v, want refresh.jwt (x-return-tokens not honored)", user["refreshToken"])
	}
}

// TestLoginEnvelope_NoXReturnTokens covers the default flow: real ABS ALWAYS
// puts accessToken on the user object, and sets refreshToken to null when the
// client did not opt into body delivery via x-return-tokens (the caller then
// ships the refresh token as a cookie).
func TestLoginEnvelope_NoXReturnTokens(t *testing.T) {
	h, _, _ := newRefreshTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)

	env := h.loginEnvelope(req, time.Now(), "u1", "Display Name", "access.jwt", "refresh.jwt", false)
	user, _ := env["user"].(map[string]any)
	if user["accessToken"] != "access.jwt" {
		t.Errorf("user.accessToken = %v, want access.jwt (must always be present)", user["accessToken"])
	}
	if rt, present := user["refreshToken"]; !present || rt != nil {
		t.Errorf("user.refreshToken = %v (present=%v), want nil without x-return-tokens", rt, present)
	}
}

// TestLoginEnvelope_DisplayNameFallsBackToUserID covers the empty-name path:
// the validator may return "" for displayName; the envelope must still emit a
// username (falling back to userID).
func TestLoginEnvelope_DisplayNameFallsBackToUserID(t *testing.T) {
	h, _, _ := newRefreshTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)

	env := h.loginEnvelope(req, time.Now(), "user-42", "", "a", "r", false)
	user, _ := env["user"].(map[string]any)
	if user["username"] != "user-42" {
		t.Errorf("username = %v, want user-42 (fallback)", user["username"])
	}
}
