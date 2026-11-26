package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
)

// addedCtx is the context of an audited v2 request on pattern, from the
// address audittest's request comes from, signed in as userID when set.
func (e *keyEnv) addedCtx(t *testing.T, pattern string, userID *int) context.Context {
	t.Helper()
	ctx, _ := audittest.Context(t, e.pool, audittest.Request{Method: http.MethodPost, Pattern: pattern, Path: pattern, UserID: userID, OptIn: true})
	if clientip.FromContext(ctx) == "" {
		t.Fatal("the request has no client address")
	}
	return ctx
}

// from is req as the route builds it under ctx: the request's address is
// the client address the middleware resolved.
func from(ctx context.Context, req KeyRequest) KeyRequest {
	req.IP = clientip.FromContext(ctx)
	return req
}

// sessionIP is the address a login session recorded.
func (e *keyEnv) sessionIP(t *testing.T, id string) string {
	t.Helper()
	var ip string
	if err := e.pool.QueryRow(t.Context(), `SELECT COALESCE(host(ip_address), '') FROM auth_sessions WHERE id = $1`, id).Scan(&ip); err != nil {
		t.Fatal(err)
	}
	return ip
}

// keyIP is the address a key row recorded, from either table.
func (e *keyEnv) keyIP(t *testing.T, table, column, value string) string {
	t.Helper()
	var ip string
	if err := e.pool.QueryRow(t.Context(), `SELECT COALESCE(host(enrolled_ip_address), '') FROM `+table+` WHERE `+column+` = $1`, value).Scan(&ip); err != nil {
		t.Fatal(err)
	}
	return ip
}

// added is what one key-adding call did, for checkAdded.
type added struct {
	userID int
	// after is each change's after side as JSON; before likewise.
	after, before map[string]string
	// secrets must appear nowhere in the row.
	secrets []string
	// keys are the key rows the call wrote, by table and kid, that
	// must carry the request's address; session is the one it opened.
	personKid, deviceKid, session string
}

// keyAddingCase is one way a key or an approval in advance comes to exist.
type keyAddingCase struct {
	name, action, pattern string
	run                   func(t *testing.T, label string) added
}

func quoted(s string) string { raw, _ := json.Marshal(s); return string(raw) }

