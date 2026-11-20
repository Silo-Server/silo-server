package apiv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestGetProfileSectionBaselineRoute(t *testing.T) {
	t.Run("returns deterministic HMAC tokens matching visible sections", func(t *testing.T) {
		deps := fixtureDeps()
		h := newTestHandler(t, deps)

		req := httptest.NewRequest(http.MethodGet, "/api/v2/profile/sections/baseline", nil)
		req.Header.Set("Authorization", "Bearer "+memberToken)
		req.Header.Set("X-Profile-Id", "p-owner")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}

		var got ProfileSectionBaseline
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(got.SectionTokens) != 2 {
			t.Fatalf("section_tokens = %v, want 2 tokens (enabled + disabled)", got.SectionTokens)
		}
		if !slices.IsSorted(got.SectionTokens) {
			t.Fatalf("section_tokens = %v, want sorted", got.SectionTokens)
		}
		if len(got.VisibleSections) != 1 || got.VisibleSections[0].ID != "s-continue" {
			t.Fatalf("visible_sections = %+v, want s-continue", got.VisibleSections)
		}
		if !slices.Contains(got.SectionTokens, got.VisibleSections[0].Token) {
			t.Fatalf("section_tokens %v missing visible token %q", got.SectionTokens, got.VisibleSections[0].Token)
		}
	})

	t.Run("fails closed when CursorSecret is missing", func(t *testing.T) {
		deps := fixtureDeps()
		deps.CursorSecret = nil
		h := newTestHandler(t, deps)

		req := httptest.NewRequest(http.MethodGet, "/api/v2/profile/sections/baseline", nil)
		req.Header.Set("Authorization", "Bearer "+memberToken)
		req.Header.Set("X-Profile-Id", "p-owner")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("validates scope and library_id", func(t *testing.T) {
		deps := fixtureDeps()
		h := newTestHandler(t, deps)

		req := httptest.NewRequest(http.MethodGet, "/api/v2/profile/sections/baseline?scope=library", nil)
		req.Header.Set("Authorization", "Bearer "+memberToken)
		req.Header.Set("X-Profile-Id", "p-owner")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
		}
	})
}
