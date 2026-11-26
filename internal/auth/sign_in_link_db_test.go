package auth

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/models"
)

// setSettingRestored sets a server setting for the test and puts back what
// was there before when it ends.
func (e *keyEnv) setSettingRestored(t *testing.T, key, value string) {
	t.Helper()
	t.Cleanup(saveSettings(t, e.pool, key))
	e.setSetting(t, key, value)
}

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

	// The switch is read inside the redemption: app key sign-in off, or demo
	// mode on, answers the set-password token only. A caller that couldn't
	// hand a code out while keys are on is refused and the link stays
	// unspent.
	for _, tc := range []struct{ name, key, value string }{
		{"app key sign-in off", config.AuthAppKeySignInSettingKey, "false"},
		{"demo mode on", demomode.SettingKey, "true"},
	} {
		t.Run(tc.name+": only the set-password token", func(t *testing.T) {
			issued := env.issueLink(t, env.localAccount(t, "link-off-"+strconv.Itoa(len(tc.name)), models.RoleUser))
			env.setSettingRestored(t, tc.key, tc.value)
			got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
			if err != nil || got.AppCode != nil || got.NoCode != NoCodeAppKeysOff || got.SetPasswordToken == "" {
				t.Fatalf("redeem = %+v, %v", got, err)
			}
		})
	}
	t.Run("no address to hand a code out with", func(t *testing.T) {
		issued := env.issueLink(t, env.localAccount(t, "link-noaddress", models.RoleUser))
		if _, err := env.signIn.RedeemSignInLink(ctx, issued.Token, false); !errors.Is(err, ErrSignInLinkNoAddress) {
			t.Fatalf("redeem: %v", err)
		}
		if _, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true); err != nil {
			t.Fatalf("the link was spent by the refused redeem: %v", err)
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

// linkCode redeems a new sign-in link for user with app keys on and answers
// what the redeem handed out.
func (e *keyEnv) linkCode(t *testing.T, user *models.User) SignInLinkRedemption {
	t.Helper()
	got, err := e.signIn.RedeemSignInLink(t.Context(), e.issueLink(t, user).Token, true)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// personKeyCount counts the account's person key rows, revoked or not.
func (e *keyEnv) personKeyCount(t *testing.T, userID int) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_person_keys WHERE user_id = $1`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A link gives an administrator the password route only. Whether the
// account is an administrator's is judged when the link is redeemed: a link
// made for an administrator hands out the set-password token and no code,
// enrolling nothing, and one made for an administrator who was demoted since
// hands out a code. Regression guard: a link's code used to enroll active
// keys on an administrator's account, a way in that skips the approval an
// administrator's new device needs.
func TestRedeemSignInLinkAdministratorDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkadmin")
	ctx := t.Context()

	t.Run("an administrator gets the password route only", func(t *testing.T) {
		admin := env.localAccount(t, "linkadmin", models.RoleAdmin)
		got := env.linkCode(t, admin)
		if got.AppCode != nil || got.NoCode != NoCodeAdministrator || got.SetPasswordToken == "" {
			t.Fatalf("redeem = %+v", got)
		}
		if n := env.personKeyCount(t, admin.ID); n != 0 {
			t.Fatalf("person keys = %d, want none", n)
		}
	})

	t.Run("an administrator demoted before redeeming gets a code", func(t *testing.T) {
		admin := env.localAccount(t, "linkdemoted", models.RoleAdmin)
		issued := env.issueLink(t, admin)
		env.exec(t, `UPDATE users SET role = 'user' WHERE id = $1`, admin.ID)
		got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
		if err != nil || got.AppCode == nil || got.NoCode != "" {
			t.Fatalf("redeem = %+v, %v", got, err)
		}
	})

	// Promotion deletes the account's link codes; the code's redeem refuses
	// an administrator's account anyway, so the rule doesn't rest on the
	// deletion alone.
	t.Run("a code redeemed after promotion", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			promote func(t *testing.T, user *models.User)
		}{
			{"promoted the ordinary way, which deletes the code", func(t *testing.T, user *models.User) {
				if err := NewUserRepository(env.pool).Update(ctx, user.ID, models.UpdateUserInput{Role: new(models.RoleAdmin)}); err != nil {
					t.Fatal(err)
				}
			}},
			{"the role changed with the code left in place", func(t *testing.T, user *models.User) {
				env.exec(t, `UPDATE users SET role = 'admin' WHERE id = $1`, user.ID)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				user := env.localAccount(t, "linkpromo-"+strconv.Itoa(len(tc.name)), models.RoleUser)
				code := env.linkCode(t, user).AppCode.Code
				tc.promote(t, user)
				if _, _, err := env.redeemCode(t, newTestKey(t), code); !errors.Is(err, ErrAppCodeInvalid) {
					t.Fatalf("redeem: %v", err)
				}
				if n := env.personKeyCount(t, user.ID); n != 0 {
					t.Fatalf("person keys = %d, want none", n)
				}
			})
		}
	})
}

// A sign-in link's app code enrolls the app's keys on its account, bound to
// no provider. The person lost every device, so the sent person key becomes
// the account's: a different one is revoked (its row kept) with the device
// it introduced that still waits for approval, while a device already
// enrolled keeps its key, and the audit row names the link and the account
// that made it. The account's own key sent again stays, as enrolled.
// Regression guard: keeping the old key would leave whoever holds it able to
// introduce devices the person never approved.
func TestRedeemSignInLinkAppCodeDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkcode")
	ctx := t.Context()

	t.Run("an account with no person key", func(t *testing.T) {
		user := env.localAccount(t, "linkcode-new", models.RoleUser)
		result, device, err := env.redeemCode(t, newTestKey(t), env.linkCode(t, user).AppCode.Code)
		if err != nil || result.PersonKeyStatus != PersonKeyEnrolled || result.Pair == nil {
			t.Fatalf("redeem = %+v, %v", result, err)
		}
		if key := env.deviceKeyRow(t, device); key.ProviderBound || key.EnrolledVia != models.AppKeyEnrolledAppCode {
			t.Fatalf("device key = %+v", key)
		}
	})

	t.Run("the account's own person key", func(t *testing.T) {
		user := env.localAccount(t, "linkcode-same", models.RoleUser)
		person, _, _ := env.enroll(t, user)
		result, _, err := env.redeemCode(t, person, env.linkCode(t, user).AppCode.Code)
		if err != nil || result.PersonKeyStatus != PersonKeyEnrolled || env.personKeyCount(t, user.ID) != 1 {
			t.Fatalf("redeem = %+v, %v; person keys %d", result, err, env.personKeyCount(t, user.ID))
		}
	})

	// The account was an administrator's, so a device the old key
	// introduced still waits for approval, and is now an ordinary account.
	t.Run("a different person key is replaced", func(t *testing.T) {
		user := env.localAccount(t, "linkcode-replace", models.RoleAdmin)
		oldPerson, oldDevice, _ := env.enroll(t, user)
		oldPersonRow, err := env.signIn.keys.LivePersonKey(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, waiting := env.introduceWaiting(t, oldPerson)
		env.exec(t, `UPDATE users SET role = 'user' WHERE id = $1`, user.ID)
		issuer := env.localAccount(t, "linkcode-issuer", models.RoleAdmin)
		env.exec(t, `UPDATE users SET is_owner = true WHERE id = $1`, issuer.ID)
		t.Cleanup(func() {
			if _, err := env.pool.Exec(context.Background(), `UPDATE users SET is_owner = false WHERE id = $1`, issuer.ID); err != nil {
				t.Error(err)
			}
		})
		issued, err := env.signIn.IssueSignInLink(ctx, SignInLinkIssue{UserID: user.ID, IssuedBy: issuer.ID, Delivery: "link"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
		if err != nil || got.AppCode == nil {
			t.Fatalf("link redeem = %+v, %v", got, err)
		}
		var linkID int64
		if err := env.pool.QueryRow(ctx, `SELECT id FROM auth_sign_in_links WHERE user_id = $1`, user.ID).Scan(&linkID); err != nil {
			t.Fatal(err)
		}
		auditCtx := env.addedCtx(t, "/api/v2/auth/app-codes/redeem", nil)
		person, device := newTestKey(t), newTestKey(t)
		result, err := env.signIn.RedeemAppCode(auditCtx, KeyRedeemRequest{
			KeyRequest: from(auditCtx, env.request(t, message{purpose: appkeys.PurposeRedeem, signer: person, device: &device, fields: map[string]any{"code": got.AppCode.Code}})),
			PersonKey:  person.pub,
		})
		if err != nil || result.PersonKeyStatus != PersonKeyReplaced || result.PersonKeyID != person.pub.KeyID() {
			t.Fatalf("redeem = %+v, %v", result, err)
		}
		if !env.personKeyRevoked(t, oldPersonRow.ID) {
			t.Fatal("the replaced person key is still live (or its row is gone)")
		}
		if key := env.deviceKeyRow(t, oldDevice); key.Status != models.DeviceKeyStatusActive || key.RevokedAt != nil {
			t.Fatalf("enrolled device = %+v, want kept", key)
		}
		if key := env.deviceKeyRow(t, waiting); key.Status != models.DeviceKeyStatusDenied {
			t.Fatalf("waiting device = %s, want denied with the old key", key.Status)
		}
		row := audittest.One(t, env.pool, auditmutation.ActionAppCodeRedeemed, auditmutation.TargetUser, strconv.Itoa(user.ID))
		for field, want := range map[string]string{
			"sign_in_link_id":         strconv.FormatInt(linkID, 10),
			"sign_in_link_created_by": strconv.Itoa(issuer.ID),
			"person_key_kid":          `"` + person.pub.KeyID() + `"`,
		} {
			if got := audittest.Value(row.Change(t, field).After); got != want {
				t.Fatalf("%s = %s, want %s", field, got, want)
			}
		}
		if before := audittest.Value(row.Change(t, "person_key_kid").Before); before != `"`+oldPerson.pub.KeyID()+`"` {
			t.Fatalf("replaced person key in the audit row = %s, want %s", before, oldPerson.pub.KeyID())
		}
		row.AssertNoSecrets(t, got.AppCode.Code, issued.Token)
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
	got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
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

// App key sign-in turned off while a redemption is on its way is the
// answer's: the redemption reads the switch under the settings lock its
// transaction holds, so a change committed before the redemption takes that
// lock gives the token-only shape. A second connection holds the settings
// lock, the redemption waits on it, the switch goes off and commits, and the
// redemption answers with no code. Regression guard: the switch was read by
// the caller before the transaction, so this answered with a code.
func TestRedeemSignInLinkReadsSwitchInTransactionDB(t *testing.T) {
	env := newKeyEnv(t, "signinlinkswitchtx")
	ctx := t.Context()
	issued := env.issueLink(t, env.localAccount(t, "link-switchtx", models.RoleUser))
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")

	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // committed below
	var holderPID uint32
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	if err := lockServerSettings(ctx, holder); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		got SignInLinkRedemption
		err error
	}
	done := make(chan answer, 1)
	go func() {
		got, err := env.signIn.RedeemSignInLink(ctx, issued.Token, true)
		done <- answer{got, err}
	}()
	waitBlockedBy(t, env, holderPID)
	if _, err := holder.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'false')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthAppKeySignInSettingKey); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.got.AppCode != nil || got.got.NoCode != NoCodeAppKeysOff || got.got.SetPasswordToken == "" {
		t.Fatalf("redeem = %+v, %v; want the set-password token only, app_keys_off", got.got, got.err)
	}
}

// waitBlockedBy waits until some session of this database waits on a lock
// the session with holderPID holds.
func waitBlockedBy(t *testing.T, env *keyEnv, holderPID uint32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked bool
		if err := env.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the redemption never waited on the settings lock")
		}
		runtime.Gosched()
	}
}
