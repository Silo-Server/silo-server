package handlers

import (
	"context"
	"slices"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminLoginSessionStore struct{}

func (fakeAdminLoginSessionStore) ListByUserPage(context.Context, int, *auth.SessionKey, int) ([]*models.AuthSession, error) {
	return nil, nil
}

func (fakeAdminLoginSessionStore) RevokeAsAdmin(context.Context, int, int, *string) (int, error) {
	return 1, nil
}

// Signing an account out everywhere ends its Jellyfin-compatible sessions
// too, as a password reset or disable does; signing out one login session
// leaves them alone, since they are not tied to a login session.
func TestRevokeAdminLoginSessionsEndsCompatSessionsOnlyEverywhere(t *testing.T) {
	h, _ := newScopedKeyAdminHandler(models.RoleUser)
	h.loginSessions = fakeAdminLoginSessionStore{}
	var notified []int
	h.OnUserSessionsRevoked = func(_ context.Context, userID int) { notified = append(notified, userID) }
	ctx := apimw.SetClaims(context.Background(), jwtAdminClaims())

	one := "00000000-0000-0000-0000-000000000001"
	if _, err := h.RevokeAdminLoginSessions(ctx, 42, &one); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 0 {
		t.Fatalf("single-session revocation notified %v", notified)
	}
	if _, err := h.RevokeAdminLoginSessions(ctx, 42, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(notified, []int{42}) {
		t.Fatalf("notified = %v, want [42]", notified)
	}
}
