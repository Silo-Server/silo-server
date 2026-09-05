package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// Handlers read the caller's identity from the context the gates populated,
// never from headers: these are the only accessors a v2 handler uses.

// claimsFrom returns the authenticated claims, nil on a public operation.
func claimsFrom(ctx context.Context) *auth.Claims { return apimw.GetClaims(ctx) }

// profileFrom returns the declared and verified profile ID, "" when the
// operation is not profile scoped.
func profileFrom(ctx context.Context) string { return apimw.GetProfileID(ctx) }

// scopeFrom returns the resolved viewer scope.
func scopeFrom(ctx context.Context) (access.Scope, bool) { return access.GetScope(ctx) }

func hasScope(ctx context.Context) bool {
	_, ok := scopeFrom(ctx)
	return ok
}

// requestKey carries the inbound *http.Request for the few handlers that
// need transport facts no typed input carries (User-Agent, the client-facing
// origin for absolute URLs). Handlers never read credentials from it.
type requestKey struct{}

// withRequest stores the request on its own context; installed by
// newChiRouter in front of the Huma adapter.
func withRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, r)))
	})
}

// requestFrom returns the inbound request, nil outside the listener.
func requestFrom(ctx context.Context) *http.Request {
	r, _ := ctx.Value(requestKey{}).(*http.Request)
	return r
}
