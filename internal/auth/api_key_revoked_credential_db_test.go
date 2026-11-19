package auth

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// A key request whose own login session, or API key, was revoked after it
// authenticated mints nothing: revoking an account's sign-ins locks the
// account first, and key creation checks its credential under that lock.
func TestAPIKeyCreationRefusesRevokedCredentialDB(t *testing.T) {
	d := newAuditActionsDB(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, d.pool, config.AuthUserAPIKeysSettingKey))
	if _, err := d.pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'true')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthUserAPIKeysSettingKey); err != nil {
		t.Fatal(err)
	}
	account, admin := d.account(t, "user", false), d.account(t, "admin", false)
	session := d.session(t, account)
	asSession := WithClaims(ctx, &Claims{UserID: account, SessionID: session, TokenType: TokenTypeAccess})
	watch := []string{ScopeWatchRead}

	if _, err := d.keys.CreatePersonal(asSession, account, APIKeyActingProfile{}, "", "Live", watch); err != nil {
		t.Fatalf("live session: %v", err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at = now() WHERE id = $1`, session); err != nil {
		t.Fatal(err)
	}
	if _, err := d.keys.CreatePersonal(asSession, account, APIKeyActingProfile{}, "", "Late", watch); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("revoked session minted a personal key: %v", err)
	}

	minted, err := d.keys.Create(ctx, admin, "Admin automation", nil)
	if err != nil {
		t.Fatal(err)
	}
	asKey := WithClaims(ctx, &Claims{UserID: admin, TokenType: TokenTypeAPIKey, APIKeyID: minted.ID})
	if _, err := d.keys.CreateAsActor(asKey, admin, account, "Live", watch); err != nil {
		t.Fatalf("live API key: %v", err)
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, minted.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.keys.CreateAsActor(asKey, admin, account, "Late", watch); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("deleted API key minted a key: %v", err)
	}
}
