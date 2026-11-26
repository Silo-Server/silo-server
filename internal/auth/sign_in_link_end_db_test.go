package auth

import (
	"errors"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Whatever ends a sign-in link ends the app code its redeem left and the
// set-password token it handed out, in the same transaction. Each row
// redeems a link (leaving a live code and token), ends it one way, and
// expects both refused and no link row or link code left. Regression
// guard: a new link, or a password set with the link, used to leave the
// old redeem's code usable, which signed in and replaced the person key.
func TestEndingASignInLinkDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkend")
	ctx := t.Context()
	inTx := func(t *testing.T, fn func(pgx.Tx) error) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, env.pool, fn); err != nil {
			t.Fatal(err)
		}
	}
	admin := env.localAccount(t, "linkend-admin", models.RoleAdmin)
	users := NewUserRepository(env.pool)
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, user *models.User, token string)
	}{
		{"a new link", func(t *testing.T, user *models.User, _ string) { env.issueLink(t, user) }},
		{"a password set with the link", func(t *testing.T, _ *models.User, token string) {
			if _, err := env.signIn.SetPasswordWithSignInLink(ctx, token, "margin fossil quench"); err != nil {
				t.Fatal(err)
			}
		}},
		{"promotion", func(t *testing.T, user *models.User, _ string) {
			if err := users.Update(ctx, user.ID, models.UpdateUserInput{Role: new(models.RoleAdmin)}); err != nil {
				t.Fatal(err)
			}
		}},
		{"disabling, then enabling again", func(t *testing.T, user *models.User, _ string) {
			for _, enabled := range []bool{false, true} {
				if err := users.Update(ctx, user.ID, models.UpdateUserInput{Enabled: new(enabled)}); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"an administrator setting the password", func(t *testing.T, user *models.User, _ string) {
			if err := users.Update(ctx, user.ID, models.UpdateUserInput{Password: new("admin set password")}); err != nil {
				t.Fatal(err)
			}
		}},
		{"a completed password reset", func(t *testing.T, user *models.User, _ string) {
			inTx(t, func(tx pgx.Tx) error { return ResetPasswordInTransaction(ctx, tx, user.ID, "reset password chosen") })
		}},
		{"the person changing their password", func(t *testing.T, user *models.User, _ string) {
			var hash string
			if err := env.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, user.ID).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			if err := users.CompareAndSwapPassword(ctx, user.ID, hash, "a changed password"); err != nil {
				t.Fatal(err)
			}
		}},
		{"signing out every other session", func(t *testing.T, user *models.User, _ string) {
			keep := env.loginSession(t, user, models.SessionOpenedPassword, nil)
			inTx(t, func(tx pgx.Tx) error { return RevokeOtherSignInsInTransaction(ctx, tx, user.ID, keep.SessionID) })
		}},
		{"an administrator signing the account out everywhere", func(t *testing.T, user *models.User, _ string) {
			if _, err := env.svc.sessions.RevokeAsAdmin(ctx, admin.ID, user.ID, nil); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := env.localAccount(t, "linkend-"+strconv.Itoa(len(tc.name)), models.RoleUser)
			got := env.linkCode(t, user)
			tc.end(t, user, got.SetPasswordToken)
			if _, _, err := env.redeemCode(t, newTestKey(t), got.AppCode.Code); !errors.Is(err, ErrAppCodeInvalid) {
				t.Fatalf("the earlier link's code: %v", err)
			}
			if _, err := env.signIn.SetPasswordWithSignInLink(ctx, got.SetPasswordToken, "another fine password"); !errors.Is(err, ErrSignInLinkInvalid) {
				t.Fatalf("the earlier link's set-password token: %v", err)
			}
			var links, codes int
			if err := env.pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM auth_sign_in_links WHERE user_id = $1 AND redeemed_at IS NOT NULL),
				(SELECT count(*) FROM auth_app_codes WHERE user_id = $1 AND kind = $2)`, user.ID, appCodeKindSignInLink).Scan(&links, &codes); err != nil {
				t.Fatal(err)
			}
			if links != 0 || codes != 0 {
				t.Fatalf("left %d redeemed links and %d link codes", links, codes)
			}
		})
	}
}

// A link to be emailed is stored only if the account still has the address
// the email goes to, compared as sign-in compares addresses. Regression
// guard: the address was read before the link was stored and not checked
// again, so a link could go to an address the account had just dropped.
func TestIssueSignInLinkRejectsChangedEmailDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkemail")
	user := env.localAccount(t, "linkemail", models.RoleUser)
	for _, tc := range []struct {
		name     string
		expected string
		want     error
	}{
		{"the account's address, in other letter case", "LINKEMAIL-" + env.suffix + "@EXAMPLE.TEST", nil},
		{"an address the account no longer has", "old-" + user.Email, ErrSignInLinkAccountChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.signIn.IssueSignInLink(t.Context(), SignInLinkIssue{UserID: user.ID, IssuedBy: user.ID, Delivery: "email", ExpectedEmail: &tc.expected})
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("issue = %v, want %v", err, tc.want)
			}
		})
	}
}

// The account's own person key sent with a link's code keeps its provider
// binding, and the device enrolled under it takes that binding, as a device
// the key introduces does. Regression guard: the device used to be enrolled
// unbound under a provider-bound key, outside the provider's checks.
func TestSignInLinkCodeKeepsOwnKeyBindingDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkbound")
	ctx := t.Context()
	person, device := newTestKey(t), newTestKey(t)
	claims := env.loginSession(t, env.user, models.SessionOpenedProvider, &env.identityID)
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
		KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device}),
		PersonKey:  person.pub, Claims: claims,
	}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	issued, err := env.signIn.IssueSignInLink(ctx, SignInLinkIssue{UserID: env.user.ID, Delivery: "link"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
	if err != nil || got.AppCode == nil {
		t.Fatalf("link redeem = %+v, %v", got, err)
	}
	result, newDevice, err := env.redeemCode(t, person, got.AppCode.Code)
	if err != nil || result.PersonKeyStatus != PersonKeyEnrolled {
		t.Fatalf("redeem = %+v, %v", result, err)
	}
	key := env.deviceKeyRow(t, newDevice)
	if !key.ProviderBound || key.IdentityID == nil || *key.IdentityID != env.identityID {
		t.Fatalf("device key = %+v, want bound to identity %d", key, env.identityID)
	}
}

// A replacement by a link's code is also recorded as a person key
// replacement, naming the link and the account that made it, so a watch on
// replacements sees it.
func TestSignInLinkReplacementAuditDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkaudit2")
	ctx := t.Context()
	user := env.localAccount(t, "linkaudit2", models.RoleUser)
	oldPerson, _, _ := env.enroll(t, user)
	issuer := env.localAccount(t, "linkaudit2-issuer", models.RoleAdmin)
	issued, err := env.signIn.IssueSignInLink(ctx, SignInLinkIssue{UserID: user.ID, IssuedBy: issuer.ID, Delivery: "link"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
	if err != nil {
		t.Fatal(err)
	}
	var linkID int64
	if err := env.pool.QueryRow(ctx, `SELECT id FROM auth_sign_in_links WHERE user_id = $1`, user.ID).Scan(&linkID); err != nil {
		t.Fatal(err)
	}
	auditCtx := env.addedCtx(t, "/api/v2/auth/app-codes/redeem", nil)
	person, device := newTestKey(t), newTestKey(t)
	if _, err := env.signIn.RedeemAppCode(auditCtx, KeyRedeemRequest{
		KeyRequest: from(auditCtx, env.request(t, message{purpose: appkeys.PurposeRedeem, signer: person, device: &device, fields: map[string]any{"code": got.AppCode.Code}})),
		PersonKey:  person.pub,
	}); err != nil {
		t.Fatal(err)
	}
	row := audittest.One(t, env.pool, auditmutation.ActionPersonKeyReplaced, auditmutation.TargetUser, strconv.Itoa(user.ID))
	kid := row.Change(t, auditFieldPersonKey)
	if audittest.Value(kid.Before) != `"`+oldPerson.pub.KeyID()+`"` || audittest.Value(kid.After) != `"`+person.pub.KeyID()+`"` {
		t.Fatalf("person_key_kid %s -> %s", audittest.Value(kid.Before), audittest.Value(kid.After))
	}
	for field, want := range map[string]string{auditFieldSignInLink: strconv.FormatInt(linkID, 10), auditFieldSignInLinkCreatedBy: strconv.Itoa(issuer.ID)} {
		if v := audittest.Value(row.Change(t, field).After); v != want {
			t.Fatalf("%s = %s, want %s", field, v, want)
		}
	}
	row.AssertNoSecrets(t, got.AppCode.Code, issued.Token)
}
