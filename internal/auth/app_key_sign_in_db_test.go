package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

const keyTestServerID = "96c1bd08-b839-4d47-980e-57d4e7a44cfa"

// testKey is a P-256 key a test signs with, as an app would.
type testKey struct {
	priv *ecdsa.PrivateKey
	pub  appkeys.PublicKey
}

func newTestKey(t *testing.T) testKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := priv.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := appkeys.PublicKeyFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{priv: priv, pub: pub}
}

// sign is the r || s signature over silo-sync-v1.<purpose>.<payload>.
func (k testKey) sign(t *testing.T, purpose appkeys.Purpose, payload string) string {
	t.Helper()
	digest := sha256.Sum256([]byte("silo-sync-v1." + string(purpose) + "." + payload))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return base64.RawURLEncoding.EncodeToString(sig)
}

// keyEnv is the re-check env (an account signed in through a provider, and
// an auth service whose refresh re-checks it) with app key sign-in on a
// fixed clock.
type keyEnv struct {
	*recheckEnv
	signIn *AppKeySignIn
	now    time.Time
}

func newKeyEnv(t *testing.T, label string) *keyEnv {
	t.Helper()
	env := &keyEnv{recheckEnv: newRecheckEnv(t, label, ""), now: time.Now().Truncate(time.Second)}
	env.signIn = NewAppKeySignIn(env.pool, env.svc)
	env.signIn.now = func() time.Time { return env.now }
	return env
}

// message is one signed request under construction: the payload fields, and
// who signs. Purpose is the signing purpose; the payload's purpose field
// follows it unless a test overrides it in fields.
type message struct {
	purpose appkeys.Purpose
	signer  testKey
	device  *testKey // signs device_sig and is the payload's device_key
	fields  map[string]any
}

// challenge issues a real challenge and returns it.
func (e *keyEnv) challenge(t *testing.T) string {
	t.Helper()
	challenge, _, err := e.signIn.IssueChallenge(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return challenge
}

// request signs m on a fresh challenge, unless m names one.
func (e *keyEnv) request(t *testing.T, m message) KeyRequest {
	t.Helper()
	reply := newTestKey(t)
	payload := map[string]any{
		"purpose": string(m.purpose), "server_id": keyTestServerID,
		"iat": e.now.Unix() - 10, "exp": e.now.Unix() + 110,
		"origin": "https://media.example.test", "reply_key": reply.pub.String(),
	}
	if m.device != nil {
		payload["device_key"] = m.device.pub.String()
		payload["device_name"] = "Phone"
		payload["device_platform"] = "ios"
	}
	for k, v := range m.fields {
		payload[k] = v
	}
	if _, ok := payload["challenge"]; !ok {
		payload["challenge"] = e.challenge(t)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	env := appkeys.Envelope{KeyID: m.signer.pub.KeyID(), Payload: encoded, Sig: m.signer.sign(t, m.purpose, encoded)}
	if m.device != nil {
		deviceSig := m.device.sign(t, m.purpose, encoded)
		env.DeviceSig = &deviceSig
	}
	return KeyRequest{Envelope: env, ServerID: keyTestServerID}
}

// session opens a login session of user, opened the way via says.
func (e *keyEnv) loginSession(t *testing.T, user *models.User, via string, identityID *int64) *Claims {
	t.Helper()
	session := models.AuthSession{ID: uuid.NewString(), UserID: user.ID, DeviceName: "browser",
		ExpiresAt: time.Now().Add(time.Hour), OpenedVia: via, IdentityID: identityID}
	if err := e.svc.sessions.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	return &Claims{UserID: user.ID, Role: user.Role, SessionID: session.ID, TokenType: TokenTypeAccess}
}

// enroll enrolls a person key and a device key for user from a password
// session and returns the keys.
func (e *keyEnv) enroll(t *testing.T, user *models.User) (testKey, testKey, *KeySignInResult) {
	t.Helper()
	person, device := newTestKey(t), newTestKey(t)
	claims := e.loginSession(t, user, models.SessionOpenedPassword, nil)
	result, err := e.signIn.Enroll(t.Context(), KeyEnrollRequest{
		KeyRequest: e.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device}),
		PersonKey:  person.pub, Claims: claims,
	})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	return person, device, result
}

