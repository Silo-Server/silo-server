package auth

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// exec runs a statement that sets up a case.
func (e *keyEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// issueLink makes a sign-in link for user, issued by the account itself,
// which the Owner rules allow for an account with password sign-in on.
func (e *keyEnv) issueLink(t *testing.T, user *models.User) IssuedSignInLink {
	t.Helper()
	issued, err := e.signIn.IssueSignInLink(t.Context(), SignInLinkIssue{UserID: user.ID, IssuedBy: user.ID, Delivery: "link"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return issued
}

// A sign-in link works once and only while it is good: a second redeem,
// a link a newer one replaced, an expired link and a disabled account's
// link each answer the same ErrSignInLinkInvalid. A redeem hands out an app
// code for the account (the code a page must never put in a link) and a
// set-password token; with app keys off only the token. Regression guard:
// the link is a bearer credential for the account for a day.
func TestRedeemSignInLinkDB(t *testing.T) {
	env := newKeyEnv(t, "signinlink")
	ctx := t.Context()

	t.Run("works once", func(t *testing.T) {
		user := env.localAccount(t, "link-once", models.RoleUser)
		issued := env.issueLink(t, user)
		got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
		if err != nil {
			t.Fatal(err)
		}
		if got.AppCode == nil || got.AppCode.Code == "" || got.AppCode.LinkAllowed || got.SetPasswordToken == "" {
			t.Fatalf("redeem = %+v", got)
		}
		if _, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true); !errors.Is(err, ErrSignInLinkInvalid) {
			t.Fatalf("second redeem: %v", err)
		}
	})

	t.Run("app keys off: only the set-password token", func(t *testing.T) {
		got, err := env.signIn.RedeemSignInLink(ctx, env.issueLink(t, env.localAccount(t, "link-keysoff", models.RoleUser)).Token, false)
		if err != nil || got.AppCode != nil || got.SetPasswordToken == "" {
			t.Fatalf("redeem = %+v, %v", got, err)
		}
	})

	for _, tc := range []struct {
		name  string
		spoil func(t *testing.T, user *models.User, issued IssuedSignInLink)
	}{
		{"replaced by a newer link", func(t *testing.T, user *models.User, _ IssuedSignInLink) { env.issueLink(t, user) }},
		{"expired", func(t *testing.T, user *models.User, _ IssuedSignInLink) {
			env.exec(t, `UPDATE auth_sign_in_links SET expires_at = NOW() - INTERVAL '1 second' WHERE user_id = $1`, user.ID)
		}},
		{"its account disabled", func(t *testing.T, user *models.User, _ IssuedSignInLink) {
			env.exec(t, `UPDATE users SET enabled = false WHERE id = $1`, user.ID)
		}},
		{"its account promoted to administrator", func(t *testing.T, user *models.User, _ IssuedSignInLink) {
			if err := NewUserRepository(env.pool).Update(ctx, user.ID, models.UpdateUserInput{Role: new(models.RoleAdmin)}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := env.localAccount(t, "link-"+strconv.Itoa(len(tc.name)), models.RoleUser)
			issued := env.issueLink(t, user)
			tc.spoil(t, user, issued)
			if _, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true); !errors.Is(err, ErrSignInLinkInvalid) {
				t.Fatalf("redeem: %v", err)
			}
		})
	}

	t.Run("refused while local passwords are off, and left unspent", func(t *testing.T) {
		user := env.localAccount(t, "link-provider", models.RoleUser)
		issued := env.issueLink(t, user)
		env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
		_, issueErr := env.signIn.IssueSignInLink(ctx, SignInLinkIssue{UserID: user.ID})
		_, redeemErr := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
		env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
		if !errors.Is(issueErr, ErrSignInLinkNeedsProvider) || !errors.Is(redeemErr, ErrSignInLinkNeedsProvider) {
			t.Fatalf("issue = %v, redeem = %v", issueErr, redeemErr)
		}
		if _, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true); err != nil {
			t.Fatalf("redeem once passwords are back on: %v", err)
		}
	})

	t.Run("a disabled account gets no link", func(t *testing.T) {
		user := env.localAccount(t, "link-disabled", models.RoleUser)
		env.exec(t, `UPDATE users SET enabled = false WHERE id = $1`, user.ID)
		if _, err := env.signIn.IssueSignInLink(ctx, SignInLinkIssue{UserID: user.ID}); !errors.Is(err, ErrUserDisabled) {
			t.Fatalf("issue: %v", err)
		}
	})
}

// Of two concurrent redemptions of one link, on any API processes, exactly
// one succeeds.
func TestRedeemSignInLinkConcurrentDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkrace")
	issued := env.issueLink(t, env.localAccount(t, "link-race", models.RoleUser))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() { _, errs[i] = env.signIn.RedeemSignInLink(t.Context(), issued.Token, true) })
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) || (!errors.Is(errs[0], ErrSignInLinkInvalid) && !errors.Is(errs[1], ErrSignInLinkInvalid)) {
		t.Fatalf("redeems = %v, %v; want exactly one success", errs[0], errs[1])
	}
}

