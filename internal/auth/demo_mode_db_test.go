package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/models"
)

// poolSettings reads server settings straight from the test database.
type poolSettings struct{ env *externalSignInEnv }

func (p poolSettings) Get(ctx context.Context, key string) (string, error) {
	var value string
	err := p.env.pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// TestExternalSignInDemoModeDB: on a demo server every visitor shares one
// account that is not an administrator, so no external sign-in may attach an
// identity to it. Catches a linking flow that a visitor started before demo
// mode was turned on, and email auto-match, still changing how the shared
// account signs in. An administrator's link is unaffected.
func TestExternalSignInDemoModeDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, env.pool, demomode.SettingKey))
	env.resolver.WithDemoSettings(demomode.New(poolSettings{env}))
	attached := func(identity ExternalIdentity) bool {
		t.Helper()
		_, err := identityBySubject(ctx, env.pool, env.installationID, identity.Subject)
		if err != nil && !errors.Is(err, ErrIdentityNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}

	t.Run("linking flow", func(t *testing.T) {
		env.setSetting(t, demomode.SettingKey, "true")
		visitor := env.localAccount(t, "demo-link", models.RoleUser)
		identity := env.identity("demo-link-idp")
		if _, err := env.resolve(t, identity, false, visitor.ID); !errors.Is(err, ErrDemoRestricted) || !errors.Is(err, ErrNotPermitted) {
			t.Fatalf("err = %v, want ErrDemoRestricted", err)
		}
		if attached(identity) {
			t.Fatal("the identity was attached to the demo account")
		}
		if after, _ := NewUserRepository(env.pool).GetByID(ctx, visitor.ID); !after.LocalPasswordLoginEnabled {
			t.Fatal("the demo account lost its local password")
		}
		admin := env.localAccount(t, "demo-link-admin", models.RoleAdmin)
		if _, err := env.resolve(t, env.identity("demo-link-admin-idp"), false, admin.ID); err != nil {
			t.Fatalf("administrator link in demo mode: %v", err)
		}
	})

	t.Run("email auto-match", func(t *testing.T) {
		env.setSetting(t, config.AuthEmailAutoMatchSettingKey, "true")
		env.setSetting(t, demomode.SettingKey, "true")
		visitor := env.localAccount(t, "demo-match", models.RoleUser)
		sessions := NewSessionRepository(env.pool)
		session := models.AuthSession{ID: uuid.New().String(), UserID: visitor.ID, DeviceName: "demo-visitor", ExpiresAt: time.Now().Add(time.Hour)}
		if err := sessions.Create(ctx, session); err != nil {
			t.Fatal(err)
		}
		verified := true
		identity := env.identity("demo-match-idp")
		identity.Email, identity.EmailVerified = visitor.Email, &verified
		// Matching does not apply, as when it is off: a new account would
		// take the email, so creation refuses.
		if _, err := env.resolve(t, identity, true, 0); !errors.Is(err, ErrEmailInUse) {
			t.Fatalf("err = %v, want ErrEmailInUse", err)
		}
		if attached(identity) {
			t.Fatal("email matching attached the identity to the demo account")
		}
		if live, err := sessions.IsValid(ctx, session.ID); err != nil || !live {
			t.Fatalf("the demo account's session live = %v, %v; want live", live, err)
		}
		if len(env.revoked) != 0 {
			t.Fatalf("session hook ran for %v", env.revoked)
		}
	})
}
