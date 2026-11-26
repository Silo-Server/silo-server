package auth

import (
	"context"
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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
	signIn := func(t *testing.T, m message) error {
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
		if err := signIn(t, message{purpose: appkeys.PurposeSignIn, signer: device, fields: map[string]any{"challenge": challenge}}); !errors.Is(err, ErrKeyChallengeInvalid) {
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
		if err := signIn(t, message{purpose: appkeys.PurposeSignIn, signer: device, fields: map[string]any{"challenge": challenge}}); err != nil {
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
	signIn := func(t *testing.T, m message) error {
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
		// Names reach the database and an administrator's screen: a NUL would
		// fail the insert after the challenge is spent.
		{name: "a NUL in device_name", m: message{purpose: appkeys.PurposeIntroduce, signer: person, device: &[]testKey{newTestKey(t)}[0], fields: map[string]any{"device_name": "a\x00b"}}, code: appkeys.CodeFieldInvalid},
		{name: "an escape in device_platform", m: message{purpose: appkeys.PurposeIntroduce, signer: person, device: &[]testKey{newTestKey(t)}[0], fields: map[string]any{"device_platform": "ios\x1b"}}, code: appkeys.CodeFieldInvalid},
		{name: "a DEL in device_name", m: message{purpose: appkeys.PurposeIntroduce, signer: person, device: &[]testKey{newTestKey(t)}[0], fields: map[string]any{"device_name": "a\x7f"}}, code: appkeys.CodeFieldInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.m.purpose == appkeys.PurposeIntroduce {
				_, err = env.signIn.Introduce(ctx, env.request(t, tc.m))
			} else {
				err = signIn(t, tc.m)
			}
			if appkeys.CodeOf(err) != tc.code {
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
		if err := signIn(t, message{purpose: appkeys.PurposeSignIn, signer: introduced}); !errors.Is(err, ErrKeySignatureInvalid) {
			t.Fatalf("revoked device key: %v", err)
		}
		if err := env.signIn.keys.RevokePersonKey(ctx, user.ID); err != nil {
			t.Fatal(err)
		}
		other := newTestKey(t)
		if _, err := env.signIn.Introduce(ctx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &other})); !errors.Is(err, ErrKeySignatureInvalid) {
			t.Fatalf("revoked person key: %v", err)
		}
		if err := signIn(t, base); err != nil {
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
	// A waiting key never opened a session, so it shows as never used,
	// however often it asks.
	listed := func() *models.AppDeviceKey {
		t.Helper()
		keys, err := env.signIn.ListOwnKeys(ctx, env.loginSession(t, admin, models.SessionOpenedPassword, nil))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys.Devices {
			if key.ID == result.DeviceID {
				return key
			}
		}
		return nil
	}
	neverUsed := func(when string) {
		t.Helper()
		key := listed()
		if key == nil {
			t.Fatalf("%s: the waiting key is not listed", when)
		}
		if key.LastUsedAt != nil {
			t.Fatalf("%s: a waiting key shows last_used_at %v", when, key.LastUsedAt)
		}
	}
	neverUsed("after introduce")
	var retry *KeyRetryError
	if _, err := signIn(); !errors.As(err, &retry) || retry.RetryAfter <= 0 || retry.RetryAfter > appkeys.PendingRetryInterval {
		t.Fatalf("asking again at once = %v", err)
	}
	neverUsed("after a throttled sign-in")
	if _, err := env.pool.Exec(ctx, `UPDATE auth_device_keys SET last_attempt_at = NOW() - INTERVAL '6 seconds' WHERE id = $1`, result.DeviceID); err != nil {
		t.Fatal(err)
	}
	again, err := signIn()
	if err != nil || again.Status != appkeys.ReplyApprovalRequired || again.Pair != nil || sessions() != 0 {
		t.Fatalf("waiting sign-in = %+v, %v, %d sessions", again, err, sessions())
	}
	neverUsed("after an allowed sign-in")
	if _, err := env.pool.Exec(ctx, `UPDATE auth_device_keys SET created_at = NOW() - INTERVAL '25 hours', last_attempt_at = NULL WHERE id = $1`, result.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(); !errors.Is(err, ErrAppKeyDenied) {
		t.Fatalf("after a day = %v", err)
	}
	// Past its lifetime the waiting key is refused, and no longer listed.
	if key := listed(); key != nil {
		t.Fatalf("a waiting key past its lifetime is listed: %+v", key)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE auth_device_keys SET created_at = NOW(), status = 'denied' WHERE id = $1`, result.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(); !errors.Is(err, ErrAppKeyDenied) {
		t.Fatalf("denied = %v", err)
	}
}

// The session rule: changes that hand out lasting access, and reading the
// key list, are refused from a session a device sign-in opened, from one
// another app's key opened, and from one older than the record; approving a
// device sign-in refuses only the app's. An API key, an administrator acting
// as the account and a remote-playback session are refused everything.
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
	impersonated := *env.loginSession(t, user, models.SessionOpenedPassword, nil)
	impersonator := user.ID + 1
	impersonated.ImpersonatorUserID = &impersonator
	remote := *env.loginSession(t, user, models.SessionOpenedPassword, nil)
	remote.SessionPurpose = models.SessionPurposeRemotePlayback
	for _, tc := range []struct {
		name                  string
		claims                *Claims
		manage, approve, read bool
	}{
		{name: "password", claims: env.loginSession(t, user, models.SessionOpenedPassword, nil), manage: true, approve: true, read: true},
		{name: "provider", claims: env.loginSession(t, user, models.SessionOpenedProvider, nil), manage: true, approve: true, read: true},
		{name: "own app key", claims: keySession, manage: true, approve: true, read: true},
		{name: "device sign-in", claims: env.loginSession(t, user, models.SessionOpenedDeviceLogin, nil), approve: true},
		{name: "older than the record", claims: env.loginSession(t, user, "", nil), approve: true},
		{name: "another app's key", claims: grantSession},
		{name: "administrator acting as the account", claims: &impersonated},
		{name: "API key", claims: &Claims{UserID: user.ID, TokenType: TokenTypeAPIKey}},
		{name: "remote playback", claims: &remote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed := func(action SessionAction, want bool) {
				t.Helper()
				err := SessionAllows(ctx, env.pool, tc.claims, action)
				if want && err != nil || !want && !errors.Is(err, ErrSessionNotAllowed) {
					t.Fatalf("action %d: err = %v, want allowed %v", action, err, want)
				}
			}
			allowed(ActionManageKeys, tc.manage)
			allowed(ActionApproveDeviceSignIn, tc.approve)
			allowed(ActionReadKeys, tc.read)
			// A personal API key or a linked sign-in is refused only to
			// another app's session. The operations' own rules decide the
			// rest, as before app keys: they already keep out an API key,
			// an administrator acting as the account and remote playback.
			refused := tc.claims == grantSession
			if err := SessionMayIssueCredential(ctx, env.pool, tc.claims); !refused && err != nil || refused && !errors.Is(err, ErrSessionNotAllowed) {
				t.Fatalf("issuing a credential: err = %v, want refused %v", err, refused)
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
	if err := env.signIn.ReplacePersonKey(ctx, tv, newTestKey(t).pub, ActingProfile{}); !errors.Is(err, ErrSessionNotAllowed) {
		t.Fatalf("replace from device sign-in = %v", err)
	}
	browser := env.loginSession(t, user, models.SessionOpenedPassword, nil)
	forged := env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device})
	forgedSig := newTestKey(t).sign(t, appkeys.PurposeEnroll, forged.Envelope.Payload)
	forged.Envelope.DeviceSig = &forgedSig
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{KeyRequest: forged, PersonKey: person.pub, Claims: browser}); appkeys.CodeOf(err) != appkeys.CodeDeviceSignatureInvalid {
		t.Fatalf("enroll without a valid device_sig = %v", err)
	}
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
	if err := env.signIn.ReplacePersonKey(ctx, browser, replacement.pub, ActingProfile{}); err != nil {
		t.Fatal(err)
	}
	keys, err := env.signIn.ListOwnKeys(ctx, browser)
	if err != nil || keys.Person == nil || keys.Person.KeyID != replacement.pub.KeyID() || len(keys.Devices) != 1 {
		t.Fatalf("keys after replacement = %+v, %v", keys, err)
	}
	// An account with no username (a later join creates them) still lists.
	if _, err := env.pool.Exec(ctx, `UPDATE users SET username = NULL WHERE id = $1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.signIn.ListOwnKeys(ctx, browser); err != nil {
		t.Fatalf("list keys of an account with no username = %v", err)
	}
	// A session whose provider identity is gone (identity_id NULL,
	// provider_since set) can't make a person key bound to nothing.
	gone := env.loginSession(t, user, models.SessionOpenedProvider, nil)
	if _, err := env.pool.Exec(ctx, `UPDATE auth_sessions SET provider_since = NOW() WHERE id = $1`, gone.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := env.signIn.ReplacePersonKey(ctx, gone, newTestKey(t).pub, ActingProfile{}); !errors.Is(err, ErrProviderSignInRequired) {
		t.Fatalf("replace from a session whose identity is gone = %v", err)
	}
}

// A signature that verifies spends its challenge even when the kid beside it
// names another key: the signer committed to the challenge.
func TestKeyChallengeSpentOnKidMismatchDB(t *testing.T) {
	env := newKeyEnv(t, "keykid")
	ctx := t.Context()
	user := env.localAccount(t, "keykid-user", models.RoleUser)
	person, device := newTestKey(t), newTestKey(t)
	claims := env.loginSession(t, user, models.SessionOpenedPassword, nil)
	challenge := env.challenge(t)
	req := env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device, fields: map[string]any{"challenge": challenge}})
	req.Envelope.KeyID = newTestKey(t).pub.KeyID()
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{KeyRequest: req, PersonKey: person.pub, Claims: claims}); appkeys.CodeOf(err) != appkeys.CodeKidMismatch {
		t.Fatalf("enroll under another kid = %v", err)
	}
	retry := env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device, fields: map[string]any{"challenge": challenge}})
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{KeyRequest: retry, PersonKey: person.pub, Claims: claims}); !errors.Is(err, ErrKeyChallengeInvalid) {
		t.Fatalf("the same challenge again = %v, want it spent", err)
	}
}

// A provider-bound key's session limit, decided before the request waited on
// the account lock, may pass meanwhile: the sign-in is then refused before a
// session row exists or the key is marked used.
func TestKeySessionRefusedPastItsLimitDB(t *testing.T) {
	env := newKeyEnv(t, "keylimit")
	ctx := t.Context()
	_, enrolled := env.enrollFromProvider(t)
	err := pgx.BeginFunc(ctx, env.pool, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, env.user.ID)
		if err != nil {
			return err
		}
		key, err := lockDeviceKey(ctx, tx, user.ID, enrolled.DeviceID)
		if err != nil {
			return err
		}
		_, err = env.svc.openKeySessionTx(ctx, tx, user, key, KeySession{Expiry: time.Now().Add(-time.Second), IdentityID: &env.identityID})
		return err
	})
	if !errors.Is(err, ErrProviderSignInRequired) {
		t.Fatalf("open a session past its limit = %v", err)
	}
	var sessions int
	if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE device_key_id = $1`, enrolled.DeviceID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("sessions of the key = %d, %v; want only the enrolling one", sessions, err)
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
	// works again without enrolling anew. The provider still can't
	// re-check, so the session ends the refresh lifetime after the anchor,
	// not after the sign-in: with the anchor ten days back the two differ by
	// ten days.
	again := time.Now().Add(-10 * 24 * time.Hour).Truncate(time.Second)
	setVouched(again)
	result, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device}))
	if err != nil {
		t.Fatalf("key sign-in after the provider vouched again = %v", err)
	}
	if expires, want := env.sessionRow(t, result.Pair.SessionID).ExpiresAt, again.Add(recheckRefreshExpiry); expires.Sub(want).Abs() > time.Second {
		t.Fatalf("key session expires %v, want the provider's limit %v", expires, want)
	}
}

// Becoming an admin revokes the account's app keys with its API keys, and
// ends the sessions they opened: a key the account enrolled from a session,
// and one another app enrolled through a grant. Regression: nothing pinned
// that the revocation ignores how a key was enrolled, so a filter on the
// key's kind would let another app's key outlive the promotion.
func TestPromotionRevokesAppKeysDB(t *testing.T) {
	env := newKeyEnv(t, "keypromote")
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		// key enrolls an app key on user and answers it with the session
		// it opened.
		key func(t *testing.T, user *models.User) (testKey, *KeySignInResult)
	}{
		{name: "session", key: func(t *testing.T, user *models.User) (testKey, *KeySignInResult) {
			_, device, enrolled := env.enroll(t, user)
			return device, enrolled
		}},
		{name: "grant", key: func(t *testing.T, user *models.User) (testKey, *KeySignInResult) {
			person, _, _ := env.enroll(t, user)
			app := newTestKey(t)
			redeemed, err := env.redeem(t, app, env.grantEnvelope(t, person, user.ID, app, nil))
			if err != nil {
				t.Fatalf("redeem: %v", err)
			}
			var via string
			if err := env.pool.QueryRow(ctx, `SELECT enrolled_via FROM auth_device_keys WHERE id = $1`, redeemed.DeviceID).Scan(&via); err != nil || via != models.AppKeyEnrolledGrant {
				t.Fatalf("the redeemed key was enrolled via %q (%v)", via, err)
			}
			return app, redeemed
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := env.localAccount(t, "keypromote-"+tc.name, models.RoleUser)
			device, enrolled := tc.key(t, user)
			admin := models.RoleAdmin
			if err := NewUserRepository(env.pool).Update(ctx, user.ID, models.UpdateUserInput{Role: &admin}); err != nil {
				t.Fatal(err)
			}
			if !env.keyRevoked(t, enrolled.DeviceID) {
				t.Fatal("the promoted account's key is not revoked")
			}
			if env.sessionLive(t, enrolled.Pair.SessionID, user.ID) {
				t.Fatal("the promoted account's key session is still live")
			}
			if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrKeySignatureInvalid) {
				t.Fatalf("a key from before the promotion = %v", err)
			}
		})
	}
}

// enrollFromProvider enrolls keys for the env's provider account from a
// session opened through its identity.
func (e *keyEnv) enrollFromProvider(t *testing.T) (testKey, *KeySignInResult) {
	t.Helper()
	person, device := newTestKey(t), newTestKey(t)
	claims := e.loginSession(t, e.user, models.SessionOpenedProvider, &e.identityID)
	result, err := e.signIn.Enroll(t.Context(), KeyEnrollRequest{
		KeyRequest: e.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device}),
		PersonKey:  person.pub, Claims: claims,
	})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	return device, result
}

// keyRevoked reports whether the device key with id is revoked.
func (e *keyEnv) keyRevoked(t *testing.T, id string) bool {
	t.Helper()
	var revoked bool
	if err := e.pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM auth_device_keys WHERE id = $1`, id).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	return revoked
}

