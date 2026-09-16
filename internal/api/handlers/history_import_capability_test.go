package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

func TestHistoryImportCapabilityReportsPlexConnectionFallback(t *testing.T) {
	// The handler answers from constants, so a nil service is the whole
	// dependency set: the probe must work on a deployment whose history-import
	// service cannot reach anything.
	handler := &HistoryImportHandler{}
	rec := httptest.NewRecorder()
	handler.HandleCapability(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history-imports/capability", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got historyImportCapabilityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding capability: %v (%s)", err, rec.Body.String())
	}

	if got.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", got.SchemaVersion)
	}
	if !got.PlexConnectionFallback {
		t.Error("plex_connection_fallback = false; a client cannot detect plex_base_urls support")
	}
	// The advertised cap must be the one the service actually enforces, or a
	// client will size plex_base_urls against fiction and quietly lose the
	// addresses past the real limit.
	if got.MaxPlexConnectionCandidates != historyimport.MaxPlexConnectionCandidates {
		t.Errorf("max_plex_connection_candidates = %d, want %d",
			got.MaxPlexConnectionCandidates, historyimport.MaxPlexConnectionCandidates)
	}
}

// TestHistoryImportRejectsInsecurePlexAddressAsBadRequest pins the error to a
// 400 carrying its own text. writeHistoryImportError's default branch turns
// anything it does not recognize into a generic 500, which would leave a
// profile whose Plex server advertises only cleartext addresses with no way to
// learn why the import will not start.
func TestHistoryImportRejectsInsecurePlexAddressAsBadRequest(t *testing.T) {
	handler := &HistoryImportHandler{}
	rec := httptest.NewRecorder()
	handler.writeHistoryImportError(rec, historyimport.ErrNoSecurePlexAddress)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding error body: %v (%s)", err, rec.Body.String())
	}
	if got.Error != "bad_request" {
		t.Errorf("error = %q, want bad_request", got.Error)
	}
	if got.Message != historyimport.ErrNoSecurePlexAddress.Error() {
		t.Errorf("message = %q, want the sentinel's own text", got.Message)
	}
}
