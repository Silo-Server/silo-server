package apiv2

import (
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

func TestAuthenticatedClassesRejectInvalidClaims(t *testing.T) {
	for _, claims := range []*auth.Claims{nil, {TokenType: auth.TokenTypeAccess, SessionID: "s1"}, {UserID: -1, TokenType: auth.TokenTypeAccess, SessionID: "s1"}} {
		deps := parityDeps(false)
		deps.Auth = apimw.NewAuthMiddleware(fakeTokens{map[string]*auth.Claims{"invalid": claims}}, fakeSessions{map[string]bool{"s1": true}}, nil, nil)
		h := newTestHandler(t, deps)
		for _, class := range []string{"authenticated", "profile_scoped", "acting_admin", "permission_gated"} {
			rec := do(t, h, "POST", Prefix+"/probe/"+class, `{"name":"x","cleared":null}`, bearer("invalid"))
			requireProblem(t, rec, TypeInvalidToken)
		}
	}
}
