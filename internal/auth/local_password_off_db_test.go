package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// localPasswordOffOutcome is where one account landed: its group and
// reason, or "" for not listed.
type localPasswordOffOutcome struct{ group, reason string }

// localPasswordOffOutcomes walks every page of both groups, one row per
// page so the keyset is crossed, and answers the outcome of each account
// whose name carries the test's suffix, with both totals.
func localPasswordOffOutcomes(t *testing.T, env *externalSignInEnv, matching *bool) (map[string]localPasswordOffOutcome, [2]int) {
	t.Helper()
	svc := NewIdentityService(env.pool)
	out := map[string]localPasswordOffOutcome{}
	var totals [2]int
	for _, group := range []string{LocalPasswordOffEmailMatch, LocalPasswordOffAdminLink} {
		q := LocalPasswordOffQuery{Group: group, EmailAutoMatch: matching, Limit: 1}
		for pages := 0; ; pages++ {
			if pages > 10_000 {
				t.Fatal("pagination did not end")
			}
			page, err := svc.LocalPasswordOffAccounts(t.Context(), q)
			if err != nil {
				t.Fatalf("%s: %v", group, err)
			}
			totals = [2]int{page.EmailMatchTotal, page.AdminLinkTotal}
			if len(page.Accounts) > 1 {
				t.Fatalf("%s page has %d rows, limit 1", group, len(page.Accounts))
			}
			for _, account := range page.Accounts {
				if strings.HasSuffix(account.Username, env.suffix) {
					label := strings.TrimSuffix(account.Username, "-"+env.suffix)
					if _, seen := out[label]; seen {
						t.Fatalf("%s listed twice", label)
					}
					out[label] = localPasswordOffOutcome{group, account.Reason}
				}
			}
			if !page.More {
				break
			}
			last := page.Accounts[len(page.Accounts)-1]
			q.AfterUsername, q.AfterID = last.Username, last.ID
		}
	}
	return out, totals
}

