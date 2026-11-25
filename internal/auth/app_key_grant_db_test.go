package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/models"
)

// grantEnvelope is a grant the person key signs away from the server, for
// app's key on userID's account, as the object a grant-redeem carries.
// fields override the payload's.
func (e *keyEnv) grantEnvelope(t *testing.T, person testKey, userID int, app testKey, fields map[string]any) map[string]any {
	t.Helper()
	id := make([]byte, appkeys.GrantIDSize)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"purpose": string(appkeys.PurposeGrant), "server_id": keyTestServerID,
		"iat": e.now.Unix() - 10, "exp": e.now.Unix() + 290,
		"grant_id": base64.RawURLEncoding.EncodeToString(id), "account_id": strconv.Itoa(userID),
		"origins": []string{"https://media.example.test"}, "app_key": app.pub.String(), "app_name": "Example Player",
	}
	for k, v := range fields {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return map[string]any{"kid": person.pub.KeyID(), "payload": encoded, "sig": person.sign(t, appkeys.PurposeGrant, encoded)}
}

// redeem signs a grant-redeem carrying grant with signer, on a fresh
// challenge.
func (e *keyEnv) redeem(t *testing.T, signer testKey, grant map[string]any) (*KeySignInResult, error) {
	t.Helper()
	return e.signIn.RedeemGrant(t.Context(), e.request(t, message{purpose: appkeys.PurposeGrantRedeem, signer: signer,
		fields: map[string]any{"grant": grant, "device_platform": "android"}}))
}

func claimsOf(result *KeySignInResult) *Claims {
	return &Claims{UserID: result.User.ID, Role: result.User.Role, SessionID: result.Pair.SessionID, TokenType: TokenTypeAccess}
}

// A grant the person key signed lets the app it names enroll its key and
// sign in: the key is listed as an app the account allowed, signs in again
// on its own, takes the person key's provider binding, and revoking it ends
// its session.
func TestGrantRedeemDB(t *testing.T) {
	env := newKeyEnv(t, "grantok")
	ctx := t.Context()
	// The provider account's keys are bound to its identity.
	personKey := newTestKey(t)
	device := newTestKey(t)
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
		KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: personKey, device: &device}),
		PersonKey:  personKey.pub, Claims: env.loginSession(t, env.user, models.SessionOpenedProvider, &env.identityID),
	}); err != nil {
		t.Fatal(err)
	}
	app := newTestKey(t)
	result, err := env.redeem(t, app, env.grantEnvelope(t, personKey, env.user.ID, app, nil))
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if result.Status != appkeys.ReplySignedIn || result.Pair == nil || result.User.ID != env.user.ID {
		t.Fatalf("result = %+v", result)
	}
	var identityID *int64
	var bound bool
	var sessionIdentity *int64
	if err := env.pool.QueryRow(ctx, `SELECT k.identity_id, k.provider_bound, s.identity_id FROM auth_device_keys k
		JOIN auth_sessions s ON s.device_key_id = k.id WHERE k.id = $1`, result.DeviceID).Scan(&identityID, &bound, &sessionIdentity); err != nil {
		t.Fatal(err)
	}
	if !bound || identityID == nil || *identityID != env.identityID || sessionIdentity == nil || *sessionIdentity != env.identityID {
		t.Fatalf("app key binding = %v %v, session %v; want the person key's identity %d", identityID, bound, sessionIdentity, env.identityID)
	}

	// The person's own session reads the list.
	own, err := env.signIn.ListOwnKeys(ctx, env.loginSession(t, env.user, models.SessionOpenedProvider, &env.identityID))
	if err != nil {
		t.Fatal(err)
	}
	var listed *models.AppDeviceKey
	for _, key := range own.Devices {
		if key.ID == result.DeviceID {
			listed = key
		}
	}
	if listed == nil || listed.Kind != models.DeviceKeyKindApp || listed.EnrolledVia != models.AppKeyEnrolledGrant ||
		listed.Name != "Example Player" || listed.Platform != "android" || own.AllowedBy != env.user.Username {
		t.Fatalf("listed = %+v, allowed by %q", listed, own.AllowedBy)
	}

	again, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: app}))
	if err != nil || again.DeviceID != result.DeviceID {
		t.Fatalf("the app's own sign-in = %+v, %v", again, err)
	}
	if err := env.signIn.RevokeOwnDeviceKey(ctx, claimsOf(result), result.DeviceID); err != nil {
		t.Fatalf("the app revoking its own key = %v", err)
	}
	for _, id := range []string{result.Pair.SessionID, again.Pair.SessionID} {
		if env.sessionLive(t, id, env.user.ID) {
			t.Fatal("a session of the revoked app key stayed live")
		}
	}
}