func (e *keyEnv) sessionLive(t *testing.T, id string, userID int) bool {
	t.Helper()
	_, active, err := e.svc.sessions.ActiveSession(t.Context(), id, userID)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

// A challenge works once, on any node, and only before it expires; a
// request whose signature fails does not spend it, so a stranger can't burn
// a device's challenge.
func TestKeyChallengeRulesDB(t *testing.T) {
	env := newKeyEnv(t, "keychallenge")
	ctx := t.Context()
	user := env.localAccount(t, "keychallenge-user", models.RoleUser)
	_, device, _ := env.enroll(t, user)
	signIn := func(m message) error {
		_, err := env.signIn.SignIn(ctx, env.request(t, m))
		return err
	}
	base := message{purpose: appkeys.PurposeSignIn, signer: device}
	t.Run("replayed challenge", func(t *testing.T) {
		req := env.request(t, base)
		if _, err := env.signIn.SignIn(ctx, req); err != nil {
			t.Fatal(err)
		}
		if _, err := env.signIn.SignIn(ctx, req); !errors.Is(err, ErrKeyChallengeInvalid) {
			t.Fatalf("replay: %v", err)
		}
	})
	t.Run("expired challenge", func(t *testing.T) {
		challenge := env.challenge(t)
		raw, _ := base64.RawURLEncoding.DecodeString(challenge)
		sum := sha256.Sum256(raw)
		if _, err := env.pool.Exec(ctx, `UPDATE auth_key_challenges SET expires_at = NOW() - INTERVAL '1 second' WHERE challenge_hash = $1`, sum[:]); err != nil {
			t.Fatal(err)
		}
		if err := signIn(message{purpose: appkeys.PurposeSignIn, signer: device, fields: map[string]any{"challenge": challenge}}); !errors.Is(err, ErrKeyChallengeInvalid) {
			t.Fatalf("expired: %v", err)
		}
	})
	t.Run("a failed signature leaves the challenge", func(t *testing.T) {
		challenge := env.challenge(t)
		forged := env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device, fields: map[string]any{"challenge": challenge}})
		forged.Envelope.Sig = newTestKey(t).sign(t, appkeys.PurposeSignIn, forged.Envelope.Payload)
		if _, err := env.signIn.SignIn(ctx, forged); appkeys.CodeOf(err) != appkeys.CodeSignatureInvalid {
			t.Fatalf("forged: %v", err)
		}
		if err := signIn(message{purpose: appkeys.PurposeSignIn, signer: device, fields: map[string]any{"challenge": challenge}}); err != nil {
			t.Fatalf("the challenge was spent by a failed signature: %v", err)
		}
	})
}

