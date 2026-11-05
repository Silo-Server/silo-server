package handlers

import (
	"context"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

func authenticatedRecsContext(parent context.Context, rctx *chi.Context) context.Context {
	ctx := parent
	if rctx != nil {
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 7})
	return apimw.SetProfileID(ctx, "profile-1")
}