// Linking a provider to an account brings the keys it enrolled from a
// password session under the provider: the next key session carries the
// identity and its last vouched time, and is refused once the provider can't
// re-check and vouched too long ago. Without it such a key would open
// sessions no provider ever checks.
func TestLinkingBindsAppKeysDB(t *testing.T) {
	env := newKeyEnv(t, "keylink")
	t.Cleanup(saveSettings(t, env.pool, config.AuthRefreshTokenExpirySettingKey))
	ctx := t.Context()
	if _, err := env.pool.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, config.AuthRefreshTokenExpirySettingKey); err != nil {
		t.Fatal(err)
	}
	user := env.localAccount(t, "keylink-user", models.RoleUser)
	_, device, _ := env.enroll(t, user)
	_, identityID, err := env.resolver.Resolve(ctx, ResolveInput{InstallationID: env.installationID, Identity: env.identity("keylink-identity"), LinkingUserID: user.ID})
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	var vouched time.Time
	if err := env.pool.QueryRow(ctx, `SELECT COALESCE(last_authenticated_at, linked_at) FROM plugin_auth_identities WHERE id = $1`, identityID).Scan(&vouched); err != nil {
		t.Fatal(err)
	}
	result, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device}))
	if err != nil {
		t.Fatalf("key sign-in after linking = %v", err)
	}
	session := env.sessionRow(t, result.Pair.SessionID)
	if session.IdentityID == nil || *session.IdentityID != identityID || session.ProviderSince == nil || !session.ProviderSince.Equal(vouched) {
		t.Fatalf("key session after linking = identity %v since %v, want %d since %v", session.IdentityID, session.ProviderSince, identityID, vouched)
	}
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, "")
	if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_identities SET last_checked_at = NOW() - INTERVAL '13 hours',
		last_authenticated_at = NOW() - $2::interval WHERE id = $1`, identityID, (recheckRefreshExpiry + time.Hour).String()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrProviderSignInRequired) {
		t.Fatalf("key sign-in past the provider's age after linking = %v", err)
	}
}

// A provider refusing the person revokes the account's app keys with its
// API keys, and ends the sessions they opened.
func TestProviderRefusalRevokesAppKeysDB(t *testing.T) {
	env := newKeyEnv(t, "keyrefuse")
	ctx := t.Context()
	device, enrolled := env.enrollFromProvider(t)
	providerSession := env.loginSession(t, env.user, models.SessionOpenedProvider, &env.identityID)
	pair, err := env.svc.generateTokenPair(*providerSession)
	if err != nil {
		t.Fatal(err)
	}
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_DISABLED, "")
	env.makeDue(t)
	if _, err := env.svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refresh after the refusal = %v", err)
	}
	if !env.keyRevoked(t, enrolled.DeviceID) || env.sessionLive(t, enrolled.Pair.SessionID, env.user.ID) {
		t.Fatal("the refused person's key or its session survived")
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrKeySignatureInvalid) {
		t.Fatalf("key sign-in after the refusal = %v", err)
	}
}

// keyedAccount gives the env's provider account keys bound to its identity,
// a live session opened by the device key, and a device sign-in approved in
// advance from that session. It returns the enrollment and the pre-approval.
func (e *keyEnv) keyedAccount(t *testing.T) (*KeySignInResult, string) {
	t.Helper()
	_, enrolled := e.enrollFromProvider(t)
	devices := NewDeviceLoginService(e.pool, NewUserRepository(e.pool), e.jwt, NewSessionRepository(e.pool), nil, nil)
	_, binding := preapprovalSecret(t)
	ctx := WithClaims(t.Context(), &Claims{UserID: e.user.ID, Role: e.user.Role, SessionID: enrolled.Pair.SessionID, TokenType: TokenTypeAccess})
	preapproved, err := devices.Preapprove(ctx, DevicePreapprovalInput{DeviceName: "TV " + e.suffix, Binding: binding}, e.user.ID)
	if err != nil {
		t.Fatalf("preapprove from the key session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM device_login_requests WHERE id = $1`, preapproved.ID)
	})
	return enrolled, preapproved.ID
}

