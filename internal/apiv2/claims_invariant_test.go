package apiv2

import (
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

func TestAuthenticatedClassesRejectInvalidClaims(t *testing.T) {
	claims := map[string]*auth.Claims{
		"nil":      nil,
		"zero":     {TokenType: auth.TokenTypeAccess, SessionID: "s1"},
		"negative": {UserID: -1, TokenType: auth.TokenTypeAccess, SessionID: "s1"},
	}
	deps := parityDeps(false)
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{claims}, fakeSessions{map[string]string{"s1": "user"}}, nil, nil)
	h := newTestHandler(t, deps)
	for token := range claims {
		for _, class := range []string{"authenticated", "profile_scoped", "acting_admin", "permission_gated"} {
			rec := do(t, h, "POST", Prefix+"/probe/"+class, `{"name":"x","cleared":null}`, bearer(token))
			requireProblem(t, rec, TypeInvalidToken)
		}
	}
}
