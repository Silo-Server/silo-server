package ratelimit

import (
	"context"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

type accountContextKey struct{}

// WithAccount attaches an already authenticated account for listeners that do
// not use native API claims. Callers must validate the credential and its current
// owner first. This value grants no authority and must never come from a header
// or from a profile, session or device identifier.
func WithAccount(ctx context.Context, accountID int) context.Context {
	return context.WithValue(ctx, accountContextKey{}, accountID)
}

func accountFromContext(ctx context.Context) int {
	if claims := apimw.GetClaims(ctx); claims != nil {
		return claims.UserID
	}
	id, _ := ctx.Value(accountContextKey{}).(int)
	return id
}