// An account holding provider-bound keys, a key session, a pre-approval
// made from it, and a second identity can be deleted, and nothing of it is
// left in the key, session or device sign-in tables.
func TestAccountWithKeysCanBeDeletedDB(t *testing.T) {
	env := newKeyEnv(t, "keydelete")
	ctx := t.Context()
	env.keyedAccount(t)
	var otherInstallation int
	if err := env.pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/key-delete-test', true, 'manual', 'plugin') RETURNING id`, "key-delete-"+env.suffix).Scan(&otherInstallation); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM plugin_installations WHERE id = $1`, otherInstallation)
	})
	var otherIdentity int64
	if err := env.pool.QueryRow(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3) RETURNING id`, otherInstallation, "other|"+env.suffix, env.user.ID).Scan(&otherIdentity); err != nil {
		t.Fatal(err)
	}
	other, err := env.signIn.keys.InsertDeviceKey(ctx, NewDeviceKey{UserID: env.user.ID, PublicKey: newTestKey(t).pub,
		Kind: models.DeviceKeyKindDevice, Name: "Tablet", EnrolledVia: models.AppKeyEnrolledSession, IdentityID: &otherIdentity})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.svc.sessions.Create(ctx, models.AuthSession{UserID: env.user.ID, DeviceName: "Tablet",
		ExpiresAt: time.Now().Add(time.Hour), DeviceKeyID: &other.ID, IdentityID: &otherIdentity, OpenedVia: models.SessionOpenedAppKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewUserRepository(env.pool).MutateAdminAccount(ctx, env.user.ID, -1, nil,
		func(*models.User, pgx.Tx) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("delete an account with keys: %v", err)
	}
	for _, table := range []string{"auth_person_keys", "auth_device_keys", "auth_sessions", "plugin_auth_identities"} {
		var left int
		if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE user_id = $1`, env.user.ID).Scan(&left); err != nil || left != 0 {
			t.Fatalf("%s rows left = %d, %v", table, left, err)
		}
	}
	var requests int
	if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM device_login_requests WHERE approved_by_user_id = $1`, env.user.ID).Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("device sign-in requests still naming the account = %d, %v", requests, err)
	}
}

// otherIdentity links a second identity, on its own plugin installation, to
// the env's account and returns its ID and installation.
func (e *keyEnv) otherIdentity(t *testing.T, label string) (int64, int) {
	t.Helper()
	ctx := t.Context()
	var installation int
	if err := e.pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/key-identity-test', true, 'manual', 'plugin') RETURNING id`, label+"-"+e.suffix).Scan(&installation); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM plugin_installations WHERE id = $1`, installation)
	})
	var identity int64
	if err := e.pool.QueryRow(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3) RETURNING id`, installation, label+"|"+e.suffix, e.user.ID).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	return identity, installation
}

