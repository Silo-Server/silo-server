package apiv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/sections"
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

		// The capability says so too, rather than promising the operation.
		req = httptest.NewRequest(http.MethodGet, "/api/v2/profile/sections/flags", nil)
		req.Header.Set("Authorization", "Bearer "+memberToken)
		req.Header.Set("X-Profile-Id", "p-owner")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var flags ProfileSectionFlags
		if err := json.Unmarshal(rec.Body.Bytes(), &flags); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("flags: status = %d, body = %s, err = %v", rec.Code, rec.Body.String(), err)
		}
		if flags.HomeLayoutBaseline {
			t.Fatal("home_layout_baseline = true, want false without a signing secret")
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

func TestBaselineTokensAreBoundToTheProfile(t *testing.T) {
	secret := []byte("test-secret")
	baseline := handlers.ProfileSectionBaseline{
		SectionIDs:      []string{"s-continue"},
		VisibleSections: []sections.ResolvedSection{{ID: "s-continue", SectionType: "continue_watching", Title: "Continue Watching"}},
	}

	owner := profileSectionBaselineOf(secret, "p-owner", "home", "", baseline)
	again := profileSectionBaselineOf(secret, "p-owner", "home", "", baseline)
	kid := profileSectionBaselineOf(secret, "p-kid", "home", "", baseline)

	if !slices.Equal(owner.SectionTokens, again.SectionTokens) {
		t.Fatalf("tokens for one profile differ between reads: %v and %v", owner.SectionTokens, again.SectionTokens)
	}
	// Another profile's export must match nothing here, so an import never
	// reads rows that profile couldn't see as rows added since its export.
	if slices.Contains(kid.SectionTokens, owner.VisibleSections[0].Token) {
		t.Fatalf("p-kid's tokens %v contain p-owner's token %q", kid.SectionTokens, owner.VisibleSections[0].Token)
	}
}