// TestLocalPasswordOffAccountsDB pins who the read lists before password
// sign-in is turned off, and in which group, against the rules sign-in
// applies: break-glass accounts keep their password, a usable linked
// identity is a way in, and email auto-match links only ordinary accounts
// with an email. A wrong predicate here tells an administrator an account
// is safe when it is locked out, or the reverse.
func TestLocalPasswordOffAccountsDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	users := NewUserRepository(env.pool)
	if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID); err != nil {
		t.Fatal(err)
	}
	matchOn, matchOff := true, false
	_, before := localPasswordOffOutcomes(t, env, &matchOn)

	env.localAccount(t, "member", models.RoleUser)
	env.localAccount(t, "admin", models.RoleAdmin)
	glass := env.localAccount(t, "glass", models.RoleAdmin)
	if err := users.Update(ctx, glass.ID, models.UpdateUserInput{BreakGlass: new(true)}); err != nil {
		t.Fatal(err)
	}
	disabled := env.localAccount(t, "disabled", models.RoleUser)
	if err := users.Update(ctx, disabled.ID, models.UpdateUserInput{Enabled: new(false)}); err != nil {
		t.Fatal(err)
	}
	noEmail := env.localAccount(t, "no-email", models.RoleUser)
	if _, err := env.pool.Exec(ctx, `UPDATE users SET email = NULL WHERE id = $1`, noEmail.ID); err != nil {
		t.Fatal(err)
	}
	// Seeded straight in, so the links keep the password on as a network
	// link or a break-glass link does.
	linked := env.localAccount(t, "linked", models.RoleUser)
	if _, err := insertIdentity(ctx, env.pool, linked.ID, env.installationID, env.identity("linked-idp"), false, false); err != nil {
		t.Fatal(err)
	}
	var oldProvider int
	if err := env.pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/local-password-off-test', true, 'manual', 'plugin') RETURNING id`,
		"local-password-off-"+env.suffix).Scan(&oldProvider); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM plugin_installations WHERE id = $1`, oldProvider)
	})
	if _, err := env.pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'oidc', false)`, oldProvider); err != nil {
		t.Fatal(err)
	}
	stale := env.localAccount(t, "stale-link", models.RoleUser)
	if _, err := insertIdentity(ctx, env.pool, stale.ID, oldProvider, env.identity("stale-idp"), false, false); err != nil {
		t.Fatal(err)
	}

	emailMatch := localPasswordOffOutcome{LocalPasswordOffEmailMatch, LocalPasswordOffReasonEmailMatch}
	for _, tc := range []struct {
		name     string
		matching *bool
		want     map[string]localPasswordOffOutcome
	}{
		{"matching on", &matchOn, map[string]localPasswordOffOutcome{
			"setup-complete": emailMatch,
			"member":         emailMatch,
			"stale-link":     emailMatch,
			"admin":          {LocalPasswordOffAdminLink, LocalPasswordOffReasonAdministrator},
			"no-email":       {LocalPasswordOffAdminLink, LocalPasswordOffReasonNoEmail},
		}},
		{"matching off", &matchOff, map[string]localPasswordOffOutcome{
			"setup-complete": {LocalPasswordOffAdminLink, LocalPasswordOffReasonEmailMatchOff},
			"member":         {LocalPasswordOffAdminLink, LocalPasswordOffReasonEmailMatchOff},
			"stale-link":     {LocalPasswordOffAdminLink, LocalPasswordOffReasonEmailMatchOff},
			"admin":          {LocalPasswordOffAdminLink, LocalPasswordOffReasonAdministrator},
			"no-email":       {LocalPasswordOffAdminLink, LocalPasswordOffReasonNoEmail},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, totals := localPasswordOffOutcomes(t, env, tc.matching)
			if len(got) != len(tc.want) {
				t.Errorf("listed %v, want %v", got, tc.want)
			}
			for label, want := range tc.want {
				if got[label] != want {
					t.Errorf("%s = %+v, want %+v", label, got[label], want)
				}
			}
			if *tc.matching {
				if totals[0]-before[0] != 2 || totals[1]-before[1] != 2 {
					t.Errorf("totals %v moved from %v; want +2 email_match (member, stale-link), +2 admin_link", totals, before)
				}
			}
		})
	}

	// Without an override the saved setting decides.
	env.setSetting(t, config.AuthEmailAutoMatchSettingKey, "false")
	page, err := NewIdentityService(env.pool).LocalPasswordOffAccounts(ctx, LocalPasswordOffQuery{Group: LocalPasswordOffEmailMatch, Limit: 1})
	if err != nil || page.EmailAutoMatch || page.EmailMatchTotal != 0 {
		t.Fatalf("saved matching off: %+v, %v", page, err)
	}
}

// TestLocalPasswordOffKeepsOpenSessionsDB pins what turning local password
// sign-in off does to sessions already open: nothing. Refresh does not read
// the switch (checkSessionStanding), so an account whose password no longer
// signs in stays signed in until its sessions end or are revoked. The
// Sign-in settings page tells the administrator so; if this changes, that
// text must change with it.
func TestLocalPasswordOffKeepsOpenSessionsDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	users := NewUserRepository(env.pool)
	sessions := NewSessionRepository(env.pool)
	svc := NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-jwt-secret-synthetic-jwt-secret", time.Minute, time.Hour), sessions, users, nil, nil, nil)
	member := env.localAccount(t, "member", models.RoleUser)
	pair, _, err := svc.Login(ctx, member.Username, "correct horse battery", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
	if _, _, err := svc.Login(ctx, member.Username, "correct horse battery", "test", ""); !errors.Is(err, ErrLocalLoginDisabled) {
		t.Fatalf("new password sign-in = %v, want ErrLocalLoginDisabled", err)
	}
	refreshed, err := svc.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatalf("refresh of the open session = %v; want it to keep working", err)
	}
	if _, err := svc.Refresh(ctx, refreshed.RefreshToken); err != nil {
		t.Fatalf("second refresh = %v", err)
	}
}