// listedKeys reports whether the account's key list shows its person key and
// the device key with id.
func (e *keyEnv) listedKeys(t *testing.T, id string) (person, device bool) {
	t.Helper()
	keys, err := e.signIn.ListOwnKeys(t.Context(), e.loginSession(t, e.user, models.SessionOpenedPassword, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys.Devices {
		device = device || key.ID == id
	}
	return keys.Person != nil, device
}

// Uninstalling a sign-in provider deletes its identities by cascade, which
// takes no account lock. Nothing in that cascade may wait on, or write, a
// key row: key rows are written only by application transactions that hold
// the account lock. Here one transaction holds both the account row and the
// bound device key's row, and the uninstall must still finish.
func TestUninstallNeverTouchesKeyRowsDB(t *testing.T) {
	env := newKeyEnv(t, "keyuninstall")
	ctx := t.Context()
	enrolled, _ := env.keyedAccount(t)
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, env.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT 1 FROM auth_device_keys WHERE id = $1 FOR UPDATE`, enrolled.DeviceID); err != nil {
		t.Fatal(err)
	}
	err = pgx.BeginFunc(ctx, env.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, env.installationID)
		return err
	})
	if err != nil {
		t.Fatalf("uninstall while the account and key rows are held: %v", err)
	}
}

// After its provider is uninstalled, a key bound to it is dead: the key
// list leaves it out at once, and its next use revokes it and answers
// signature_invalid, which tells the device to enroll again. The same holds
// for a key whose identity number names another account's identity. The
// session the key opened survives with its identity cleared: it is then a
// provider session whose identity is gone, which refresh ends at the
// provider age limit, as any such session.
func TestKeysOfAGoneIdentityAreDeadDB(t *testing.T) {
	env := newKeyEnv(t, "keydead")
	ctx := t.Context()
	person, device := newTestKey(t), newTestKey(t)
	claims := env.loginSession(t, env.user, models.SessionOpenedProvider, &env.identityID)
	enrolled, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
		KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device}),
		PersonKey:  person.pub, Claims: claims,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasPerson, hasDevice := env.listedKeys(t, enrolled.DeviceID); !hasPerson || !hasDevice {
		t.Fatalf("before the uninstall: person %v, device %v listed", hasPerson, hasDevice)
	}
	if _, err := env.pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, env.installationID); err != nil {
		t.Fatal(err)
	}
	if hasPerson, hasDevice := env.listedKeys(t, enrolled.DeviceID); hasPerson || hasDevice {
		t.Fatalf("after the uninstall: person %v, device %v listed", hasPerson, hasDevice)
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrKeySignatureInvalid) {
		t.Fatalf("sign-in with the dead key = %v", err)
	}
	if !env.keyRevoked(t, enrolled.DeviceID) {
		t.Fatal("the dead device key was not revoked at its use")
	}
	newDevice := newTestKey(t)
	if _, err := env.signIn.Introduce(ctx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &newDevice})); !errors.Is(err, ErrKeySignatureInvalid) {
		t.Fatalf("introduce with the dead person key = %v", err)
	}
	var personRevoked bool
	if err := env.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM auth_person_keys WHERE user_id = $1`, env.user.ID).Scan(&personRevoked); err != nil || !personRevoked {
		t.Fatalf("person key revoked = %v, %v", personRevoked, err)
	}
	// The key's session was opened before the uninstall. Revoking the key at
	// its use ended it; before that, it had only lost its identity.
	session := env.sessionRow(t, enrolled.Pair.SessionID)
	if session.IdentityID != nil || session.ProviderSince == nil {
		t.Fatalf("key session after the uninstall = identity %v since %v", session.IdentityID, session.ProviderSince)
	}

	// A key whose identity number is another account's identity is not
	// bound to that identity: it is a key whose identity is gone.
	stranger := env.localAccount(t, "keydead-stranger", models.RoleUser)
	foreign, installation := env.otherIdentity(t, "keydead-foreign")
	if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_identities SET user_id = $2 WHERE id = $1 AND plugin_installation_id = $3`,
		foreign, stranger.ID, installation); err != nil {
		t.Fatal(err)
	}
	misbound := newTestKey(t)
	key, err := env.signIn.keys.InsertDeviceKey(ctx, NewDeviceKey{UserID: env.user.ID, PublicKey: misbound.pub,
		Kind: models.DeviceKeyKindDevice, Name: "Misbound", EnrolledVia: models.AppKeyEnrolledSession, IdentityID: &foreign})
	if err != nil {
		t.Fatal(err)
	}
	if _, device := env.listedKeys(t, key.ID); device {
		t.Fatal("a key bound to another account's identity is listed")
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: misbound})); !errors.Is(err, ErrKeySignatureInvalid) {
		t.Fatalf("sign-in with a key bound to another account's identity = %v", err)
	}
	if !env.keyRevoked(t, key.ID) {
		t.Fatal("the misbound key was not revoked")
	}
}

// An administrator's unlink takes the identity row before the key rows and
// their sessions, the order a plugin uninstall's cascade takes the identity
// and the sessions it clears. Here another transaction holds the bound
// device key's row, so the unlink has to wait for it; by then it must hold
// the identity row. Revoking keys and sessions first deadlocks with an
// uninstall.
func TestAdminUnlinkLocksIdentityBeforeKeysDB(t *testing.T) {
	env := newKeyEnv(t, "keyadminunlink")
	ctx := t.Context()
	_, bound := env.enrollFromProvider(t)
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM auth_device_keys WHERE id = $1 FOR UPDATE`, bound.DeviceID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- NewIdentityService(env.pool).AdminUnlink(ctx, env.user.ID, env.identityID, 0, false) }()
	deadline := time.After(10 * time.Second)
	for held := false; !held; {
		select {
		case err := <-done:
			t.Fatalf("unlink finished while its key row was held: %v", err)
		case <-deadline:
			t.Fatal("unlink waits for a key row without holding its identity row")
		case <-time.After(20 * time.Millisecond):
		}
		_, err := env.pool.Exec(ctx, `SELECT 1 FROM plugin_auth_identities WHERE id = $1 FOR UPDATE NOWAIT`, env.identityID)
		var pgErr *pgconn.PgError
		held = errors.As(err, &pgErr) && pgErr.Code == "55P03"
		if err != nil && !held {
			t.Fatal(err)
		}
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("unlink = %v", err)
	}
	if !env.keyRevoked(t, bound.DeviceID) {
		t.Fatal("the unlinked identity's key was not revoked")
	}
}

