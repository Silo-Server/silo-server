package abs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

type refusingValidator struct{ err error }

func (v refusingValidator) Validate(context.Context, string, string) (Principal, error) {
	return Principal{}, v.err
}

// A temporary password is a refusal the client can show, not an outage.
func TestLogin_TemporaryPasswordIsRefusedNotUnavailable(t *testing.T) {
	h := New(Dependencies{
		Config:        &staticConfig{secret: []byte("test-secret-32-bytes-aaaaaaaaaaaaa")},
		TokenStore:    newMemTokenStore(),
		MediaStore:    noopMediaStore{},
		CredValidator: refusingValidator{err: fmt.Errorf("compat login: %w", auth.ErrPasswordChangeRequired)},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"bob","password":"temporary-pass"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handleLogin(rec, req)

	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "temporary password") {
		t.Fatalf("status = %d, body = %q; want 401 naming the temporary password", rec.Code, rec.Body.String())
	}
}
