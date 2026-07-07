package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A library type the server seeds no metadata levels for (unknown types, or
// ones like podcasts without metadata content levels) has no defaults — the
// endpoint answers with an empty levels map rather than an error, so the UI
// can treat "no defaults" and "defaults" uniformly.
func TestHandleGetLibraryProviderDefaults_NoLevelsForType(t *testing.T) {
	h := &LibraryHandler{}

	for _, libraryType := range []string{"podcasts", "bogus", ""} {
		req := httptest.NewRequest(http.MethodGet, "/libraries/provider-defaults?library_type="+libraryType, nil)
		rec := httptest.NewRecorder()
		h.HandleGetLibraryProviderDefaults(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("type %q: expected 200, got %d", libraryType, rec.Code)
		}
		var body struct {
			Levels map[string][]chainLevelEntry `json:"levels"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("type %q: decoding body: %v", libraryType, err)
		}
		if len(body.Levels) != 0 {
			t.Errorf("type %q: expected empty levels, got %v", libraryType, body.Levels)
		}
	}
}

// A seedable type still needs the chain repository; without one the endpoint
// reports unavailable like the other provider-chain handlers.
func TestHandleGetLibraryProviderDefaults_NoChainRepo(t *testing.T) {
	h := &LibraryHandler{}

	req := httptest.NewRequest(http.MethodGet, "/libraries/provider-defaults?library_type=series", nil)
	rec := httptest.NewRecorder()
	h.HandleGetLibraryProviderDefaults(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}
