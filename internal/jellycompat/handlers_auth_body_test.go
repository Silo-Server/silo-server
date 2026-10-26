package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
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

// A name that has used its sign-in attempt budget answers 429 in the
// listener's error envelope with the time left in Retry-After.
func TestHandleAuthenticateByNameThrottledName(t *testing.T) {
	svc := auth.NewService(nil, nil, nil, nil, nil, nil, nil)
	svc.SetSignInAttempts(ratelimit.NewMemoryAttemptLimiter(ratelimit.AttemptPolicy{MaxAttempts: 1, Window: time.Minute}))
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, NewLoginResolver(svc, nil, nil, nil, nil), nil)
	signIn := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"alice","Pw":"secret"}`))
		rec := httptest.NewRecorder()
		h.HandleAuthenticateByName(rec, req)
		return rec
	}

	if rec := signIn(); rec.Code != http.StatusUnauthorized {
		t.Fatalf("first attempt: status = %d, want 401; body %s", rec.Code, rec.Body.String())
	}
	rec := signIn()
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" || !strings.Contains(rec.Body.String(), "TooManyRequests") {
		t.Fatalf("locked name: status = %d, Retry-After = %q, body %s; want 429, 60, TooManyRequests",
			rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}
