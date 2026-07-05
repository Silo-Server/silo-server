package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleMe_ReturnsFullUserObject guards that GET /me emits the full ABS
// user object (audiobookshelf User.toOldJSONForBrowser), not a thin
// {id,username,defaultLibraryId} map — a strict client decodes /me with its
// User model and crashes on any missing required key.
func TestHandleMe_ReturnsFullUserObject(t *testing.T) {
	h := New(Dependencies{MediaStore: noopMediaStore{}})

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, ctxAuth{
		UserID: "42",
		Token:  "bearer.jwt",
	}))
	rec := httptest.NewRecorder()
	h.handleMe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var user map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Full toOldJSONForBrowser key set.
	want := []string{
		"id", "username", "email", "type", "token", "isOldToken",
		"mediaProgress", "seriesHideFromContinueListening", "bookmarks",
		"isActive", "isLocked", "lastSeen", "createdAt", "permissions",
		"librariesAccessible", "itemTagsSelected", "hasOpenIDLink",
	}
	for _, k := range want {
		if _, ok := user[k]; !ok {
			t.Errorf("/me user missing key %q", k)
		}
	}
	if user["token"] != "bearer.jwt" {
		t.Errorf("token = %v, want bearer.jwt (presented bearer)", user["token"])
	}
	// /me must NOT carry login-only token pair.
	if _, ok := user["accessToken"]; ok {
		t.Errorf("/me should not carry accessToken")
	}
	if _, ok := user["refreshToken"]; ok {
		t.Errorf("/me should not carry refreshToken")
	}
}