// A sign-in link's app code enrolls the app's keys on its account, active
// on an administrator's account too and bound to no provider. The person
// lost every device, so the sent person key becomes the account's: a
// different one is revoked with the device it introduced that still waits
// for approval, while a device already enrolled keeps its key. Regression
// guard: keeping the old key would leave whoever holds it able to introduce
// devices the person never approved.
func TestRedeemSignInLinkAppCodeDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkcode")
	ctx := t.Context()
	code := func(t *testing.T, user *models.User) string {
		t.Helper()
		got, err := env.signIn.RedeemSignInLink(ctx, env.issueLink(t, user).Token, true)
		if err != nil {
			t.Fatal(err)
		}
		return got.AppCode.Code
	}

	t.Run("an account with no person key", func(t *testing.T) {
		user := env.localAccount(t, "linkcode-new", models.RoleUser)
		result, device, err := env.redeemCode(t, newTestKey(t), code(t, user))
		if err != nil || result.PersonKeyStatus != PersonKeyEnrolled || result.Pair == nil {
			t.Fatalf("redeem = %+v, %v", result, err)
		}
		if key := env.deviceKeyRow(t, device); key.ProviderBound || key.EnrolledVia != models.AppKeyEnrolledAppCode {
			t.Fatalf("device key = %+v", key)
		}
	})

	t.Run("a different person key is replaced", func(t *testing.T) {
		user := env.localAccount(t, "linkcode-replace", models.RoleAdmin)
		oldPerson, oldDevice, _ := env.enroll(t, user)
		oldPersonRow, err := env.signIn.keys.LivePersonKey(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, waiting := env.introduceWaiting(t, oldPerson)
		result, device, err := env.redeemCode(t, newTestKey(t), code(t, user))
		if err != nil || result.PersonKeyStatus != PersonKeyReplaced || result.Pair == nil {
			t.Fatalf("redeem = %+v, %v", result, err)
		}
		if !env.personKeyRevoked(t, oldPersonRow.ID) {
			t.Fatal("the replaced person key is still live")
		}
		if key := env.deviceKeyRow(t, device); key.Status != models.DeviceKeyStatusActive {
			t.Fatalf("new device on an administrator's account = %s, want active", key.Status)
		}
		if key := env.deviceKeyRow(t, oldDevice); key.Status != models.DeviceKeyStatusActive || key.RevokedAt != nil {
			t.Fatalf("enrolled device = %+v, want kept", key)
		}
		if key := env.deviceKeyRow(t, waiting); key.Status != models.DeviceKeyStatusDenied {
			t.Fatalf("waiting device = %s, want denied with the old key", key.Status)
		}
	})
}

// Setting a password with a redeemed link's token sets it, turns the
// account's local password sign-in on (an account made with no password
// has it off) and signs the account out everywhere, as a reset does; app
// keys stay. The token works once, and a password the rules refuse
// doesn't spend it.
func TestSetPasswordWithSignInLinkDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkpw")
	ctx := t.Context()
	user := env.localAccount(t, "linkpw", models.RoleUser)
	_, device, _ := env.enroll(t, user)
	issued := env.issueLink(t, user)
	env.exec(t, `UPDATE users SET password_hash = NULL, local_password_login_enabled = false WHERE id = $1`, user.ID)
	other := env.loginSession(t, user, models.SessionOpenedProvider, nil)
	got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.signIn.SetPasswordWithSignInLink(ctx, got.SetPasswordToken, "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("short password: %v", err)
	}
	if _, err := env.signIn.SetPasswordWithSignInLink(ctx, got.SetPasswordToken, "margin fossil quench"); err != nil {
		t.Fatal(err)
	}
	account, err := UserByID(ctx, env.pool, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !account.HasLocalPassword() {
		t.Fatalf("account = %+v, want a usable local password", account)
	}
	var revoked bool
	if err := env.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM auth_sessions WHERE id = $1`, other.SessionID).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("the account's other session revoked = %v (%v)", revoked, err)
	}
	if key := env.deviceKeyRow(t, device); key.RevokedAt != nil {
		t.Fatal("setting a password revoked an app key")
	}
	if _, err := env.signIn.SetPasswordWithSignInLink(ctx, got.SetPasswordToken, "another fine password"); !errors.Is(err, ErrSignInLinkInvalid) {
		t.Fatalf("second use: %v", err)
	}
}

// Making a link is audited with its delivery and expiry, never the token.
func TestIssueSignInLinkAuditDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkaudit")
	user := env.localAccount(t, "linkaudit", models.RoleUser)
	ctx, _ := audittest.Context(t, env.pool, audittest.Request{Method: http.MethodPost,
		Pattern: "/api/v2/admin/users/{user_id}/sign-in-links", Path: "/api/v2/admin/users/1/sign-in-links", OptIn: true})
	issued, err := env.signIn.IssueSignInLink(ctx, SignInLinkIssue{UserID: user.ID, Delivery: "email"})
	if err != nil {
		t.Fatal(err)
	}
	row := audittest.One(t, env.pool, auditmutation.ActionSignInLinkCreated, auditmutation.TargetUser, strconv.Itoa(user.ID))
	if got := audittest.Value(row.Change(t, "delivery").After); got != `"email"` {
		t.Fatalf("delivery = %s", got)
	}
	row.AssertNoSecrets(t, issued.Token)
}
