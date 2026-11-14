package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/policy"
)

// errPolicyEvalTimeout is the error the viewer resolver returns when the scope
// policy runs out of its evaluation budget.
var errPolicyEvalTimeout = fmt.Errorf("resolve viewer scope policy: %w",
	fmt.Errorf("%w: %w after 100ms: %w", policy.ErrPolicyEvalFailed, policy.ErrPolicyEvalTimeout, context.DeadlineExceeded))

type recordingResolver struct{ got *access.ResolveInput }

func (s recordingResolver) Resolve(_ context.Context, in access.ResolveInput) (access.Scope, error) {
	*s.got = in
	return access.Scope{UserID: in.UserID, ProfileID: in.ProfileID, ProfileVerified: true}, nil
}

// An administrator viewing as an account opens its PIN-locked profiles
// without the PIN, as an API key does; the account holder's own session still
// has to prove it. The resolver then marks the scope PINVerificationSkipped,
// which household management refuses.
func TestViewerAccessSkipsProfilePINOnlyForExemptCredentials(t *testing.T) {
	admin := 1
	for name, tc := range map[string]struct {
		claims   auth.Claims
		wantSkip bool
	}{
		"own login session": {auth.Claims{UserID: 2, SessionID: "s", TokenType: auth.TokenTypeAccess}, false},
		"view-as session":   {auth.Claims{UserID: 2, SessionID: "s", TokenType: auth.TokenTypeAccess, ImpersonatorUserID: &admin}, true},
		"API key":           {auth.Claims{UserID: 2, TokenType: auth.TokenTypeAPIKey}, true},
	} {
		t.Run(name, func(t *testing.T) {
			var got access.ResolveInput
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.Header.Set("X-Profile-Id", "locked")
			r = r.WithContext(SetClaims(r.Context(), &tc.claims))
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
			NewViewerAccessMiddleware(recordingResolver{got: &got}).RequireViewerAccess(next).ServeHTTP(httptest.NewRecorder(), r)
			if got.ProfileID != "locked" || got.SkipPINVerification != tc.wantSkip {
				t.Fatalf("resolve input %+v, want profile locked with SkipPINVerification=%v", got, tc.wantSkip)
			}
		})
	}
}

// A policy that runs out of time refuses the request without deciding it.
// That is a 503 the client retries, not a server fault; every other resolver
// failure is still a 500, and neither reaches the handler.
func TestViewerAccessPolicyTimeoutIsRetryable(t *testing.T) {
	for name, tc := range map[string]struct {
		err        error
		status     int
		retryAfter string
	}{
		"policy timeout":        {errPolicyEvalTimeout, http.StatusServiceUnavailable, "1"},
		"other policy failure":  {fmt.Errorf("resolve viewer scope policy: %w", policy.ErrPolicyEvalFailed), http.StatusInternalServerError, ""},
		"user store failure":    {errors.New("opening user store for 1: connection refused"), http.StatusInternalServerError, ""},
		"canceled during check": {fmt.Errorf("resolve viewer scope policy: %w: %w", policy.ErrPolicyEvalFailed, context.Canceled), http.StatusInternalServerError, ""},
	} {
		t.Run(name, func(t *testing.T) {
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("refused request reached the handler") })
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.Header.Set("X-Profile-Id", "p1")
			r = r.WithContext(SetClaims(r.Context(), &auth.Claims{UserID: 1, Role: "user", SessionID: "s"}))
			rec := httptest.NewRecorder()
			NewViewerAccessMiddleware(stubResolver{err: tc.err}).RequireViewerAccess(next).ServeHTTP(rec, r)
			if rec.Code != tc.status || rec.Header().Get("Retry-After") != tc.retryAfter {
				t.Fatalf("status %d Retry-After %q, want %d %q: %s", rec.Code, rec.Header().Get("Retry-After"), tc.status, tc.retryAfter, rec.Body.String())
			}
		})
	}
}