// The signature rules every signed request passes: a payload for another
// server, for another purpose, outside its times or from a known-wrong
// origin is refused, each route takes only its own envelope, a public key is
// enrolled once, and a revoked key signs nothing in.
func TestKeySignInRefusalsDB(t *testing.T) {
	env := newKeyEnv(t, "keyrules")
	ctx := t.Context()
	user := env.localAccount(t, "keyrules-user", models.RoleUser)
	person, device, _ := env.enroll(t, user)
	signIn := func(m message) error {
		_, err := env.signIn.SignIn(ctx, env.request(t, m))
		return err
	}
	base := message{purpose: appkeys.PurposeSignIn, signer: device}

	for _, tc := range []struct {
		name string
		m    message
		code appkeys.Code
	}{
		{name: "wrong server_id", m: message{purpose: appkeys.PurposeSignIn, signer: device, fields: map[string]any{"server_id": uuid.NewString()}}, code: appkeys.CodeServerMismatch},
		{name: "expired message", m: message{purpose: appkeys.PurposeSignIn, signer: device, fields: map[string]any{"iat": env.now.Unix() - 200, "exp": env.now.Unix()}}, code: appkeys.CodeExpired},
		{name: "sign-in carrying device_sig", m: message{purpose: appkeys.PurposeSignIn, signer: device, device: &device}, code: appkeys.CodeEnvelopeMalformed},
		{name: "payload signed for another purpose", m: message{purpose: appkeys.PurposeSignIn, signer: device, fields: map[string]any{"purpose": "introduce"}}, code: appkeys.CodePurposeMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := signIn(tc.m); appkeys.CodeOf(err) != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
	t.Run("an introduce envelope at sign-in", func(t *testing.T) {
		other := newTestKey(t)
		req := env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &other})
		if _, err := env.signIn.SignIn(ctx, req); appkeys.CodeOf(err) != appkeys.CodeEnvelopeMalformed {
			t.Fatalf("introduce at sign-in: %v", err)
		}
	})
	t.Run("a known-wrong origin", func(t *testing.T) {
		req := env.request(t, base)
		req.CheckOrigin = func(string) error { return ErrKeyOriginMismatch }
		if _, err := env.signIn.SignIn(ctx, req); !errors.Is(err, ErrKeyOriginMismatch) {
			t.Fatalf("origin: %v", err)
		}
	})
	t.Run("introduce without a valid device_sig", func(t *testing.T) {
		other := newTestKey(t)
		req := env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &other})
		forged := newTestKey(t).sign(t, appkeys.PurposeIntroduce, req.Envelope.Payload)
		req.Envelope.DeviceSig = &forged
		if _, err := env.signIn.Introduce(ctx, req); appkeys.CodeOf(err) != appkeys.CodeDeviceSignatureInvalid {
			t.Fatalf("introduce: %v", err)
		}
	})
	t.Run("an enrolled public key enrolls again", func(t *testing.T) {
		req := env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &device})
		if _, err := env.signIn.Introduce(ctx, req); !errors.Is(err, ErrAppKeyAlreadyEnrolled) {
			t.Fatalf("re-enroll: %v", err)
		}
	})
	t.Run("revoked keys", func(t *testing.T) {
		introduced := newTestKey(t)
		result, err := env.signIn.Introduce(ctx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &introduced}))
		if err != nil || result.Status != appkeys.ReplySignedIn || result.Pair == nil {
			t.Fatalf("introduce = %+v, %v", result, err)
		}
		if err := env.signIn.keys.RevokeDeviceKey(ctx, user.ID, result.DeviceID); err != nil {
			t.Fatal(err)
		}
		if env.sessionLive(t, result.Pair.SessionID, user.ID) {
			t.Fatal("the revoked key's session is still live")
		}
		if err := signIn(message{purpose: appkeys.PurposeSignIn, signer: introduced}); !errors.Is(err, ErrKeySignatureInvalid) {
			t.Fatalf("revoked device key: %v", err)
		}
		if err := env.signIn.keys.RevokePersonKey(ctx, user.ID); err != nil {
			t.Fatal(err)
		}
		other := newTestKey(t)
		if _, err := env.signIn.Introduce(ctx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &other})); !errors.Is(err, ErrKeySignatureInvalid) {
			t.Fatalf("revoked person key: %v", err)
		}
		if err := signIn(base); err != nil {
			t.Fatalf("revoking the person key stopped a device already in: %v", err)
		}
	})
}

