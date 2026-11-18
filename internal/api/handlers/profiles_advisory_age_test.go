package handlers

import (
	"context"
	"errors"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// profileServiceContext is a non-admin caller acting as activeProfileID.
func profileServiceContext(activeProfileID string) context.Context {
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess})
	if activeProfileID != "" {
		ctx = apimw.SetProfileID(ctx, activeProfileID)
	}
	return ctx
}

func noProfileVerification(*userstore.Profile) error { return nil }

func requireAPIStatus(t *testing.T, err error, status int) *APIError {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != status {
		t.Fatalf("error = %v, want an API error with status %d", err, status)
	}
	return apiErr
}
