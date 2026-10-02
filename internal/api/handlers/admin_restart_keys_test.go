package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// The admin UI badges restart-required fields from this endpoint, so it has to
// report the compiled registry verbatim — both exact keys and whole namespaces.
func TestHandleGetRestartKeys(t *testing.T) {
	handler := &AdminHandler{}
	rec := httptest.NewRecorder()

	handler.HandleGetRestartKeys(rec, httptest.NewRequest(http.MethodGet, "/admin/settings/restart-keys", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Keys     []string `json:"keys"`
		Prefixes []string `json:"prefixes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !slices.Equal(body.Keys, config.RestartRequiredKeys()) {
		t.Errorf("keys = %v, want %v", body.Keys, config.RestartRequiredKeys())
	}
	if !slices.Equal(body.Prefixes, config.RestartRequiredPrefixes()) {
		t.Errorf("prefixes = %v, want %v", body.Prefixes, config.RestartRequiredPrefixes())
	}
	if !sort.StringsAreSorted(body.Keys) {
		t.Errorf("keys are not sorted: %v", body.Keys)
	}

	// Spot-check both matching modes so a registry refactor that drops one of
	// them fails here rather than silently un-badging the UI.
	if !slices.Contains(body.Keys, "auth.jwt_secret") {
		t.Errorf("keys missing auth.jwt_secret: %v", body.Keys)
	}
	if !slices.Contains(body.Prefixes, "database.") {
		t.Errorf("prefixes missing database.: %v", body.Prefixes)
	}
	for _, key := range body.Keys {
		if !config.RestartRequired(key) {
			t.Errorf("reported key %q is not restart-required", key)
		}
	}
	for _, prefix := range body.Prefixes {
		if !config.RestartRequired(prefix + "example") {
			t.Errorf("reported prefix %q does not mark its namespace restart-required", prefix)
		}
	}
}