// Each grant the server must not honor is refused with the problem the app
// branches on: grant_expired only for the grant's own exp, grant_invalid for
// a grant that can't be used here, and signature_invalid for a redeemer that
// doesn't hold the app key.
func TestGrantRedeemRefusalsDB(t *testing.T) {
	env := newKeyEnv(t, "grantno")
	ctx := t.Context()
	user := env.localAccount(t, "grantno-user", models.RoleUser)
	person, _, _ := env.enroll(t, user)
	admin := env.localAccount(t, "grantno-admin", models.RoleAdmin)
	adminPerson, _, _ := env.enroll(t, admin)
	replaced := env.localAccount(t, "grantno-replaced", models.RoleUser)
	oldPerson, _, _ := env.enroll(t, replaced)
	if err := env.signIn.ReplacePersonKey(ctx, env.loginSession(t, replaced, models.SessionOpenedPassword, nil), newTestKey(t).pub); err != nil {
		t.Fatal(err)
	}

	used := newTestKey(t)
	usedGrant := env.grantEnvelope(t, person, user.ID, used, nil)
	first, err := env.redeem(t, used, usedGrant)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.signIn.RevokeOwnDeviceKey(ctx, claimsOf(first), first.DeviceID); err != nil {
		t.Fatal(err)
	}

	grantInvalid := func(err error) bool {
		return errors.Is(err, ErrGrantInvalid) || appkeys.CodeOf(err) == appkeys.CodeGrantInvalid || appkeys.CodeOf(err) == appkeys.CodeOriginNotGranted
	}
	for _, tc := range []struct {
		name   string
		signer testKey // signs the grant-redeem; the grant names app
		app    testKey
		grant  func(app testKey) map[string]any
		want   func(error) bool
	}{
		{name: "expired grant", grant: func(app testKey) map[string]any {
			return env.grantEnvelope(t, person, user.ID, app, map[string]any{"iat": env.now.Unix() - 400, "exp": env.now.Unix() - 100})
		}, want: func(err error) bool { return appkeys.CodeOf(err) == appkeys.CodeGrantExpired }},
		// The same grant again, for a key since revoked: the grant, not
		// the key, refuses it.
		{name: "grant redeemed twice", app: used, signer: used, grant: func(testKey) map[string]any { return usedGrant }, want: grantInvalid},
		{name: "grant for another server", grant: func(app testKey) map[string]any {
			return env.grantEnvelope(t, person, user.ID, app, map[string]any{"server_id": uuid.NewString()})
		}, want: grantInvalid},
		{name: "grant for an admin account", grant: func(app testKey) map[string]any {
			return env.grantEnvelope(t, adminPerson, admin.ID, app, nil)
		}, want: grantInvalid},
		{name: "redeemer without the app key", signer: newTestKey(t), grant: func(app testKey) map[string]any {
			return env.grantEnvelope(t, person, user.ID, app, nil)
		}, want: func(err error) bool { return appkeys.CodeOf(err) == appkeys.CodeSignatureInvalid }},
		{name: "grant signed by a replaced person key", grant: func(app testKey) map[string]any {
			return env.grantEnvelope(t, oldPerson, replaced.ID, app, nil)
		}, want: grantInvalid},
		{name: "grant for another account's person key", grant: func(app testKey) map[string]any {
			return env.grantEnvelope(t, person, admin.ID, app, nil)
		}, want: grantInvalid},
		{name: "origin outside the grant's origins", grant: func(app testKey) map[string]any {
			return env.grantEnvelope(t, person, user.ID, app, map[string]any{"origins": []string{"https://other.example.test"}})
		}, want: grantInvalid},
		{name: "grant living longer than five minutes", grant: func(app testKey) map[string]any {
			return env.grantEnvelope(t, person, user.ID, app, map[string]any{"exp": env.now.Unix() + 300})
		}, want: grantInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := tc.app
			if app.priv == nil {
				app = newTestKey(t)
			}
			signer := tc.signer
			if signer.priv == nil {
				signer = app
			}
			result, err := env.redeem(t, signer, tc.grant(app))
			if result != nil || !tc.want(err) {
				t.Fatalf("redeem = %+v, %v", result, err)
			}
			var enrolled bool
			if err := env.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_device_keys WHERE key_id = $1 AND revoked_at IS NULL)`,
				app.pub.KeyID()).Scan(&enrolled); err != nil {
				t.Fatal(err)
			}
			if enrolled {
				t.Fatal("a refused grant enrolled the app's key")
			}
		})
	}
}

// Two redemptions of one grant, racing on different API processes: exactly
// one enrolls the app.
func TestGrantRedeemConcurrentDB(t *testing.T) {
	env := newKeyEnv(t, "grantrace")
	user := env.localAccount(t, "grantrace-user", models.RoleUser)
	person, _, _ := env.enroll(t, user)
	app := newTestKey(t)
	grant := env.grantEnvelope(t, person, user.ID, app, nil)
	requests := []KeyRequest{}
	for range 2 {
		requests = append(requests, env.request(t, message{purpose: appkeys.PurposeGrantRedeem, signer: app,
			fields: map[string]any{"grant": grant}}))
	}
	errs := make([]error, len(requests))
	var wg sync.WaitGroup
	for i, req := range requests {
		wg.Go(func() { _, errs[i] = env.signIn.RedeemGrant(t.Context(), req) })
	}
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case !errors.Is(err, ErrGrantInvalid):
			t.Fatalf("the losing redemption = %v, want grant_invalid", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d redemptions succeeded: %v", succeeded, errs)
	}
}

// An app's session must not mint access that outlives revoking its key: it
// can't enroll keys, replace the person key or approve a device sign-in, and
// it can revoke only its own key.
func TestGrantSessionCannotManageKeysDB(t *testing.T) {
	env := newKeyEnv(t, "grantlimits")
	ctx := t.Context()
	user := env.localAccount(t, "grantlimits-user", models.RoleUser)
	person, _, enrolled := env.enroll(t, user)
	app := newTestKey(t)
	result, err := env.redeem(t, app, env.grantEnvelope(t, person, user.ID, app, nil))
	if err != nil {
		t.Fatal(err)
	}
	session := claimsOf(result)

	newPerson, device := newTestKey(t), newTestKey(t)
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
		KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: newPerson, device: &device}),
		PersonKey:  person.pub, Claims: session,
	}); !errors.Is(err, ErrSessionNotAllowed) {
		t.Fatalf("enroll = %v", err)
	}
	if err := env.signIn.ReplacePersonKey(ctx, session, newPerson.pub); !errors.Is(err, ErrSessionNotAllowed) {
		t.Fatalf("replace person key = %v", err)
	}
	// Device sign-in approval and approving one in advance call this rule.
	for _, action := range []SessionAction{ActionApproveDeviceSignIn, ActionManageKeys} {
		if err := SessionAllows(ctx, env.pool, session, action); !errors.Is(err, ErrSessionNotAllowed) {
			t.Fatalf("action %d = %v", action, err)
		}
	}
	if err := env.signIn.RevokeOwnDeviceKey(ctx, session, enrolled.DeviceID); !errors.Is(err, ErrSessionNotAllowed) {
		t.Fatalf("revoking the person's device = %v", err)
	}
	if err := env.signIn.RevokeOwnDeviceKey(ctx, session, result.DeviceID); err != nil {
		t.Fatalf("revoking its own key = %v", err)
	}
}
