package jellycompat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleFilters2Stub verifies /Items/Filters2 returns 200 with Jellyfin's
// v2 QueryFilters shape (every field an array, never null).
func TestHandleFilters2Stub(t *testing.T) {
	h := &ItemsHandler{}
	req := httptest.NewRequest(http.MethodGet, "/Items/Filters2?IncludeItemTypes=Movie&Recursive=true", nil)
	rec := httptest.NewRecorder()

	h.HandleFilters2Stub(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, field := range []string{`"Genres":[]`, `"Tags":[]`, `"AudioLanguages":[]`, `"SubtitleLanguages":[]`} {
		if !strings.Contains(body, field) {
			t.Errorf("body missing %s: %s", field, body)
		}
	}
	// Must not be the legacy QueryFiltersLegacy shape.
	if strings.Contains(body, "OfficialRatings") || strings.Contains(body, "Years") {
		t.Errorf("Filters2 returned legacy v1 fields: %s", body)
	}
	var dto queryFiltersDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("response is not valid QueryFilters JSON: %v", err)
	}
}

// TestHandleLocalTrailers verifies the endpoint returns a bare empty array (not
// the {Items,...} envelope) for an authenticated request, and 401 without a
// session.
func TestHandleLocalTrailers(t *testing.T) {
	h := &ItemsHandler{}

	t.Run("authenticated returns empty array", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/Items/abc/LocalTrailers", nil)
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{}))
		rec := httptest.NewRecorder()

		h.HandleLocalTrailers(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
			t.Fatalf("body = %q, want bare empty array %q", got, "[]")
		}
	})

	t.Run("missing session returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/Items/abc/LocalTrailers", nil)
		rec := httptest.NewRecorder()

		h.HandleLocalTrailers(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}

// TestHandleClientLogDocument verifies the upload endpoint drains the body and
// answers 200 with a FileName (Jellyfin's contract), and rejects oversized
// uploads with 413.
func TestHandleClientLogDocument(t *testing.T) {
	t.Run("accepts and returns FileName", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/ClientLog/Document",
			bytes.NewBufferString("type: crash_report\nclient: test\n"))
		rec := httptest.NewRecorder()

		HandleClientLogDocument(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var resp clientLogDocumentResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid response JSON: %v", err)
		}
		if resp.FileName == "" {
			t.Fatalf("FileName empty; clients parse it from the 200 body")
		}
	})

	t.Run("rejects oversized upload with 413", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/ClientLog/Document", strings.NewReader("x"))
		req.ContentLength = maxClientLogBytes + 1
		rec := httptest.NewRecorder()

		HandleClientLogDocument(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
	})
}

// TestHandleSessions verifies GET /Sessions returns a 200 JSON array (not the
// chi 404 that broke client session polling).
func TestHandleSessions(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/Sessions?deviceId=abc123", nil)
	rec := httptest.NewRecorder()

	HandleSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("body = %q, want JSON array %q", got, "[]")
	}
	var sessions []sessionInfoDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("response is not a SessionInfoDto array: %v", err)
	}
}

// TestHandleUserImageQueryFallback verifies /UserImage?userId= resolves the id
// from the query string (the path param is empty on this route) and serves the
// same deterministic palette avatar as the legacy path form — not the empty-id
// avatar every caller would otherwise share.
func TestHandleUserImageQueryFallback(t *testing.T) {
	h := &ImagesHandler{}
	const userID = "45def085-5dd8-5ad7-b972-9c7a499fa846"

	req := httptest.NewRequest(http.MethodGet, "/UserImage?userId="+userID, nil)
	rec := httptest.NewRecorder()

	h.HandleUserImage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	want := avatarPalette[avatarPaletteIndex(userID)]
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("served avatar does not match the userId-derived palette entry; query fallback not applied")
	}
	// Guard against regression to the empty-id avatar.
	if empty := avatarPalette[avatarPaletteIndex("")]; bytes.Equal(want, empty) {
		t.Skip("palette collision: userID and empty id hash to the same entry; pick another userID")
	} else if bytes.Equal(rec.Body.Bytes(), empty) {
		t.Fatalf("served the empty-id avatar; query fallback not applied")
	}
}