// After a sign-in provider is uninstalled, the person key it bound is dead
// and the key list leaves it out. Enrolling again from a password session
// must then take a new person key, not answer a conflict with a key nobody
// can see: the dead key is revoked on the way.
func TestDeadPersonKeyDoesNotBlockEnrollDB(t *testing.T) {
	env := newKeyEnv(t, "keydeadenroll")
	ctx := t.Context()
	env.enrollFromProvider(t)
	if _, err := env.pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, env.installationID); err != nil {
		t.Fatal(err)
	}
	person, device := newTestKey(t), newTestKey(t)
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
		KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device}),
		PersonKey:  person.pub, Claims: env.loginSession(t, env.user, models.SessionOpenedPassword, nil),
	}); err != nil {
		t.Fatalf("enroll after the uninstall = %v", err)
	}
	var live, revoked int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE revoked_at IS NULL), count(*) FILTER (WHERE revoked_at IS NOT NULL)
		FROM auth_person_keys WHERE user_id = $1`, env.user.ID).Scan(&live, &revoked); err != nil {
		t.Fatal(err)
	}
	if live != 1 || revoked != 1 {
		t.Fatalf("person keys after enrolling again: %d live, %d revoked", live, revoked)
	}
}

// Unlinking an identity revokes the keys bound to it and ends their sessions
// at once; a key of the same account bound to another identity, and one not
// bound to any, are untouched.
func TestUnlinkRevokesItsKeysDB(t *testing.T) {
	env := newKeyEnv(t, "keyunlink")
	ctx := t.Context()
	_, bound := env.enrollFromProvider(t)
	otherID, _ := env.otherIdentity(t, "keyunlink-other")
	elsewhere, err := env.signIn.keys.InsertDeviceKey(ctx, NewDeviceKey{UserID: env.user.ID, PublicKey: newTestKey(t).pub,
		Kind: models.DeviceKeyKindDevice, Name: "Elsewhere", EnrolledVia: models.AppKeyEnrolledSession, IdentityID: &otherID})
	if err != nil {
		t.Fatal(err)
	}
	unbound, err := env.signIn.keys.InsertDeviceKey(ctx, NewDeviceKey{UserID: env.user.ID, PublicKey: newTestKey(t).pub,
		Kind: models.DeviceKeyKindDevice, Name: "Unbound", EnrolledVia: models.AppKeyEnrolledSession})
	if err != nil {
		t.Fatal(err)
	}
	// The account keeps its password, so unlinking isn't refused as its
	// last way in.
	if _, err := env.pool.Exec(ctx, `UPDATE users SET local_password_login_enabled = true WHERE id = $1`, env.user.ID); err != nil {
		t.Fatal(err)
	}
	if err := NewIdentityService(env.pool).UnlinkOwn(ctx, env.user.ID, env.identityID); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if !env.keyRevoked(t, bound.DeviceID) || env.sessionLive(t, bound.Pair.SessionID, env.user.ID) {
		t.Fatal("the unlinked identity's key or its session survived")
	}
	if env.keyRevoked(t, elsewhere.ID) || env.keyRevoked(t, unbound.ID) {
		t.Fatal("unlinking revoked a key not bound to that identity")
	}
}
