package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

// defaultLibraryStubStore serves a fixed library list so the default-library
// tests can control folder order.
type defaultLibraryStubStore struct {
	noopMediaStore
	libs []AudiobookLibrary
}

func (s *defaultLibraryStubStore) ListAudiobookLibraries(context.Context, catalog.AccessFilter) ([]AudiobookLibrary, error) {
	return s.libs, nil
}

// TestDefaultLibraryPrefersAudiobooks pins the resolution every sentinel and
// legacy surface shares. ListAudiobookLibraries returns ebook folders too, so
// "the first library" is not a safe default: an ABS client that browses the
// virtual library, or reads defaultLibraryId off /me, expects audiobooks
// whenever the deployment has any.
func TestDefaultLibraryPrefersAudiobooks(t *testing.T) {
	ebooks := AudiobookLibrary{ID: 10, Name: "Ebooks", Type: libraryTypeEbooks}
	audiobooks := AudiobookLibrary{ID: 20, Name: "Audiobooks", Type: "audiobooks"}

	cases := []struct {
		name   string
		libs   []AudiobookLibrary
		want   AudiobookLibrary
		wantOK bool
	}{
		{"ebook folder sorts first", []AudiobookLibrary{ebooks, audiobooks}, audiobooks, true},
		{"audiobook folder sorts first", []AudiobookLibrary{audiobooks, ebooks}, audiobooks, true},
		{"ebook-only deployment falls back", []AudiobookLibrary{ebooks}, ebooks, true},
		{"no libraries", nil, AudiobookLibrary{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := defaultLibrary(tc.libs)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("defaultLibrary = (%+v, %v), want (%+v, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestHandleMe_DefaultLibraryIDPrefersAudiobooks is the wire-level half: the
// same preference has to reach defaultLibraryId, which is the library an ABS
// client opens on launch.
func TestHandleMe_DefaultLibraryIDPrefersAudiobooks(t *testing.T) {
	h := New(Dependencies{MediaStore: &defaultLibraryStubStore{libs: []AudiobookLibrary{
		{ID: 10, Name: "Ebooks", Type: libraryTypeEbooks},
		{ID: 20, Name: "Audiobooks", Type: "audiobooks"},
	}}})

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: "42"}))
	rec := httptest.NewRecorder()
	h.handleMe(rec, req)

	var user map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if user["defaultLibraryId"] != "20" {
		t.Fatalf("defaultLibraryId = %v, want 20 (the audiobook library)", user["defaultLibraryId"])
	}
}

// TestResolveLibrarySentinelPrefersAudiobooks covers the third caller: the
// "silo-audiobooks" sentinel and a missing {libraryId} both browse whatever
// defaultLibrary picks.
func TestResolveLibrarySentinelPrefersAudiobooks(t *testing.T) {
	h := New(Dependencies{MediaStore: &defaultLibraryStubStore{libs: []AudiobookLibrary{
		{ID: 10, Name: "Ebooks", Type: libraryTypeEbooks},
		{ID: 20, Name: "Audiobooks", Type: "audiobooks"},
	}}})

	req := httptest.NewRequest(http.MethodGet, "/api/libraries/"+VirtualLibraryID, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: "42"}))
	rec := httptest.NewRecorder()
	lib, ok := h.resolveLibrary(rec, req)
	if !ok {
		t.Fatalf("resolveLibrary failed: %s", rec.Body.String())
	}
	if lib.ID != 20 {
		t.Fatalf("sentinel resolved to library %d, want the audiobook library 20", lib.ID)
	}
}
