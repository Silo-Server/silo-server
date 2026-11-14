package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

func TestDeviceDecisionRevokedSessionResponse(t *testing.T) {
	t.Parallel()

	err := deviceDecisionError(auth.ErrSessionRevoked)
	if !errors.Is(err, auth.ErrSessionRevoked) {
		t.Fatal("device decision lost the revoked-session cause")
	}
	rec := httptest.NewRecorder()
	writeAPIError(rec, err)
	var body struct {
		Error string `json:"error"`
	}
	if rec.Code != http.StatusUnauthorized || json.NewDecoder(rec.Body).Decode(&body) != nil || body.Error != "unauthorized" {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}
