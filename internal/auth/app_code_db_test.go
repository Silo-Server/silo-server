package auth

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// redeemCode redeems code with a new device key signed by person.
func (e *keyEnv) redeemCode(t *testing.T, person testKey, code string) (*KeySignInResult, testKey, error) {
	t.Helper()
	device := newTestKey(t)
	result, err := e.signIn.RedeemAppCode(t.Context(), KeyRedeemRequest{
		KeyRequest: e.request(t, message{purpose: appkeys.PurposeRedeem, signer: person, device: &device, fields: map[string]any{"code": code}}),
		PersonKey:  person.pub,
	})
	return result, device, err
}

// deviceKeyRow reads a device key by its public key.
func (e *keyEnv) deviceKeyRow(t *testing.T, key testKey) *models.AppDeviceKey {
	t.Helper()
	row, err := e.signIn.keys.DeviceKeyByKeyID(t.Context(), key.pub.KeyID())
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// grantSession opens a login session the way a grant-enrolled app's key
// does: bound to a device key of kind app, enrolled by a grant.
func (e *keyEnv) grantSession(t *testing.T, user *models.User) *Claims {
	t.Helper()
	app := newTestKey(t)
	key, err := e.signIn.keys.InsertDeviceKey(t.Context(), NewDeviceKey{UserID: user.ID, PublicKey: app.pub,
		Kind: models.DeviceKeyKindApp, Name: "Other app", EnrolledVia: models.AppKeyEnrolledGrant})
	if err != nil {
		t.Fatal(err)
	}
	claims := e.loginSession(t, user, models.SessionOpenedAppKey, nil)
	if _, err := e.pool.Exec(t.Context(), `UPDATE auth_sessions SET device_key_id = $1 WHERE id = $2`, key.ID, claims.SessionID); err != nil {
		t.Fatal(err)
	}
	return claims
}

// A session code enrolls the app's keys on the account that issued it. An
// account with no person key takes the given one; one with a different key
// has it replaced, its waiting devices refused and its enrolled devices
// kept. The keys take the issuing session's provider binding, which is how a
// person gets the app on a server with local passwords off. A code works
// once, not after it expires, and not once its session has ended.
func TestRedeemSessionAppCodeDB(t *testing.T) {
	env := newKeyEnv(t, "appcode")
	ctx := t.Context()
	issue := func(t *testing.T, claims *Claims) string {
		t.Helper()
		issued, err := env.signIn.IssueSessionAppCode(ctx, claims, ActingProfile{})
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		return issued.Code
	}

	t.Run("an account with no person key", func(t *testing.T) {
		user := env.localAccount(t, "appcode-new", models.RoleUser)
		code := issue(t, env.loginSession(t, user, models.SessionOpenedPassword, nil))
		person := newTestKey(t)
		result, device, err := env.redeemCode(t, person, code)
		if err != nil {
			t.Fatal(err)
		}
		if result.PersonKeyStatus != PersonKeyEnrolled || result.Pair == nil || result.User.ID != user.ID {
			t.Fatalf("result = %+v", result)
		}
		if key := env.deviceKeyRow(t, device); key.Status != models.DeviceKeyStatusActive || key.EnrolledVia != models.AppKeyEnrolledAppCode || key.ProviderBound {
			t.Fatalf("device key = %+v", key)
		}
		if _, _, err := env.redeemCode(t, newTestKey(t), code); !errors.Is(err, ErrAppCodeInvalid) {
			t.Fatalf("second redeem: %v", err)
		}
	})

	t.Run("a different person key is replaced", func(t *testing.T) {
		user := env.localAccount(t, "appcode-replace", models.RoleAdmin)
		oldPerson, oldDevice, _ := env.enroll(t, user)
		waiting, _ := env.introduceWaiting(t, oldPerson)
		code := issue(t, env.loginSession(t, user, models.SessionOpenedPassword, nil))
		person := newTestKey(t)
		result, device, err := env.redeemCode(t, person, code)
		if err != nil {
			t.Fatal(err)
		}
		if result.PersonKeyStatus != PersonKeyReplaced {
			t.Fatalf("person key = %q", result.PersonKeyStatus)
		}
		live, err := env.signIn.keys.LivePersonKey(ctx, user.ID)
		if err != nil || live.KeyID != person.pub.KeyID() {
			t.Fatalf("live person key = %+v, %v", live, err)
		}
		// An administrator's keys from a code start active.
		if key := env.deviceKeyRow(t, device); key.Status != models.DeviceKeyStatusActive {
			t.Fatalf("new device key = %+v", key)
		}
		if key := env.deviceKeyRow(t, oldDevice); key.RevokedAt != nil || key.Status != models.DeviceKeyStatusActive {
			t.Fatalf("an enrolled device lost its key: %+v", key)
		}
		var status string
		if err := env.pool.QueryRow(ctx, `SELECT status FROM auth_device_keys WHERE id = $1`, waiting).Scan(&status); err != nil || status != models.DeviceKeyStatusDenied {
			t.Fatalf("waiting device = %q, %v", status, err)
		}
	})

	t.Run("keys take a provider session's identity, with local passwords off", func(t *testing.T) {
		t.Cleanup(saveSettings(t, env.pool, config.AuthLocalPasswordLoginSettingKey))
		code := issue(t, env.loginSession(t, env.user, models.SessionOpenedProvider, &env.identityID))
		env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
		_, device, err := env.redeemCode(t, newTestKey(t), code)
		if err != nil {
			t.Fatal(err)
		}
		key := env.deviceKeyRow(t, device)
		if !key.ProviderBound || key.IdentityID == nil || *key.IdentityID != env.identityID {
			t.Fatalf("device key binding = %+v", key)
		}
	})

	t.Run("a device key this server holds already", func(t *testing.T) {
		other := env.localAccount(t, "appcode-otherdevice", models.RoleUser)
		_, taken, _ := env.enroll(t, other)
		user := env.localAccount(t, "appcode-keytaken", models.RoleUser)
		code := issue(t, env.loginSession(t, user, models.SessionOpenedPassword, nil))
		person := newTestKey(t)
		_, err := env.signIn.RedeemAppCode(ctx, KeyRedeemRequest{
			KeyRequest: env.request(t, message{purpose: appkeys.PurposeRedeem, signer: person, device: &taken, fields: map[string]any{"code": code}}),
			PersonKey:  person.pub,
		})
		var enrolled *KeyEnrolledError
		if !errors.As(err, &enrolled) || enrolled.Member != "device_key" {
			t.Fatalf("redeem = %v, want the device key named as enrolled", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		user := env.localAccount(t, "appcode-expired", models.RoleUser)
		code := issue(t, env.loginSession(t, user, models.SessionOpenedPassword, nil))
		hash, _ := appCodeHash(code)
		if _, err := env.pool.Exec(ctx, `UPDATE auth_app_codes SET expires_at = NOW() - INTERVAL '1 second' WHERE code_hash = $1`, hash); err != nil {
			t.Fatal(err)
		}
		if _, _, err := env.redeemCode(t, newTestKey(t), code); !errors.Is(err, ErrAppCodeInvalid) {
			t.Fatalf("redeem: %v", err)
		}
	})

	t.Run("its session ended", func(t *testing.T) {
		user := env.localAccount(t, "appcode-ended", models.RoleUser)
		claims := env.loginSession(t, user, models.SessionOpenedPassword, nil)
		code := issue(t, claims)
		if _, err := env.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE id = $1`, claims.SessionID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := env.redeemCode(t, newTestKey(t), code); !errors.Is(err, ErrAppCodeInvalid) {
			t.Fatalf("redeem: %v", err)
		}
		if live, err := env.signIn.keys.LivePersonKey(ctx, user.ID); err == nil {
			t.Fatalf("a refused redeem left a person key: %+v", live)
		}
	})
}

// Only a session the person opened themselves issues an app code: a device
// sign-in, another app's grant session, a session with no record of how it
// opened, an administrator acting as the account, an API key and a
// remote-playback session are refused.
func TestIssueSessionAppCodeRefusalsDB(t *testing.T) {
	env := newKeyEnv(t, "appcoderefuse")
	user := env.localAccount(t, "appcoderefuse-user", models.RoleUser)
	_, device, _ := env.enroll(t, user)
	keySession := env.loginSession(t, user, models.SessionOpenedAppKey, nil)
	if _, err := env.pool.Exec(t.Context(), `UPDATE auth_sessions SET device_key_id = $1 WHERE id = $2`, env.deviceKeyRow(t, device).ID, keySession.SessionID); err != nil {
		t.Fatal(err)
	}
	impersonated := *env.loginSession(t, user, models.SessionOpenedImpersonation, nil)
	impersonated.ImpersonatorUserID = new(user.ID + 1)
	apiKey := *env.loginSession(t, user, models.SessionOpenedPassword, nil)
	apiKey.TokenType = TokenTypeAPIKey
	remotePlayback := *env.loginSession(t, user, models.SessionOpenedPassword, nil)
	remotePlayback.SessionPurpose = models.SessionPurposeRemotePlayback
	for _, tc := range []struct {
		name   string
		claims *Claims
		want   error
	}{
		{"password", env.loginSession(t, user, models.SessionOpenedPassword, nil), nil},
		{"provider", env.loginSession(t, env.user, models.SessionOpenedProvider, &env.identityID), nil},
		{"the person's own app key", keySession, nil},
		{"device sign-in", env.loginSession(t, user, models.SessionOpenedDeviceLogin, nil), ErrSessionNotAllowed},
		{"another app's grant", env.grantSession(t, user), ErrSessionNotAllowed},
		{"no record of how it opened", env.loginSession(t, user, "", nil), ErrSessionNotAllowed},
		{"administrator acting as the account", &impersonated, ErrSessionNotAllowed},
		{"API key", &apiKey, ErrSessionNotAllowed},
		{"remote-playback session", &remotePlayback, ErrSessionNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.signIn.IssueSessionAppCode(t.Context(), tc.claims, ActingProfile{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// What a browser page is told it may do, by session kind and by the other
// ways the account can sign in: "sign in again" only where a fresh sign-in
// would get a session that may manage keys.
func TestSessionKeyAccessDB(t *testing.T) {
	env := newKeyEnv(t, "keyaccess")
	ctx := t.Context()
	withPassword := env.localAccount(t, "keyaccess-password", models.RoleUser)
	passwordless := env.localAccount(t, "keyaccess-none", models.RoleUser)
	if _, err := env.pool.Exec(ctx, `UPDATE users SET password_hash = NULL, local_password_login_enabled = false WHERE id = $1`, passwordless.ID); err != nil {
		t.Fatal(err)
	}
	impersonated := *env.loginSession(t, withPassword, models.SessionOpenedImpersonation, nil)
	impersonated.ImpersonatorUserID = new(withPassword.ID + 1)
	full := SessionKeyAccess{List: true, Manage: true}
	for _, tc := range []struct {
		name   string
		claims *Claims
		want   SessionKeyAccess
	}{
		{"password session", env.loginSession(t, withPassword, models.SessionOpenedPassword, nil), full},
		{"device sign-in, account with a password", env.loginSession(t, withPassword, models.SessionOpenedDeviceLogin, nil), SessionKeyAccess{SignInAgain: true}},
		{"no record, account with a password", env.loginSession(t, withPassword, "", nil), SessionKeyAccess{SignInAgain: true}},
		{"device sign-in, provider account", env.loginSession(t, env.user, models.SessionOpenedDeviceLogin, nil), SessionKeyAccess{SignInAgain: true}},
		{"device sign-in, account with keys only", env.loginSession(t, passwordless, models.SessionOpenedDeviceLogin, nil), SessionKeyAccess{}},
		{"another app's grant", env.grantSession(t, withPassword), SessionKeyAccess{}},
		{"administrator acting as the account", &impersonated, SessionKeyAccess{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadSessionKeyAccess(ctx, env.pool, tc.claims)
			if err != nil || got != tc.want {
				t.Fatalf("access = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
	t.Run("device sign-in, password while local passwords are off", func(t *testing.T) {
		t.Cleanup(saveSettings(t, env.pool, config.AuthLocalPasswordLoginSettingKey))
		claims := env.loginSession(t, withPassword, models.SessionOpenedDeviceLogin, nil)
		env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
		if got, err := ReadSessionKeyAccess(ctx, env.pool, claims); err != nil || got.SignInAgain {
			t.Fatalf("access = %+v, %v", got, err)
		}
	})
}