// An administrator's new device waits for approval: introduce stores its key
// waiting and answers approval_required with no tokens, the device may ask
// again no sooner than every five seconds, and after a day, or once denied,
// it is refused.
func TestKeySignInAdminWaitsForApprovalDB(t *testing.T) {
	env := newKeyEnv(t, "keyadmin")
	ctx := t.Context()
	admin := env.localAccount(t, "keyadmin-admin", models.RoleAdmin)
	person, _, _ := env.enroll(t, admin)
	waiting := newTestKey(t)
	result, err := env.signIn.Introduce(ctx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &waiting}))
	if err != nil || result.Status != appkeys.ReplyApprovalRequired || result.Pair != nil || result.DeviceID == "" {
		t.Fatalf("introduce = %+v, %v", result, err)
	}
	sessions := func() int {
		var n int
		if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE device_key_id = $1`, result.DeviceID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	signIn := func() (*KeySignInResult, error) {
		return env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: waiting}))
	}
	var retry *KeyRetryError
	if _, err := signIn(); !errors.As(err, &retry) || retry.RetryAfter <= 0 || retry.RetryAfter > appkeys.PendingRetryInterval {
		t.Fatalf("asking again at once = %v", err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE auth_device_keys SET last_used_at = NOW() - INTERVAL '6 seconds' WHERE id = $1`, result.DeviceID); err != nil {
		t.Fatal(err)
	}
	again, err := signIn()
	if err != nil || again.Status != appkeys.ReplyApprovalRequired || again.Pair != nil || sessions() != 0 {
		t.Fatalf("waiting sign-in = %+v, %v, %d sessions", again, err, sessions())
	}
	if _, err := env.pool.Exec(ctx, `UPDATE auth_device_keys SET created_at = NOW() - INTERVAL '25 hours', last_used_at = NULL WHERE id = $1`, result.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(); !errors.Is(err, ErrAppKeyDenied) {
		t.Fatalf("after a day = %v", err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE auth_device_keys SET created_at = NOW(), status = 'denied' WHERE id = $1`, result.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(); !errors.Is(err, ErrAppKeyDenied) {
		t.Fatalf("denied = %v", err)
	}
}

// The session rule: changes that hand out lasting access are refused from a
// session a device sign-in opened, from one another app's key opened, and
// from one older than the record; approving a device sign-in refuses only
// the app's.
func TestSessionAllowsDB(t *testing.T) {
	env := newKeyEnv(t, "keyrule")
	ctx := t.Context()
	user := env.localAccount(t, "keyrule-user", models.RoleUser)
	_, _, enrolled := env.enroll(t, user)
	grant, err := env.signIn.keys.InsertDeviceKey(ctx, NewDeviceKey{UserID: user.ID, PublicKey: newTestKey(t).pub,
		Kind: models.DeviceKeyKindApp, Name: "Other app", EnrolledVia: models.AppKeyEnrolledGrant})
	if err != nil {
		t.Fatal(err)
	}
	grantSession := env.loginSession(t, user, models.SessionOpenedAppKey, nil)
	if _, err := env.pool.Exec(ctx, `UPDATE auth_sessions SET device_key_id = $2 WHERE id = $1`, grantSession.SessionID, grant.ID); err != nil {
		t.Fatal(err)
	}
	keySession := &Claims{UserID: user.ID, SessionID: enrolled.Pair.SessionID, TokenType: TokenTypeAccess}
	for _, tc := range []struct {
		name            string
		claims          *Claims
		manage, approve bool
	}{
		{name: "password", claims: env.loginSession(t, user, models.SessionOpenedPassword, nil), manage: true, approve: true},
		{name: "own app key", claims: keySession, manage: true, approve: true},
		{name: "device sign-in", claims: env.loginSession(t, user, models.SessionOpenedDeviceLogin, nil), approve: true},
		{name: "older than the record", claims: env.loginSession(t, user, "", nil), approve: true},
		{name: "another app's key", claims: grantSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := SessionAllows(ctx, env.pool, tc.claims, ActionManageKeys); (err == nil) != tc.manage {
				t.Fatalf("manage keys: %v", err)
			}
			if err := SessionAllows(ctx, env.pool, tc.claims, ActionApproveDeviceSignIn); (err == nil) != tc.approve {
				t.Fatalf("approve a device: %v", err)
			}
		})
	}
}

// enroll and the person key replacement give lasting access, so a TV's
// session (device sign-in) can't make them; a different person key already
// on the account is a conflict.
func TestKeyEnrollRulesDB(t *testing.T) {
	env := newKeyEnv(t, "keyenroll")
	ctx := t.Context()
	user := env.localAccount(t, "keyenroll-user", models.RoleUser)
	person, _, result := env.enroll(t, user)
	if result.PersonKeyID != person.pub.KeyID() || result.Pair == nil || !env.sessionLive(t, result.Pair.SessionID, user.ID) {
		t.Fatalf("enroll = %+v", result)
	}
	tv := env.loginSession(t, user, models.SessionOpenedDeviceLogin, nil)
	device := newTestKey(t)
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
		KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device}), PersonKey: person.pub, Claims: tv,
	}); !errors.Is(err, ErrSessionNotAllowed) {
		t.Fatalf("enroll from device sign-in = %v", err)
	}
	if err := env.signIn.ReplacePersonKey(ctx, tv, newTestKey(t).pub); !errors.Is(err, ErrSessionNotAllowed) {
		t.Fatalf("replace from device sign-in = %v", err)
	}
	browser := env.loginSession(t, user, models.SessionOpenedPassword, nil)
	other := newTestKey(t)
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
		KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: other, device: &device}), PersonKey: other.pub, Claims: browser,
	}); !errors.Is(err, ErrPersonKeyConflict) {
		t.Fatalf("enroll a second person key = %v", err)
	}
	enrollAsIntroduce := env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &device})
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{KeyRequest: enrollAsIntroduce, PersonKey: person.pub, Claims: browser}); appkeys.CodeOf(err) != appkeys.CodeSignatureInvalid {
		t.Fatalf("an introduce envelope at enroll = %v", err)
	}
	replacement := newTestKey(t)
	if err := env.signIn.ReplacePersonKey(ctx, browser, replacement.pub); err != nil {
		t.Fatal(err)
	}
	keys, err := env.signIn.ListOwnKeys(ctx, browser)
	if err != nil || keys.Person == nil || keys.Person.KeyID != replacement.pub.KeyID() || len(keys.Devices) != 1 {
		t.Fatalf("keys after replacement = %+v, %v", keys, err)
	}
}

// Who may revoke a device key: a device its own (which ends the session
// asking), another app only its own, a TV session none.
func TestRevokeOwnDeviceKeyRulesDB(t *testing.T) {
	env := newKeyEnv(t, "keyrevoke")
	ctx := t.Context()
	user := env.localAccount(t, "keyrevoke-user", models.RoleUser)
	_, _, enrolled := env.enroll(t, user)
	grant, err := env.signIn.keys.InsertDeviceKey(ctx, NewDeviceKey{UserID: user.ID, PublicKey: newTestKey(t).pub,
		Kind: models.DeviceKeyKindApp, Name: "Other app", EnrolledVia: models.AppKeyEnrolledGrant})
	if err != nil {
		t.Fatal(err)
	}
	grantSession := env.loginSession(t, user, models.SessionOpenedAppKey, nil)
	if _, err := env.pool.Exec(ctx, `UPDATE auth_sessions SET device_key_id = $2 WHERE id = $1`, grantSession.SessionID, grant.ID); err != nil {
		t.Fatal(err)
	}
	tv := env.loginSession(t, user, models.SessionOpenedDeviceLogin, nil)
	if err := env.signIn.RevokeOwnDeviceKey(ctx, tv, enrolled.DeviceID); !errors.Is(err, ErrSessionNotAllowed) {
		t.Fatalf("a TV revoking = %v", err)
	}
	if err := env.signIn.RevokeOwnDeviceKey(ctx, grantSession, enrolled.DeviceID); !errors.Is(err, ErrSessionNotAllowed) {
		t.Fatalf("another app revoking the person's device = %v", err)
	}
	self := &Claims{UserID: user.ID, SessionID: enrolled.Pair.SessionID, TokenType: TokenTypeAccess}
	if err := env.signIn.RevokeOwnDeviceKey(ctx, self, enrolled.DeviceID); err != nil {
		t.Fatal(err)
	}
	if env.sessionLive(t, enrolled.Pair.SessionID, user.ID) {
		t.Fatal("a device revoking its own key kept its session")
	}
	if err := env.signIn.RevokeOwnDeviceKey(ctx, grantSession, grant.ID); err != nil {
		t.Fatalf("an app revoking its own key = %v", err)
	}
}

// A key that is not provider-bound is a local credential: while local
// password sign-in is off it signs in only for a break-glass account.
func TestKeySignInLocalPasswordsOffDB(t *testing.T) {
	env := newKeyEnv(t, "keylocal")
	ctx := t.Context()
	user := env.localAccount(t, "keylocal-user", models.RoleUser)
	_, device, _ := env.enroll(t, user)
	breakGlass := env.localAccount(t, "keylocal-admin", models.RoleAdmin)
	if _, err := env.pool.Exec(ctx, `UPDATE users SET break_glass = true WHERE id = $1`, breakGlass.ID); err != nil {
		t.Fatal(err)
	}
	_, adminDevice, _ := env.enroll(t, breakGlass)
	if _, err := env.pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'false')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthLocalPasswordLoginSettingKey); err != nil {
		t.Fatal(err)
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrLocalLoginDisabled) {
		t.Fatalf("local key with local passwords off = %v", err)
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: adminDevice})); err != nil {
		t.Fatalf("break-glass key with local passwords off = %v", err)
	}
}

// A provider-bound key inherits the provider checks of the session that
// enrolled it. Its session continues the identity with provider_since at the
// identity's last vouched time, not the sign-in time, so a key sign-in never
// restarts the age of a chain the provider can't re-check: past that age the
// key is refused until the person signs in through the provider again.
func TestKeySessionContinuesProviderChainDB(t *testing.T) {
	env := newKeyEnv(t, "keychain")
	t.Cleanup(saveSettings(t, env.pool, config.AuthRefreshTokenExpirySettingKey))
	ctx := t.Context()
	if _, err := env.pool.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, config.AuthRefreshTokenExpirySettingKey); err != nil {
		t.Fatal(err)
	}
	vouched := time.Now().Add(-10 * 24 * time.Hour).Truncate(time.Second)
	setVouched := func(at time.Time) {
		t.Helper()
		if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_identities SET last_authenticated_at = $2 WHERE id = $1`, env.identityID, at); err != nil {
			t.Fatal(err)
		}
	}
	setVouched(vouched)

	person, device := newTestKey(t), newTestKey(t)
	claims := env.loginSession(t, env.user, models.SessionOpenedProvider, &env.identityID)
	enrolled, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
		KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device}),
		PersonKey:  person.pub, Claims: claims,
	})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := env.signIn.keys.ListAccountKeys(ctx, env.user.ID)
	if err != nil || keys.Person == nil || !keys.Person.ProviderBound || len(keys.Devices) != 1 ||
		keys.Devices[0].IdentityID == nil || *keys.Devices[0].IdentityID != env.identityID {
		t.Fatalf("enrolled keys = %+v, %v", keys, err)
	}
	session := env.sessionRow(t, enrolled.Pair.SessionID)
	if session.IdentityID == nil || *session.IdentityID != env.identityID || session.ProviderSince == nil || !session.ProviderSince.Equal(vouched) {
		t.Fatalf("key session chain = identity %v since %v, want %d since %v", session.IdentityID, session.ProviderSince, env.identityID, vouched)
	}

	// The provider can't re-check, and last vouched past the refresh
	// lifetime: the key is refused as a refresh of that chain would be.
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, "")
	env.makeDue(t)
	setVouched(time.Now().Add(-recheckRefreshExpiry - time.Hour))
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrProviderSignInRequired) {
		t.Fatalf("key sign-in past the provider's age = %v", err)
	}
	// Signing in through the provider again moves the anchor; the key
	// works again without enrolling anew, within the new age.
	setVouched(time.Now())
	result, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device}))
	if err != nil {
		t.Fatalf("key sign-in after the provider vouched again = %v", err)
	}
	if expires := env.sessionRow(t, result.Pair.SessionID).ExpiresAt; expires.After(time.Now().Add(recheckRefreshExpiry).Add(time.Minute)) {
		t.Fatalf("key session expires %v, past the provider's age", expires)
	}
}

// Becoming an admin revokes the account's app keys with its API keys, and
// ends the sessions they opened.
func TestPromotionRevokesAppKeysDB(t *testing.T) {
	env := newKeyEnv(t, "keypromote")
	ctx := t.Context()
	user := env.localAccount(t, "keypromote-user", models.RoleUser)
	_, device, enrolled := env.enroll(t, user)
	admin := models.RoleAdmin
	if err := NewUserRepository(env.pool).Update(ctx, user.ID, models.UpdateUserInput{Role: &admin}); err != nil {
		t.Fatal(err)
	}
	if env.sessionLive(t, enrolled.Pair.SessionID, user.ID) {
		t.Fatal("the promoted account's key session is still live")
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrKeySignatureInvalid) {
		t.Fatalf("a key from before the promotion = %v", err)
	}
}
