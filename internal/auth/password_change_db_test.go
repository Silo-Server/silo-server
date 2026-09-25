package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestTemporaryPasswordLifecycleDB runs against a migrated database: a
// temporary password restricts the sessions it opens until the account
// chooses a new one, and every password write decides the flag.
func TestTemporaryPasswordLifecycleDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	users := NewUserRepository(pool)
	sessions := NewSessionRepository(pool)
	jwt := NewJWTService("temporary-password-test", 15*time.Minute, 24*time.Hour)
	svc := NewService(NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil)

	name := fmt.Sprintf("temporary-password-%d", time.Now().UnixNano())
	user, err := users.Create(ctx, models.CreateUserInput{Username: name, Email: name + "@example.invalid", Password: "temporary-pass", Role: models.RoleUser, PasswordChangeRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM auth_sessions WHERE user_id = $1`, user.ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, user.ID)
	})
	flag := func() bool {
		t.Helper()
		current, err := users.GetByID(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current.PasswordChangeRequired
	}
	restricted := func(token string) bool {
		t.Helper()
		claims, err := jwt.ValidateToken(token)
		if err != nil {
			t.Fatal(err)
		}
		return claims.PasswordChangeRequired
	}

	pair, _, err := svc.Login(ctx, name, "temporary-pass", "test", "")
	if err != nil || !restricted(pair.AccessToken) {
		t.Fatalf("temporary login not restricted: %v", err)
	}
	if _, _, err := svc.CompatLogin(ctx, name, "temporary-pass", "test", ""); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("compat login with a temporary password: %v", err)
	}
	// Someone else who knew the temporary password signed in too.
	other, _, err := svc.Login(ctx, name, "temporary-pass", "other", "")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := jwt.ValidateToken(pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangePassword(ctx, user.ID, claims.SessionID, "temporary-pass", "temporary-pass"); !errors.Is(err, ErrPasswordUnchanged) {
		t.Fatalf("reusing the temporary password: %v", err)
	}
	if err := svc.ChangePassword(ctx, user.ID, claims.SessionID, "temporary-pass", "chosen-pass"); err != nil {
		t.Fatal(err)
	}
	if flag() {
		t.Fatal("changing the password left it temporary")
	}
	refreshed, err := svc.Refresh(ctx, pair.RefreshToken)
	if err != nil || restricted(refreshed.AccessToken) {
		t.Fatalf("refresh after the change still restricted: %v", err)
	}
	// The other temporary-password session is revoked, not promoted.
	if _, err := svc.Refresh(ctx, other.RefreshToken); err == nil {
		t.Fatal("another temporary-password session survived the change")
	}

	// An administrator's password write decides the flag either way; other
	// writes leave it alone.
	temporary := "admin-temporary"
	if err := users.Update(ctx, user.ID, models.UpdateUserInput{Password: &temporary, PasswordChangeRequired: true}); err != nil || !flag() {
		t.Fatalf("temporary admin password not flagged: %v", err)
	}
	enabled := true
	if err := users.Update(ctx, user.ID, models.UpdateUserInput{Enabled: &enabled, PasswordChangeRequired: false}); err != nil || !flag() {
		t.Fatalf("a write without a password cleared the flag: %v", err)
	}
	settled := "admin-settled"
	if err := users.Update(ctx, user.ID, models.UpdateUserInput{Password: &settled}); err != nil || flag() {
		t.Fatalf("settled admin password still flagged: %v", err)
	}
}
