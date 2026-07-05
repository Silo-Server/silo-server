package abs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingValidator captures the credentials handleStandaloneLogin extracted
// from the request body, and reports success so completeLogin runs.
type recordingValidator struct{ gotUser, gotPass string }

func (v *recordingValidator) Validate(_ context.Context, u, p string) (string, string, string, error) {
	v.gotUser, v.gotPass = u, p
	return "1", "", "Alice", nil
}

func newLoginBodyHandler(v *recordingValidator) *Handler {
	return New(Dependencies{
		Config:        &staticConfig{secret: []byte("test-secret-32-bytes-aaaaaaaaaaaaa")},
		TokenStore:    newMemTokenStore(),
		MediaStore:    noopMediaStore{},
		CredValidator: v,
	})
}

// TestLogin_AcceptsFormEncoded is the regression guard for the real bug:
// real ABS accepts application/x-www-form-urlencoded credentials; a JSON-only
// parse 400s a form-encoded client, surfacing as "unknown error" on sign-in.
func TestLogin_AcceptsFormEncoded(t *testing.T) {
	v := &recordingValidator{}
	h := newLoginBodyHandler(v)

	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader("username=alice&password=s3cret%21"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.handleLogin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if v.gotUser != "alice" || v.gotPass != "s3cret!" {
		t.Errorf("validator got user=%q pass=%q, want alice/s3cret!", v.gotUser, v.gotPass)
	}
}

// TestLogin_AcceptsJSON confirms the JSON path still works after the change.
func TestLogin_AcceptsJSON(t *testing.T) {
	v := &recordingValidator{}
	h := newLoginBodyHandler(v)

	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"bob","password":"pw"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handleLogin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if v.gotUser != "bob" || v.gotPass != "pw" {
		t.Errorf("validator got user=%q pass=%q, want bob/pw", v.gotUser, v.gotPass)
	}
}