func keyAddingCases(env *keyEnv, devices *DeviceLoginService) []keyAddingCase {
	return []keyAddingCase{
		{"enroll from a session", auditmutation.ActionAppKeysEnrolled, "/api/v2/auth/keys/enroll", func(t *testing.T, label string) added {
			user := env.localAccount(t, label, models.RoleUser)
			person, device := newTestKey(t), newTestKey(t)
			reqCtx := env.addedCtx(t, "/api/v2/auth/keys/enroll", &user.ID)
			result, err := env.signIn.Enroll(reqCtx, KeyEnrollRequest{
				KeyRequest: from(reqCtx, env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device})),
				PersonKey:  person.pub, Claims: env.loginSession(t, user, models.SessionOpenedPassword, nil),
			})
			if err != nil {
				t.Fatal(err)
			}
			return added{userID: user.ID, after: map[string]string{auditFieldDeviceKey: quoted(result.DeviceID), auditFieldPersonKey: quoted(person.pub.KeyID()),
				auditFieldSessionOpenedVia: quoted(models.SessionOpenedPassword)},
				personKid: person.pub.KeyID(), deviceKid: device.pub.KeyID(), session: result.Pair.SessionID}
		}},
		{"introduce", auditmutation.ActionDeviceKeyIntroduced, "/api/v2/auth/keys/introduce", func(t *testing.T, label string) added {
			user := env.localAccount(t, label, models.RoleUser)
			person, _, _ := env.enroll(t, user)
			device := newTestKey(t)
			reqCtx := env.addedCtx(t, "/api/v2/auth/keys/introduce", nil)
			result, err := env.signIn.Introduce(reqCtx, from(reqCtx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &device})))
			if err != nil {
				t.Fatal(err)
			}
			return added{userID: user.ID, after: map[string]string{auditFieldDeviceKey: quoted(result.DeviceID), auditFieldDeviceKeyStatus: quoted(models.DeviceKeyStatusActive)},
				deviceKid: device.pub.KeyID(), session: result.Pair.SessionID}
		}},
		{"replace the person key", auditmutation.ActionPersonKeyReplaced, "/api/v2/auth/keys/person", func(t *testing.T, label string) added {
			user := env.localAccount(t, label, models.RoleUser)
			old, _, _ := env.enroll(t, user)
			replacement := newTestKey(t)
			reqCtx := env.addedCtx(t, "/api/v2/auth/keys/person", &user.ID)
			if err := env.signIn.ReplacePersonKey(reqCtx, env.loginSession(t, user, models.SessionOpenedPassword, nil), replacement.pub, ActingProfile{}); err != nil {
				t.Fatal(err)
			}
			return added{userID: user.ID, before: map[string]string{auditFieldPersonKey: quoted(old.pub.KeyID())},
				after:     map[string]string{auditFieldPersonKey: quoted(replacement.pub.KeyID()), auditFieldSessionOpenedVia: quoted(models.SessionOpenedPassword)},
				personKid: replacement.pub.KeyID()}
		}},
		// An administrator's device the old key introduced is still waiting:
		// the replacement denies it, and the row counts it.
		{"replace a person key whose device waits", auditmutation.ActionPersonKeyReplaced, "/api/v2/auth/keys/person", func(t *testing.T, label string) added {
			admin := env.localAccount(t, label, models.RoleAdmin)
			old, _, _ := env.enroll(t, admin)
			env.introduceWaiting(t, old)
			replacement := newTestKey(t)
			reqCtx := env.addedCtx(t, "/api/v2/auth/keys/person", &admin.ID)
			if err := env.signIn.ReplacePersonKey(reqCtx, env.loginSession(t, admin, models.SessionOpenedPassword, nil), replacement.pub, ActingProfile{}); err != nil {
				t.Fatal(err)
			}
			return added{userID: admin.ID, before: map[string]string{auditFieldPersonKey: quoted(old.pub.KeyID())},
				after: map[string]string{auditFieldPersonKey: quoted(replacement.pub.KeyID()), auditFieldSessionOpenedVia: quoted(models.SessionOpenedPassword),
					auditFieldDeniedDeviceKeys: "1"},
				personKid: replacement.pub.KeyID()}
		}},
		{"issue an app code", auditmutation.ActionAppCodeIssued, "/api/v2/auth/app-codes", func(t *testing.T, label string) added {
			user := env.localAccount(t, label, models.RoleUser)
			reqCtx := env.addedCtx(t, "/api/v2/auth/app-codes", &user.ID)
			issued, err := env.signIn.IssueSessionAppCode(reqCtx, env.loginSession(t, user, models.SessionOpenedPassword, nil), ActingProfile{})
			if err != nil {
				t.Fatal(err)
			}
			hash, _ := appCodeHash(issued.Code)
			return added{userID: user.ID, after: map[string]string{auditFieldAppCodeKind: quoted(appCodeKindSession), auditFieldExpiresAt: quoted(auditInstant(issued.ExpiresAt))},
				secrets: []string{issued.Code, base64.StdEncoding.EncodeToString(hash), base64.RawURLEncoding.EncodeToString(hash)}}
		}},
		{"redeem an app code", auditmutation.ActionAppCodeRedeemed, "/api/v2/auth/app-codes/redeem", func(t *testing.T, label string) added {
			user := env.localAccount(t, label, models.RoleUser)
			issued, err := env.signIn.IssueSessionAppCode(t.Context(), env.loginSession(t, user, models.SessionOpenedPassword, nil), ActingProfile{})
			if err != nil {
				t.Fatal(err)
			}
			person, device := newTestKey(t), newTestKey(t)
			reqCtx := env.addedCtx(t, "/api/v2/auth/app-codes/redeem", nil)
			result, err := env.signIn.RedeemAppCode(reqCtx, KeyRedeemRequest{PersonKey: person.pub,
				KeyRequest: from(reqCtx, env.request(t, message{purpose: appkeys.PurposeRedeem, signer: person, device: &device, fields: map[string]any{"code": issued.Code}}))})
			if err != nil {
				t.Fatal(err)
			}
			return added{userID: user.ID, after: map[string]string{auditFieldDeviceKey: quoted(result.DeviceID), auditFieldPersonKey: quoted(person.pub.KeyID()),
				auditFieldAppCodeKind: quoted(appCodeKindSession)}, secrets: []string{issued.Code},
				personKid: person.pub.KeyID(), deviceKid: device.pub.KeyID(), session: result.Pair.SessionID}
		}},
		{"redeem a grant", auditmutation.ActionAppGrantRedeemed, "/api/v2/auth/grants/redeem", func(t *testing.T, label string) added {
			user := env.localAccount(t, label, models.RoleUser)
			person, _, _ := env.enroll(t, user)
			app := newTestKey(t)
			reqCtx := env.addedCtx(t, "/api/v2/auth/grants/redeem", nil)
			result, err := env.signIn.RedeemGrant(reqCtx, from(reqCtx, env.request(t, message{purpose: appkeys.PurposeGrantRedeem, signer: app,
				fields: map[string]any{"grant": env.grantEnvelope(t, person, user.ID, app, nil), "device_platform": "android"}})))
			if err != nil {
				t.Fatal(err)
			}
			return added{userID: user.ID, after: map[string]string{auditFieldDeviceKey: quoted(result.DeviceID)},
				deviceKid: app.pub.KeyID(), session: result.Pair.SessionID}
		}},
		{"approve a sign-in in advance", auditmutation.ActionDeviceSignInPreapproved, "/api/v2/auth/device/preapprove", func(t *testing.T, label string) added {
			user := env.localAccount(t, label, models.RoleUser)
			secret, binding := preapprovalSecret(t)
			reqCtx := env.addedCtx(t, "/api/v2/auth/device/preapprove", &user.ID)
			reqCtx = WithClaims(reqCtx, env.loginSession(t, user, models.SessionOpenedPassword, nil))
			result, err := devices.Preapprove(reqCtx, DevicePreapprovalInput{DeviceName: "Living room TV", Binding: binding}, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			return added{userID: user.ID, after: map[string]string{auditFieldDeviceLoginRequest: quoted(result.ID), auditFieldDeviceName: quoted("Living room TV"),
				auditFieldExpiresAt: quoted(auditInstant(result.ExpiresAt))}, secrets: []string{result.DeviceCode, secret, binding}}
		}},
	}
}

