package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/models"
)

// reportsPersonKey fails unless result is a signed_in answer that says the
// account holds the person key want, or none when want is "".
func reportsPersonKey(t *testing.T, label string, result *KeySignInResult, want string) {
	t.Helper()
	if result.Status != appkeys.ReplySignedIn || !result.PersonKeyReported || result.PersonKeyID != want || result.GrantedApp {
		t.Fatalf("%s: status %q, reported %v, person key %q, granted app %v; want %q", label,
			result.Status, result.PersonKeyReported, result.PersonKeyID, result.GrantedApp, want)
	}
}

// signInWith signs in with an enrolled device key.
func (e *keyEnv) signInWith(t *testing.T, device testKey) *KeySignInResult {
	t.Helper()
	result, err := e.signIn.SignIn(t.Context(), e.request(t, message{purpose: appkeys.PurposeSignIn, signer: device}))
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	return result
}

// Every signed_in answer to one of the person's own device keys says which
// person key the account holds, read as the session opens: a device that
// knows a newer key learns the server is behind. A revoked key, and one
// whose provider identity is gone, count as none, as in the key list.
func TestSignedInRepliesReportPersonKeyDB(t *testing.T) {
	env := newKeyEnv(t, "keyreport")
	ctx := t.Context()
	user := env.localAccount(t, "keyreport-own", models.RoleUser)
	person, device, enrolled := env.enroll(t, user)
	reportsPersonKey(t, "enroll", enrolled, person.pub.KeyID())
	reportsPersonKey(t, "sign-in", env.signInWith(t, device), person.pub.KeyID())

	introduced := newTestKey(t)
	result, err := env.signIn.Introduce(ctx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &introduced}))
	if err != nil {
		t.Fatalf("introduce: %v", err)
	}
	reportsPersonKey(t, "introduce", result, person.pub.KeyID())

	// A replace: the next sign-in of a device already enrolled names the
	// new key, not the one that vouched for it.
	replacement := newTestKey(t)
	if err := env.signIn.ReplacePersonKey(ctx, env.loginSession(t, user, models.SessionOpenedPassword, nil), replacement.pub, ActingProfile{}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	reportsPersonKey(t, "sign-in after a replace", env.signInWith(t, device), replacement.pub.KeyID())

	// Revoked by an administrator: the device keeps signing in, and learns
	// the account has no person key.
	if err := env.signIn.keys.RevokePersonKey(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	reportsPersonKey(t, "sign-in after a revoke", env.signInWith(t, introduced), "")

	// Bound to a provider identity that is gone: unrevoked, but dead.
	bound := env.localAccount(t, "keyreport-bound", models.RoleUser)
	boundPerson, boundDevice, _ := env.enroll(t, bound)
	var identity int64
	if err := env.pool.QueryRow(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3) RETURNING id`, env.installationID, "keyreport-bound|"+env.suffix, bound.ID).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE auth_person_keys SET identity_id = $2, provider_bound = true WHERE user_id = $1`, bound.ID, identity); err != nil {
		t.Fatal(err)
	}
	reportsPersonKey(t, "sign-in with a bound key", env.signInWith(t, boundDevice), boundPerson.pub.KeyID())
	if _, err := env.pool.Exec(ctx, `DELETE FROM plugin_auth_identities WHERE id = $1`, identity); err != nil {
		t.Fatal(err)
	}
	reportsPersonKey(t, "sign-in once the identity is gone", env.signInWith(t, boundDevice), "")
}

// The answers that never say which person key the account holds: a
// waiting device's (no session opened), and every answer to another app's
// key, whose session also never carries the account's email. A person key
// ID that changes would tell that app the person replaced their key.
func TestRepliesThatLeaveOutThePersonKeyDB(t *testing.T) {
	env := newKeyEnv(t, "keyreportout")
	ctx := t.Context()

	admin := env.localAccount(t, "keyreportout-admin", models.RoleAdmin)
	adminPerson, _, _ := env.enroll(t, admin)
	waiting := newTestKey(t)
	result, err := env.signIn.Introduce(ctx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: adminPerson, device: &waiting}))
	if err != nil || result.Status != appkeys.ReplyApprovalRequired || result.PersonKeyReported {
		t.Fatalf("waiting introduce = %+v, %v; want approval_required with no person key", result, err)
	}

	user := env.localAccount(t, "keyreportout-own", models.RoleUser)
	person, _, _ := env.enroll(t, user)
	app := newTestKey(t)
	redeemed, err := env.redeem(t, app, env.grantEnvelope(t, person, user.ID, app, nil))
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if !redeemed.GrantedApp || redeemed.PersonKeyReported {
		t.Fatalf("grant redeem: granted app %v, reported %v", redeemed.GrantedApp, redeemed.PersonKeyReported)
	}
	appSignIn := func(label string) {
		t.Helper()
		again := env.signInWith(t, app)
		if again.Status != appkeys.ReplySignedIn || !again.GrantedApp || again.PersonKeyReported {
			t.Fatalf("%s: status %q, granted app %v, reported %v", label, again.Status, again.GrantedApp, again.PersonKeyReported)
		}
	}
	appSignIn("the app's sign-in")
	if err := env.signIn.ReplacePersonKey(ctx, env.loginSession(t, user, models.SessionOpenedPassword, nil), newTestKey(t).pub, ActingProfile{}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	appSignIn("the app's sign-in after a replace")

	// Its refreshes say the same: not the person's own session.
	pair, err := env.svc.Refresh(ctx, redeemed.Pair.RefreshToken)
	if err != nil || !pair.GrantedApp || pair.User == nil || pair.User.ID != user.ID {
		t.Fatalf("refresh of the app's session = %+v, %v", pair, err)
	}
	own, err := env.svc.Refresh(ctx, env.signInWith(t, mustDevice(t, env, user)).Pair.RefreshToken)
	if err != nil || own.GrantedApp || own.User == nil || !own.User.CreatedAt.Equal(user.CreatedAt) {
		t.Fatalf("refresh of the person's own key session = %+v, %v", own, err)
	}
}

