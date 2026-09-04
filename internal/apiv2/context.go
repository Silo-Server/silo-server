package apiv2

import (
	"context"

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