// checkAdded asserts tc's one audit row and the address on the key rows got
// names.
func (env *keyEnv) checkAdded(t *testing.T, tc keyAddingCase, got added) {
	t.Helper()
	row := audittest.One(t, env.pool, tc.action, auditmutation.TargetUser, strconv.Itoa(got.userID))
	if row.PathPattern != tc.pattern || row.StatusCode < 200 || row.StatusCode > 201 {
		t.Fatalf("row %s at %d, want %s", row.PathPattern, row.StatusCode, tc.pattern)
	}
	fields := map[string]bool{}
	for field := range got.after {
		fields[field] = true
	}
	for field := range got.before {
		fields[field] = true
	}
	if len(row.Changes) != len(fields) {
		t.Fatalf("changes = %+v, want only %v", row.Changes, fields)
	}
	for field, want := range got.after {
		if v := audittest.Value(row.Change(t, field).After); v != want {
			t.Fatalf("%s after = %s, want %s", field, v, want)
		}
	}
	for field, want := range got.before {
		if v := audittest.Value(row.Change(t, field).Before); v != want {
			t.Fatalf("%s before = %s, want %s", field, v, want)
		}
	}
	row.AssertNoSecrets(t, got.secrets...)
	want := "192.0.2.1"
	if got.session != "" {
		if want = env.sessionIP(t, got.session); want == "" {
			t.Fatal("the session the request opened recorded no address")
		}
	}
	if got.personKid != "" {
		if ip := env.keyIP(t, "auth_person_keys", "key_id", got.personKid); ip != want {
			t.Fatalf("person key address = %q, want %q", ip, want)
		}
	}
	if got.deviceKid != "" {
		if ip := env.keyIP(t, "auth_device_keys", "key_id", got.deviceKid); ip != want {
			t.Fatalf("device key address = %q, want %q", ip, want)
		}
	}
}

