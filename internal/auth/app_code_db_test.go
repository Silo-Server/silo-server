package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// redeemCode redeems code with a new device key signed by person. A
// successful redeem's person_key_id is the account's person key, and it is
// person's key exactly when the answer is enrolled: a client tells whether
// the account holds the key it sent by the ID alone.
func (e *keyEnv) redeemCode(t *testing.T, person testKey, code string) (*KeySignInResult, testKey, error) {
	t.Helper()
	device := newTestKey(t)
	result, err := e.signIn.RedeemAppCode(t.Context(), KeyRedeemRequest{
		KeyRequest: e.request(t, message{purpose: appkeys.PurposeRedeem, signer: person, device: &device, fields: map[string]any{"code": code}}),
		PersonKey:  person.pub,
	})
	if err != nil {
		return result, device, err
	}
	live, liveErr := e.signIn.keys.LivePersonKey(t.Context(), result.User.ID)
	if liveErr != nil || !result.PersonKeyReported || result.PersonKeyID != live.KeyID {
		t.Fatalf("person_key_id = %q, want the account's person key %+v (%v)", result.PersonKeyID, live, liveErr)
	}
	switch sent := result.PersonKeyID == person.pub.KeyID(); result.PersonKeyStatus {
	case PersonKeyEnrolled:
		if !sent {
			t.Fatalf("enrolled, but person_key_id %q is not the sent key %q", result.PersonKeyID, person.pub.KeyID())
		}
	case PersonKeyKept:
		if sent {
			t.Fatalf("kept, but person_key_id is the sent key %q", result.PersonKeyID)
		}
	default:
		t.Fatalf("person key status = %q", result.PersonKeyStatus)
	}
	return result, device, nil
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

// personKeyRevoked reports whether the person key with id is revoked.
func (e *keyEnv) personKeyRevoked(t *testing.T, id string) bool {
	t.Helper()
	var revoked bool
	if err := e.pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM auth_person_keys WHERE id = $1`, id).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	return revoked
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
// account with no live person key takes the given one. One with a different
// key keeps it: the device enrolls under the account's key, and the
// account's other devices, enrolled or waiting for approval, are untouched.
// The keys take the issuing session's provider binding, which is how a
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

	// Regression: the redeem used to make the body's key the account's
	// person key, revoking the one the account's other devices hold and
	// denying the devices waiting for approval on it.
	t.Run("a different person key is kept", func(t *testing.T) {
		user := env.localAccount(t, "appcode-keep", models.RoleAdmin)
		accountPerson, oldDevice, _ := env.enroll(t, user)
		waiting, _ := env.introduceWaiting(t, accountPerson)
		code := issue(t, env.loginSession(t, user, models.SessionOpenedPassword, nil))
		person := newTestKey(t)
		result, device, err := env.redeemCode(t, person, code)
		if err != nil {
			t.Fatal(err)
		}
		if result.PersonKeyStatus != PersonKeyKept || result.PersonKeyID != accountPerson.pub.KeyID() || result.Pair == nil {
			t.Fatalf("result = %+v, want kept with the account's key %s", result, accountPerson.pub.KeyID())
		}
		live, err := env.signIn.keys.LivePersonKey(ctx, user.ID)
		if err != nil || live.KeyID != accountPerson.pub.KeyID() {
			t.Fatalf("live person key = %+v, %v", live, err)
		}
		var stored bool
		if err := env.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_person_keys WHERE key_id = $1)`, person.pub.KeyID()).Scan(&stored); err != nil || stored {
			t.Fatalf("the body's person key was stored: %v, %v", stored, err)
		}
		// An administrator's keys from a code start active.
		if key := env.deviceKeyRow(t, device); key.Status != models.DeviceKeyStatusActive || key.PersonKeyID == nil || *key.PersonKeyID != live.ID {
			t.Fatalf("new device key = %+v, want active under the account's person key %s", key, live.ID)
		}
		if key := env.deviceKeyRow(t, oldDevice); key.RevokedAt != nil || key.Status != models.DeviceKeyStatusActive {
			t.Fatalf("an enrolled device lost its key: %+v", key)
		}
		// The device waiting for approval on the administrator's account
		// is still waiting, and can still be approved.
		approver := env.loginSession(t, user, models.SessionOpenedPassword, nil)
		if got := env.pendingIDs(t, approver); len(got) != 1 || got[0] != waiting {
			t.Fatalf("waiting devices = %v, want [%s]", got, waiting)
		}
		if err := env.signIn.ApprovePendingKey(ctx, approver, waiting, ActingProfile{}); err != nil {
			t.Fatalf("approve the waiting device: %v", err)
		}
	})

	// A person who has lost every other device starts fresh: the redeem
	// keeps the account's key, then the session it opened replaces the key
	// as the household's main profile. The route refuses a child profile
	// (main_profile_required) and an unproved PIN
	// (profile_verification_required) before the service runs
	// (TestKeyCreatingRoutesNeedVerifiedMainProfile); here the service, given
	// the redeem's session, refuses a profile that is not the main one or
	// whose PIN changed, and replaces the key for the main profile.
	t.Run("redeem, then replace from the redeem's session", func(t *testing.T) {
		user := env.localAccount(t, "appcode-fresh", models.RoleUser)
		env.enroll(t, user)
		main := ActingProfile{ID: uuid.NewString()}
		child := ActingProfile{ID: uuid.NewString()}
		if err := env.pool.QueryRow(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary, pin_hash)
			VALUES ($1, $2, 'Main', true, 'hash-of-1234') RETURNING pin_revision`, main.ID, user.ID).Scan(&main.PINRevision); err != nil {
			t.Fatal(err)
		}
		if err := env.pool.QueryRow(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary)
			VALUES ($1, $2, 'Child', false) RETURNING pin_revision`, child.ID, user.ID).Scan(&child.PINRevision); err != nil {
			t.Fatal(err)
		}
		code := issue(t, env.loginSession(t, user, models.SessionOpenedPassword, nil))
		person := newTestKey(t)
		result, _, err := env.redeemCode(t, person, code)
		if err != nil || result.PersonKeyStatus != PersonKeyKept {
			t.Fatalf("redeem = %+v, %v", result, err)
		}
		claims := &Claims{UserID: user.ID, Role: user.Role, SessionID: result.Pair.SessionID, TokenType: TokenTypeAccess}
		stale := main
		stale.PINRevision--
		for name, acting := range map[string]ActingProfile{"a child profile": child, "the main profile at an old PIN": stale} {
			if err := env.signIn.ReplacePersonKey(ctx, claims, person.pub, acting); !errors.Is(err, ErrActingProfileChanged) {
				t.Fatalf("replace as %s = %v, want ErrActingProfileChanged", name, err)
			}
		}
		if err := env.signIn.ReplacePersonKey(ctx, claims, person.pub, main); err != nil {
			t.Fatalf("replace as the main profile: %v", err)
		}
		live, err := env.signIn.keys.LivePersonKey(ctx, user.ID)
		if err != nil || live.KeyID != person.pub.KeyID() {
			t.Fatalf("live person key = %+v, %v; want the device's %s", live, err, person.pub.KeyID())
		}
	})

	// A provider-bound person key whose identity is gone is dead: the
	// redeem revokes it and enrolls the body's key in its place.
	t.Run("a dead person key", func(t *testing.T) {
		user := env.localAccount(t, "appcode-dead", models.RoleUser)
		gone := int64(-1)
		dead, err := env.signIn.keys.InsertPersonKey(ctx, NewPersonKey{UserID: user.ID, PublicKey: newTestKey(t).pub,
			IdentityID: &gone, ProviderBound: true, EnrolledVia: models.AppKeyEnrolledSession})
		if err != nil {
			t.Fatal(err)
		}
		code := issue(t, env.loginSession(t, user, models.SessionOpenedPassword, nil))
		person := newTestKey(t)
		result, _, err := env.redeemCode(t, person, code)
		if err != nil || result.PersonKeyStatus != PersonKeyEnrolled {
			t.Fatalf("redeem = %+v, %v", result, err)
		}
		if !env.personKeyRevoked(t, dead.ID) {
			t.Fatal("the dead person key is still live")
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

// A redeem with a key this server holds already names that key, so the app
// knows to make it again rather than send the person back to the page. The
// refusal rolls the code back (only the challenge is spent), so the same code
// works on a fresh challenge with the key made again.
func TestRedeemAppCodeKeyEnrolledDB(t *testing.T) {
	env := newKeyEnv(t, "appcodekey")
	ctx := t.Context()
	otherPerson, otherDevice, _ := env.enroll(t, env.localAccount(t, "appcodekey-other", models.RoleUser))
	redeem := func(t *testing.T, code string, person, device testKey) error {
		t.Helper()
		_, err := env.signIn.RedeemAppCode(ctx, KeyRedeemRequest{
			KeyRequest: env.request(t, message{purpose: appkeys.PurposeRedeem, signer: person, device: &device, fields: map[string]any{"code": code}}),
			PersonKey:  person.pub,
		})
		return err
	}
	for _, tc := range []struct {
		name           string
		person, device func() testKey
		member         string
	}{
		{name: "device key", person: func() testKey { return newTestKey(t) }, device: func() testKey { return otherDevice }, member: KeyMemberDevice},
		{name: "person key", person: func() testKey { return otherPerson }, device: func() testKey { return newTestKey(t) }, member: KeyMemberPerson},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := env.localAccount(t, "appcodekey-"+tc.member, models.RoleUser)
			issued, err := env.signIn.IssueSessionAppCode(ctx, env.loginSession(t, user, models.SessionOpenedPassword, nil), ActingProfile{})
			if err != nil {
				t.Fatal(err)
			}
			err = redeem(t, issued.Code, tc.person(), tc.device())
			var enrolled *KeyEnrolledError
			if !errors.As(err, &enrolled) || enrolled.Member != tc.member {
				t.Fatalf("redeem = %v, want %s named as enrolled", err, tc.member)
			}
			if err := redeem(t, issued.Code, newTestKey(t), newTestKey(t)); err != nil {
				t.Fatalf("the same code with the keys made again: %v", err)
			}
		})
	}
}

// The hourly cleanup of expired app codes never waits for a row a request
// holds: issuing a code holds its invitation's own expired code while it
// deletes other expired codes for the name, so a cleanup that waited could
// deadlock with it. It skips the held row and deletes the rest.
func TestAppCodeCleanupSkipsHeldRowsDB(t *testing.T) {
	env := newKeyEnv(t, "appcodeclean")
	ctx := t.Context()
	user := env.localAccount(t, "appcodeclean-user", models.RoleUser)
	expired := func() []byte {
		_, hash, err := newAppCode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.pool.Exec(ctx, `INSERT INTO auth_app_codes (code_hash, kind, user_id, expires_at)
			VALUES ($1, $2, $3, NOW() - INTERVAL '1 minute')`, hash, appCodeKindSession, user.ID); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	held, other := expired(), expired()
	tx, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // nothing to keep
	if _, err := tx.Exec(ctx, `SELECT 1 FROM auth_app_codes WHERE code_hash = $1 FOR UPDATE`, held); err != nil {
		t.Fatal(err)
	}
	// A wait would block until the deadline; the cleanup must return first.
	cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := NewKeyChallenges(env.pool).DeleteExpiredChallenges(cleanupCtx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	var heldLeft, otherLeft bool
	if err := env.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_app_codes WHERE code_hash = $1),
		EXISTS (SELECT 1 FROM auth_app_codes WHERE code_hash = $2)`, held, other).Scan(&heldLeft, &otherLeft); err != nil {
		t.Fatal(err)
	}
	if !heldLeft || otherLeft {
		t.Fatalf("held row left %v (want true), other expired row left %v (want false)", heldLeft, otherLeft)
	}
}
