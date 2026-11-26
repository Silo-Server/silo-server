package auth

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/config"
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
		{"a change of the account's email address", func(t *testing.T, user *models.User, _ string) {
			if err := users.Update(ctx, user.ID, models.UpdateUserInput{Email: new("moved-" + user.Email)}); err != nil {
				t.Fatal(err)
			}
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

// A link is allowed for an account exactly when it may sign in with a local
// password on this server, by the rule password sign-in reads: the switch
// on, or the account break-glass. With the switch off, a break-glass
// administrator's link works the password route (no code, as for any
// administrator) and the new password signs in; an ordinary account's or an
// ordinary administrator's is refused at every step and stays unspent.
// Regression guard: a link for a break-glass administrator was refused
// though that account still signs in with a password.
func TestSignInLinkLocalPasswordSwitchDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkswitch")
	ctx := t.Context()
	off := func(t *testing.T) {
		t.Helper()
		env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
		t.Cleanup(func() {
			if _, err := env.pool.Exec(context.Background(), `UPDATE server_settings SET value = 'true' WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey); err != nil {
				t.Error(err)
			}
		})
	}

	t.Run("a break-glass administrator", func(t *testing.T) {
		admin := env.localAccount(t, "linkswitch-glass", models.RoleAdmin)
		env.exec(t, `UPDATE users SET break_glass = true WHERE id = $1`, admin.ID)
		off(t)
		issued, err := env.signIn.IssueSignInLink(ctx, SignInLinkIssue{UserID: admin.ID, IssuedBy: admin.ID, Delivery: "link"})
		if err != nil {
			t.Fatalf("make: %v", err)
		}
		got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
		if err != nil || got.AppCode != nil || got.NoCode != NoCodeAdministrator || got.SetPasswordToken == "" {
			t.Fatalf("redeem = %+v, %v", got, err)
		}
		if _, err := env.signIn.SetPasswordWithSignInLink(ctx, got.SetPasswordToken, "break glass chosen"); err != nil {
			t.Fatalf("set-password: %v", err)
		}
		if _, user, err := env.svc.Login(ctx, admin.Username, "break glass chosen", "test", ""); err != nil || user.ID != admin.ID {
			t.Fatalf("sign-in with the new password: %v", err)
		}
	})

	for _, role := range []string{models.RoleUser, models.RoleAdmin} {
		t.Run("an ordinary "+role, func(t *testing.T) {
			user := env.localAccount(t, "linkswitch-"+role, role)
			issued := env.issueLink(t, user)
			off(t)
			if _, err := env.signIn.IssueSignInLink(ctx, SignInLinkIssue{UserID: user.ID, IssuedBy: user.ID}); !errors.Is(err, ErrSignInLinkNeedsProvider) {
				t.Fatalf("make: %v", err)
			}
			if _, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true); !errors.Is(err, ErrSignInLinkNeedsProvider) {
				t.Fatalf("redeem: %v", err)
			}
			var spent bool
			if err := env.pool.QueryRow(ctx, `SELECT redeemed_at IS NOT NULL FROM auth_sign_in_links WHERE user_id = $1`, user.ID).Scan(&spent); err != nil || spent {
				t.Fatalf("link spent = %v (%v), want unspent", spent, err)
			}
		})
	}

	t.Run("an ordinary account's set-password", func(t *testing.T) {
		user := env.localAccount(t, "linkswitch-pw", models.RoleUser)
		got := env.linkCode(t, user)
		off(t)
		if _, err := env.signIn.SetPasswordWithSignInLink(ctx, got.SetPasswordToken, "never accepted"); !errors.Is(err, ErrSignInLinkNeedsProvider) {
			t.Fatalf("set-password: %v", err)
		}
	})
}

// A link's code that brings a different person key on an account whose own
// person key is provider-bound: the new person key and the new device key
// start unbound, and the old person key is revoked.
func TestSignInLinkReplacementStartsUnboundDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkunbound")
	ctx := t.Context()
	old, device := newTestKey(t), newTestKey(t)
	claims := env.loginSession(t, env.user, models.SessionOpenedProvider, &env.identityID)
	if _, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
		KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: old, device: &device}),
		PersonKey:  old.pub, Claims: claims,
	}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	oldRow, err := env.signIn.keys.LivePersonKey(ctx, env.user.ID)
	if err != nil || !oldRow.ProviderBound {
		t.Fatalf("old person key = %+v, %v; want provider-bound", oldRow, err)
	}
	issued, err := env.signIn.IssueSignInLink(ctx, SignInLinkIssue{UserID: env.user.ID, Delivery: "link"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
	if err != nil || got.AppCode == nil {
		t.Fatalf("link redeem = %+v, %v", got, err)
	}
	person := newTestKey(t)
	result, newDevice, err := env.redeemCode(t, person, got.AppCode.Code)
	if err != nil || result.PersonKeyStatus != PersonKeyReplaced {
		t.Fatalf("redeem = %+v, %v", result, err)
	}
	live, err := env.signIn.keys.LivePersonKey(ctx, env.user.ID)
	if err != nil || live.KeyID != person.pub.KeyID() || live.ProviderBound || live.IdentityID != nil {
		t.Fatalf("new person key = %+v, %v; want unbound", live, err)
	}
	if key := env.deviceKeyRow(t, newDevice); key.ProviderBound || key.IdentityID != nil {
		t.Fatalf("new device key = %+v, want unbound", key)
	}
	if !env.personKeyRevoked(t, oldRow.ID) {
		t.Fatal("the old person key is still live")
	}
}

// A link is allowed only where password sign-in would take the account's
// password, and a network identity provider's refusal is part of that rule.
// Making a link for a refused account is refused; a refusal that arrives
// after the link is made, or after it is redeemed, refuses the next step and
// leaves the link or its set-password token unspent, and the step works
// again once the provider permits the person. The account view's
// local_password_allowed reads the same rule in one query for a page.
// Regression guard: such an account was offered a link that spent itself,
// replaced the password and signed the account out, for a password sign-in
// still refused.
func TestSignInLinkNetworkRefusalDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinknet")
	ctx := t.Context()
	env.exec(t, `UPDATE plugin_auth_bindings SET capability_id = 'tailscale' WHERE plugin_installation_id = $1`, env.installationID)
	env.exec(t, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'auth_provider.v1', 'tailscale', '{"auth_modes":["network"]}') ON CONFLICT DO NOTHING`, env.installationID)
	env.exec(t, `UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID)
	env.exec(t, `UPDATE plugin_installations SET enabled = true WHERE id = $1`, env.installationID)
	refuse := func(t *testing.T, refused bool) {
		t.Helper()
		status := CheckStatusActive
		if refused {
			status = CheckStatusNotPermitted
		}
		env.exec(t, `UPDATE plugin_auth_identities SET last_check_status = $2, pending_refusal = '' WHERE id = $1`, env.identityID, status)
	}
	user := env.user
	issue := func() (IssuedSignInLink, error) {
		return env.signIn.IssueSignInLink(ctx, SignInLinkIssue{UserID: user.ID, Delivery: "link"})
	}
	linkState := func(t *testing.T) (links int, redeemed, tokenUsed bool) {
		t.Helper()
		if err := env.pool.QueryRow(ctx, `SELECT count(*), COALESCE(bool_or(redeemed_at IS NOT NULL), false),
			COALESCE(bool_or(set_password_used_at IS NOT NULL), false) FROM auth_sign_in_links WHERE user_id = $1`, user.ID).Scan(&links, &redeemed, &tokenUsed); err != nil {
			t.Fatal(err)
		}
		return links, redeemed, tokenUsed
	}

	refuse(t, true)
	if single, err := networkRefused(ctx, env.pool, user.ID); err != nil || !single {
		t.Fatalf("networkRefused = %v, %v; want the account refused", single, err)
	}
	refused, err := NetworkRefusedAccounts(ctx, env.pool, []int{user.ID})
	if err != nil || !refused[user.ID] || LocalPasswordRefusal(user, true, refused[user.ID]) == nil {
		t.Fatalf("view rule = %v, %v; want the account refused", refused, err)
	}
	if _, err := issue(); !errors.Is(err, ErrSignInLinkNotPermitted) {
		t.Fatalf("making a link for a refused account: %v", err)
	}
	if links, _, _ := linkState(t); links != 0 {
		t.Fatalf("a refused make stored %d links", links)
	}

	refuse(t, false)
	issued, err := issue()
	if err != nil {
		t.Fatalf("make: %v", err)
	}
	refuse(t, true)
	if _, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true); !errors.Is(err, ErrSignInLinkNotPermitted) {
		t.Fatalf("redeem after a refusal: %v", err)
	}
	if _, redeemed, _ := linkState(t); redeemed {
		t.Fatal("a refused redeem spent the link")
	}
	refuse(t, false)
	got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
	if err != nil || got.SetPasswordToken == "" {
		t.Fatalf("redeem once permitted = %+v, %v", got, err)
	}

	refuse(t, true)
	if _, err := env.signIn.SetPasswordWithSignInLink(ctx, got.SetPasswordToken, "network refused pw"); !errors.Is(err, ErrSignInLinkNotPermitted) {
		t.Fatalf("set-password after a refusal: %v", err)
	}
	if _, _, used := linkState(t); used {
		t.Fatal("a refused set-password spent the token")
	}
	refuse(t, false)
	if _, err := env.signIn.SetPasswordWithSignInLink(ctx, got.SetPasswordToken, "network permitted pw"); err != nil {
		t.Fatalf("set-password once permitted: %v", err)
	}
}