// Each way a key or an approval in advance comes to exist leaves one audit
// row on the account, in the transaction that made it, naming what was
// added and never a secret; each key row records the request's address,
// the one the session that request opened records. Regression: enrolling
// from a session, introducing, replacing the person key, issuing and
// redeeming an app code, redeeming a grant and approving a sign-in in
// advance left no trace an owner could read, and a key looked like any
// other whatever address added it.
func TestKeyAddingOperationsAuditedDB(t *testing.T) {
	env := newKeyEnv(t, "keyadded")
	devices := NewDeviceLoginService(env.pool, NewUserRepository(env.pool), env.jwt, NewSessionRepository(env.pool), nil, nil)
	for i, tc := range keyAddingCases(env, devices) {
		t.Run(tc.name, func(t *testing.T) {
			env.checkAdded(t, tc, tc.run(t, "keyadded-"+strconv.Itoa(i)))
		})
	}
}

// A refused or rolled-back operation leaves no audit row and no key. A
// redeem inserts the keys and then finds the issuing session ended, so its
// whole transaction rolls back; an enroll with a different person key is
// refused under the account lock.
func TestKeyAddingRefusalsLeaveNoAuditDB(t *testing.T) {
	env := newKeyEnv(t, "keyaddednone")
	ctx := t.Context()
	user := env.localAccount(t, "keyaddednone-user", models.RoleUser)
	env.enroll(t, user)

	issuing := env.loginSession(t, user, models.SessionOpenedPassword, nil)
	issued, err := env.signIn.IssueSessionAppCode(ctx, issuing, ActingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE id = $1`, issuing.SessionID); err != nil {
		t.Fatal(err)
	}
	person, device := newTestKey(t), newTestKey(t)
	reqCtx := env.addedCtx(t, "/api/v2/auth/app-codes/redeem", nil)
	if _, err := env.signIn.RedeemAppCode(reqCtx, KeyRedeemRequest{PersonKey: person.pub,
		KeyRequest: from(reqCtx, env.request(t, message{purpose: appkeys.PurposeRedeem, signer: person, device: &device, fields: map[string]any{"code": issued.Code}}))}); !errors.Is(err, ErrAppCodeInvalid) {
		t.Fatalf("redeem after its session ended = %v", err)
	}
	if _, err := env.signIn.keys.DeviceKeyByKeyID(ctx, device.pub.KeyID()); !errors.Is(err, ErrAppKeyNotFound) {
		t.Fatalf("the rolled-back redeem left its device key: %v", err)
	}

	other, second := newTestKey(t), newTestKey(t)
	enrollCtx := env.addedCtx(t, "/api/v2/auth/keys/enroll", &user.ID)
	if _, err := env.signIn.Enroll(enrollCtx, KeyEnrollRequest{
		KeyRequest: from(enrollCtx, env.request(t, message{purpose: appkeys.PurposeEnroll, signer: other, device: &second})),
		PersonKey:  other.pub, Claims: env.loginSession(t, user, models.SessionOpenedPassword, nil),
	}); !errors.Is(err, ErrPersonKeyConflict) {
		t.Fatalf("enroll a second person key = %v", err)
	}
	for _, action := range []string{auditmutation.ActionAppCodeRedeemed, auditmutation.ActionAppKeysEnrolled} {
		if rows := audittest.Rows(t, env.pool, action, auditmutation.TargetUser, strconv.Itoa(user.ID)); len(rows) != 0 {
			t.Fatalf("%s: %d rows after a refusal: %+v", action, len(rows), rows)
		}
	}
}

// A key row with no address, as every key enrolled before the column
// existed has, lists with an empty address rather than failing the list.
func TestKeyWithoutAddressListsEmptyDB(t *testing.T) {
	env := newKeyEnv(t, "keynoaddr")
	user := env.localAccount(t, "keynoaddr-user", models.RoleUser)
	_, device, enrolled := env.enroll(t, user)
	if _, err := env.pool.Exec(t.Context(), `UPDATE auth_device_keys SET enrolled_ip_address = NULL WHERE key_id = $1`, device.pub.KeyID()); err != nil {
		t.Fatal(err)
	}
	keys, err := env.signIn.ListOwnKeys(t.Context(), env.loginSession(t, user, models.SessionOpenedPassword, nil))
	if err != nil || len(keys.Devices) != 1 || keys.Devices[0].ID != enrolled.DeviceID || keys.Devices[0].EnrolledIP != "" {
		t.Fatalf("keys = %+v, %v", keys, err)
	}
}

// Enrolling and replacing the person key record how the session that did it
// was opened, read from that session's row in the change's transaction: a
// later notice tells a change with a device key behind it (app_key) from one
// made after a password or provider sign-in. One row per value either
// operation can see; a device sign-in's session is refused both.
func TestKeyChangesRecordSessionOpenedViaDB(t *testing.T) {
	env := newKeyEnv(t, "keyopenedvia")
	sessions := []struct {
		via     string
		session func(t *testing.T, label string) (*models.User, *Claims, testKey)
	}{
		{models.SessionOpenedPassword, func(t *testing.T, label string) (*models.User, *Claims, testKey) {
			user := env.localAccount(t, label, models.RoleUser)
			return user, env.loginSession(t, user, models.SessionOpenedPassword, nil), newTestKey(t)
		}},
		{models.SessionOpenedProvider, func(t *testing.T, _ string) (*models.User, *Claims, testKey) {
			return env.user, env.loginSession(t, env.user, models.SessionOpenedProvider, &env.identityID), newTestKey(t)
		}},
		// The session a device's own key opened, enrolling a second device
		// under the same person key.
		{models.SessionOpenedAppKey, func(t *testing.T, label string) (*models.User, *Claims, testKey) {
			user := env.localAccount(t, label, models.RoleUser)
			person, _, result := env.enroll(t, user)
			return user, claimsOf(result), person
		}},
	}
	for i, tc := range sessions {
		t.Run(tc.via, func(t *testing.T) {
			user, claims, person := tc.session(t, "keyopenedvia-"+strconv.Itoa(i))
			device := newTestKey(t)
			enrollCtx := env.addedCtx(t, "/api/v2/auth/keys/enroll", &user.ID)
			if _, err := env.signIn.Enroll(enrollCtx, KeyEnrollRequest{
				KeyRequest: from(enrollCtx, env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device})),
				PersonKey:  person.pub, Claims: claims,
			}); err != nil {
				t.Fatal(err)
			}
			replaceCtx := env.addedCtx(t, "/api/v2/auth/keys/person", &user.ID)
			if err := env.signIn.ReplacePersonKey(replaceCtx, claims, newTestKey(t).pub, ActingProfile{}); err != nil {
				t.Fatal(err)
			}
			want := `"` + tc.via + `"`
			for _, action := range []string{auditmutation.ActionAppKeysEnrolled, auditmutation.ActionPersonKeyReplaced} {
				row := audittest.One(t, env.pool, action, auditmutation.TargetUser, strconv.Itoa(user.ID))
				if got := audittest.Value(row.Change(t, auditFieldSessionOpenedVia).After); got != want {
					t.Fatalf("%s: session_opened_via = %s, want %s", action, got, want)
				}
			}
		})
	}
}

// A key row stores no address for the unspecified address the client-address
// resolver answers when it can't tell the client (0.0.0.0, or ::), so the
// lists show none rather than "0.0.0.0"; any other address is stored as
// given. Regression: such a key listed, and the panel showed, "From 0.0.0.0".
func TestKeyStoresNoUnspecifiedAddressDB(t *testing.T) {
	env := newKeyEnv(t, "keyunspec")
	user := env.localAccount(t, "keyunspec-user", models.RoleUser)
	for _, tc := range []struct{ ip, want string }{{"0.0.0.0", ""}, {"::", ""}, {"", ""}, {"192.0.2.9", "192.0.2.9"}} {
		device := newTestKey(t)
		if _, err := env.signIn.keys.InsertDeviceKey(t.Context(), NewDeviceKey{UserID: user.ID, PublicKey: device.pub,
			Kind: models.DeviceKeyKindDevice, Name: "Phone", EnrolledVia: models.AppKeyEnrolledSession, IP: tc.ip}); err != nil {
			t.Fatal(err)
		}
		if got := env.keyIP(t, "auth_device_keys", "key_id", device.pub.KeyID()); got != tc.want {
			t.Fatalf("enrolled from %q: stored %q, want %q", tc.ip, got, tc.want)
		}
	}
}

// An introduce records its row and then opens the device's session in the
// same transaction. When that session can't be written, the transaction
// rolls back and takes the row and the key with it: the row is never written
// outside the change. A trigger on the test database refuses the session row
// for this one device name; production code has no seam for it.
func TestIntroduceFailingAfterItsAuditRowLeavesNoneDB(t *testing.T) {
	env := newKeyEnv(t, "keyaddedlate")
	ctx := t.Context()
	user := env.localAccount(t, "keyaddedlate-user", models.RoleUser)
	person, _, _ := env.enroll(t, user)
	name := "refused-" + strconv.FormatInt(env.now.UnixNano(), 36)
	fn := "test_refuse_session_" + strconv.FormatInt(env.now.UnixNano(), 36)
	if _, err := env.pool.Exec(ctx, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'session refused by test'; END $$;
		CREATE TRIGGER `+fn+` BEFORE INSERT ON auth_sessions FOR EACH ROW
		WHEN (NEW.device_name = '`+name+`') EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.WithoutCancel(ctx), `DROP TRIGGER IF EXISTS `+fn+` ON auth_sessions; DROP FUNCTION IF EXISTS `+fn+`()`)
	})
	device := newTestKey(t)
	reqCtx := env.addedCtx(t, "/api/v2/auth/keys/introduce", nil)
	req := from(reqCtx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &device,
		fields: map[string]any{"device_name": name}}))
	if _, err := env.signIn.Introduce(reqCtx, req); err == nil || !strings.Contains(err.Error(), "session refused by test") {
		t.Fatalf("introduce whose session is refused = %v", err)
	}
	if rows := audittest.Rows(t, env.pool, auditmutation.ActionDeviceKeyIntroduced, auditmutation.TargetUser, strconv.Itoa(user.ID)); len(rows) != 0 {
		t.Fatalf("%d audit rows survived the rollback: %+v", len(rows), rows)
	}
	if _, err := env.signIn.keys.DeviceKeyByKeyID(ctx, device.pub.KeyID()); !errors.Is(err, ErrAppKeyNotFound) {
		t.Fatalf("the rolled-back introduce left its device key: %v", err)
	}
}