// mustDevice stores a device key of the person's own on user's account,
// vouched for by its live person key.
func mustDevice(t *testing.T, env *keyEnv, user *models.User) testKey {
	t.Helper()
	live, err := env.signIn.keys.LivePersonKey(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	device := newTestKey(t)
	if _, err := env.signIn.keys.InsertDeviceKey(t.Context(), NewDeviceKey{UserID: user.ID, PublicKey: device.pub,
		Kind: models.DeviceKeyKindDevice, Name: "Phone", EnrolledVia: models.AppKeyEnrolledSession, PersonKeyID: &live.ID}); err != nil {
		t.Fatal(err)
	}
	return device
}

// A person key this server revoked is never taken again while the account
// that held it exists: not by enroll, an app code's redeem or a replace, on
// its own account or another. Nothing prunes revoked rows; deleting the
// account deletes them, and then the key may be enrolled again. A cleanup
// job for key rows would break this, and this test with it.
func TestRevokedPersonKeyIsNeverTakenAgainDB(t *testing.T) {
	env := newKeyEnv(t, "keyreuse")
	ctx := t.Context()
	holder := env.localAccount(t, "keyreuse-holder", models.RoleUser)
	revoked, _, _ := env.enroll(t, holder)
	if err := env.signIn.keys.RevokePersonKey(ctx, holder.ID); err != nil {
		t.Fatal(err)
	}
	other := env.localAccount(t, "keyreuse-other", models.RoleUser)

	for _, account := range []*models.User{holder, other} {
		device := newTestKey(t)
		_, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
			KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: revoked, device: &device}),
			PersonKey:  revoked.pub, Claims: env.loginSession(t, account, models.SessionOpenedPassword, nil),
		})
		if !errors.Is(err, ErrAppKeyAlreadyEnrolled) {
			t.Fatalf("enroll on account %d: %v", account.ID, err)
		}
		issued, err := env.signIn.IssueSessionAppCode(ctx, env.loginSession(t, account, models.SessionOpenedPassword, nil), ActingProfile{})
		if err != nil {
			t.Fatal(err)
		}
		var enrolled *KeyEnrolledError
		if _, _, err := env.redeemCode(t, revoked, issued.Code); !errors.As(err, &enrolled) || enrolled.Member != KeyMemberPerson {
			t.Fatalf("app code redeem on account %d: %v", account.ID, err)
		}
		if err := env.signIn.ReplacePersonKey(ctx, env.loginSession(t, account, models.SessionOpenedPassword, nil), revoked.pub, ActingProfile{}); !errors.Is(err, ErrAppKeyAlreadyEnrolled) {
			t.Fatalf("replace on account %d: %v", account.ID, err)
		}
	}

	if _, err := NewUserRepository(env.pool).MutateAdminAccount(ctx, holder.ID, -1, nil,
		func(*models.User, pgx.Tx) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("delete the account: %v", err)
	}
	if err := env.signIn.ReplacePersonKey(ctx, env.loginSession(t, other, models.SessionOpenedPassword, nil), revoked.pub, ActingProfile{}); err != nil {
		t.Fatalf("enrolling the key once its account is gone: %v", err)
	}
}

// account_created_at tells a device which account it reached, so nothing
// an administrator edits may move it: a rename leaves it, and the refresh
// answer carries the stored value at its full microsecond precision.
func TestAccountCreatedAtSurvivesRenameDB(t *testing.T) {
	env := newKeyEnv(t, "createdat")
	ctx := t.Context()
	user := env.localAccount(t, "createdat-own", models.RoleUser)
	_, device, _ := env.enroll(t, user)
	var stored time.Time
	if err := env.pool.QueryRow(ctx, `SELECT created_at FROM users WHERE id = $1`, user.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	renamed := user.Username + "-renamed"
	if _, err := NewUserRepository(env.pool).MutateAdminAccount(ctx, user.ID, -1, &models.UpdateUserInput{Username: &renamed},
		func(*models.User, pgx.Tx) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("rename: %v", err)
	}
	pair, err := env.svc.Refresh(ctx, env.signInWith(t, device).Pair.RefreshToken)
	if err != nil || pair.User == nil || pair.User.Username != renamed || !pair.User.CreatedAt.Equal(stored) {
		t.Fatalf("refresh after a rename = %+v, %v; want created_at %s", pair, err, stored.Format(time.RFC3339Nano))
	}
}
