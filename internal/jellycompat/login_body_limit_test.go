package jellycompat

import (
	"net/http"
	"strings"
	"testing"
)

// TestAuthenticateByNameRejectsOversizedBody pins the sign-in body cap: an
// oversized body is refused as malformed before the password check, which
// would answer 401.
func TestAuthenticateByNameRejectsOversizedBody(t *testing.T) {
	router := newRateLimitedRouter(t, nil, nil)
	const ip = "203.0.113.10"

	oversized := `{"Username":"alice","Pw":"` + strings.Repeat("a", maxAuthenticateByNameBody) + `"}`
	if rec := serveFrom(router, ip, http.MethodPost, "/Users/AuthenticateByName", oversized, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized sign-in: status = %d, want 400", rec.Code)
	}
	if rec := signInFrom(router, ip, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("normal sign-in: status = %d, want 401", rec.Code)
	}
}
