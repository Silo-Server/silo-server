package abs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLoginEnvelope_HasRequiredKeys marshals the response and asserts every
// field the real ABS iOS client expects is present. Guards against silent
// regressions on the login/authorize envelope shape — iOS degrades to a
// half-broken mode when any of these are missing.
func TestLoginEnvelope_HasRequiredKeys(t *testing.T) {
	h, _, _ := newRefreshTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)

	env := h.loginEnvelope(req, "u1", "Display Name", "access.jwt", "refresh.jwt")
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

	env := h.loginEnvelope(req, "u1", "Display Name", "access.jwt", "refresh.jwt")
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

// TestLoginEnvelope_NoXReturnTokens_OmitsFromUser is the inverse: without the
// header, user object should NOT carry the duplicated tokens (top-level only).
func TestLoginEnvelope_NoXReturnTokens_OmitsFromUser(t *testing.T) {
	h, _, _ := newRefreshTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)

	env := h.loginEnvelope(req, "u1", "Display Name", "access.jwt", "refresh.jwt")
	user, _ := env["user"].(map[string]any)
	if _, present := user["accessToken"]; present {
		t.Errorf("user.accessToken should not be set without x-return-tokens header")
	}
}

// TestLoginEnvelope_DisplayNameFallsBackToUserID covers the empty-name path:
// the validator may return "" for displayName; the envelope must still emit a
// username (falling back to userID).
func TestLoginEnvelope_DisplayNameFallsBackToUserID(t *testing.T) {
	h, _, _ := newRefreshTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)

	env := h.loginEnvelope(req, "user-42", "", "a", "r")
	user, _ := env["user"].(map[string]any)
	if user["username"] != "user-42" {
		t.Errorf("username = %v, want user-42 (fallback)", user["username"])
	}
}

