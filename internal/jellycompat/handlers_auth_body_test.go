package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestHandleAuthenticateByNameRejectsOversizedBody(t *testing.T) {
	// No login resolver is wired, so reaching it would panic: a 413 proves the
	// handler stopped before checking credentials.
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil)

	body := `{"Username":"admin","Pw":"` + strings.Repeat("a", maxAuthenticateByNameBodyBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleAuthenticateByName(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
}

func TestHandleAuthenticateByNameKeepsBadRequestForMalformedBody(t *testing.T) {
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":`))
	rec := httptest.NewRecorder()

	h.HandleAuthenticateByName(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
