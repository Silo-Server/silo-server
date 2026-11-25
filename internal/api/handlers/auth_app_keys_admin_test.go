package handlers

import (
	"context"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The admin key operations apply the Owner rules to the account the request
// acts as (auth.TestAdminAppKeyAuthorityDB covers those rules by account).
// This pins which account that is for each kind of caller: a scoped API key
// acts as its owner, and an administrator viewing as another account acts as
// that account, never as the administrator behind it, so the Owner viewing
// as an admin can't use the Owner's authority there. A caller that is not an
// administrator acts as no one.
func TestAdminKeyActor(t *testing.T) {
	owner := 1
	for _, tc := range []struct {
		name   string
		claims *auth.Claims
		want   int
		denied bool
	}{
		{name: "an administrator's own session", claims: &auth.Claims{UserID: 7, Role: models.RoleAdmin, SessionID: "s"}, want: 7},
		{name: "a scoped API key", claims: &auth.Claims{UserID: 7, Role: models.RoleAdmin, APIKeyID: 3, APIKeyScopes: []string{"admin:users"}}, want: 7},
		{name: "the Owner viewing as an administrator", claims: &auth.Claims{UserID: 7, Role: models.RoleAdmin, SessionID: "s", ImpersonatorUserID: &owner}, want: 7},
		{name: "an account that is not an administrator", claims: &auth.Claims{UserID: 7, Role: models.RoleUser, SessionID: "s"}, denied: true},
		{name: "no credential", denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.claims != nil {
				ctx = apimw.SetClaims(ctx, tc.claims)
			}
			got, err := adminKeyActor(ctx)
			if tc.denied {
				if err == nil {
					t.Fatalf("actor = %d, want a refusal", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("actor = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}
