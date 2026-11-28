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
				name := account.Username
				if name == "" {
					// An account without a username is known by its email.
					name, _, _ = strings.Cut(account.Email, "@")
				}
				if strings.HasSuffix(name, env.suffix) {
					label := strings.TrimSuffix(name, "-"+env.suffix)
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

// exec runs one statement or fails the test.
func (e *externalSignInEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// authInstallation seeds an auth plugin installation with one capability
// whose auth_modes are modes (no capability row when empty) and its binding.
func (e *externalSignInEnv) authInstallation(t *testing.T, label, capability, modes string, enabled bool) int {
	t.Helper()
	var id int
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/'||$1, true, 'manual', 'plugin') RETURNING id`, e.name(label)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id)
	})
	if modes != "" {
		e.exec(t, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
			VALUES ($1, 'auth_provider.v1', $2, jsonb_build_object('auth_modes', jsonb_build_array($3::text)))`, id, capability, modes)
	}
	e.exec(t, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, $2, $3)`, id, capability, enabled)
	return id
}

// seedLocalPasswordOffAccounts seeds one account per rule the read applies,
// each named for it, and answers the one linked only to a network provider.
func seedLocalPasswordOffAccounts(t *testing.T, env *externalSignInEnv) *models.User {
	t.Helper()
	ctx := t.Context()
	users := NewUserRepository(env.pool)
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
	env.exec(t, `UPDATE users SET email = NULL WHERE id = $1`, noEmail.ID)
	// Links seeded straight in keep the password on, as a network link or a
	// break-glass link does.
	link := func(label string, installation int) *models.User {
		user := env.localAccount(t, label, models.RoleUser)
		if _, err := insertIdentity(ctx, env.pool, user.ID, installation, env.identity(label+"-idp"), false, false); err != nil {
			t.Fatal(err)
		}
		return user
	}
	link("linked", env.installationID)
	link("stale-link", env.authInstallation(t, "old-provider", "oidc", "", false))
	// A link only to a network provider signs in only from that network
	// once passwords are off.
	networkLinked := link("network-link", env.authInstallation(t, "tailnet", "tailnet", "network", true))
	// The flag alone is not a password that signs in.
	noHash := env.localAccount(t, "no-hash", models.RoleUser)
	env.exec(t, `UPDATE users SET password_hash = '' WHERE id = $1`, noHash.ID)
	// A username is optional; such an account sorts first and must still
	// appear on exactly one page.
	noUsername := env.localAccount(t, "no-username", models.RoleUser)
	env.exec(t, `UPDATE users SET username = NULL WHERE id = $1`, noUsername.ID)
	return networkLinked
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
	env.exec(t, `UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID)
	matchOn, matchOff := true, false
	_, before := localPasswordOffOutcomes(t, env, &matchOn)

	networkLinked := seedLocalPasswordOffAccounts(t, env)

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
			"network-link":   emailMatch,
			"no-username":    emailMatch,
			"admin":          {LocalPasswordOffAdminLink, LocalPasswordOffReasonAdministrator},
			"no-email":       {LocalPasswordOffAdminLink, LocalPasswordOffReasonNoEmail},
		}},
		{"matching off", &matchOff, map[string]localPasswordOffOutcome{
			"setup-complete": {LocalPasswordOffAdminLink, LocalPasswordOffReasonEmailMatchOff},
			"member":         {LocalPasswordOffAdminLink, LocalPasswordOffReasonEmailMatchOff},
			"stale-link":     {LocalPasswordOffAdminLink, LocalPasswordOffReasonEmailMatchOff},
			"network-link":   {LocalPasswordOffAdminLink, LocalPasswordOffReasonEmailMatchOff},
			"no-username":    {LocalPasswordOffAdminLink, LocalPasswordOffReasonEmailMatchOff},
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
				if totals[0]-before[0] != 4 || totals[1]-before[1] != 2 {
					t.Errorf("totals %v moved from %v; want +4 email_match, +2 admin_link", totals, before)
				}
			}
		})
	}

	// Email matching does connect the network-linked account: its
	// "already linked" check looks only at the provider signing in.
	verified := true
	identity := env.identity("network-match")
	identity.Email = networkLinked.Email
	identity.EmailVerified = &verified
	env.setSetting(t, config.AuthEmailAutoMatchSettingKey, "true")
	if user, err := env.resolve(t, identity, false, 0); err != nil || user.ID != networkLinked.ID {
		t.Fatalf("email match of the network-linked account = %+v, %v", user, err)
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
// the switch (checkSessionStanding), so each session ends as it otherwise
// would: a password session, as here, keeps sliding at each refresh until the
// account signs out, an administrator ends it, or it goes unrefreshed past
// auth.refresh_token_expiry (a provider session is re-checked with its
// provider as before). The account can still approve a new device. The
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

	// From that session the account can still approve a new device, which
	// then gets a session of its own.
	devices := NewDeviceLoginService(env.pool, users, NewJWTService("synthetic-jwt-secret-synthetic-jwt-secret", time.Minute, time.Hour), sessions, nil, nil)
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.Background(), `DELETE FROM device_login_requests WHERE device_name = $1`, env.name("tv"))
	})
	start, err := devices.Start(ctx, DeviceLoginStartInput{DeviceName: env.name("tv")})
	if err != nil {
		t.Fatal(err)
	}
	if err := devices.Approve(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}, member.ID); err != nil {
		t.Fatalf("device approval from the open session = %v; want it to work", err)
	}
	if poll, err := devices.Poll(ctx, start.DeviceCode, ""); err != nil || poll.TokenPair == nil {
		t.Fatalf("approved device poll = %+v, %v", poll, err)
	}
}

// TestLocalPasswordOffCountsOpenIDConnectLinkOnlyWithPublicURLDB: the read
// counts a link as a way in by the guard's rule (signInProviders). An OpenID
// Connect provider signs nobody in without a public URL, so an account linked
// only to one is listed then; a directory provider needs no public URL.
func TestLocalPasswordOffCountsOpenIDConnectLinkOnlyWithPublicURLDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, env.pool, config.ServerPublicURLSettingKey))
	link := func(label string, installation int) {
		user := env.localAccount(t, label, models.RoleUser)
		if _, err := insertIdentity(ctx, env.pool, user.ID, installation, env.identity(label+"-idp"), false, false); err != nil {
			t.Fatal(err)
		}
	}
	link("oidc-only", env.authInstallation(t, "url-oidc", "oidc", "oauth2", true))
	link("ldap-only", env.authInstallation(t, "url-ldap", "ldap", "password", true))
	matching := true
	for _, tc := range []struct {
		publicURL  string
		oidcListed bool
	}{{"", true}, {"https://silo.example.test", false}} {
		env.setSetting(t, config.ServerPublicURLSettingKey, tc.publicURL)
		got, _ := localPasswordOffOutcomes(t, env, &matching)
		if _, listed := got["oidc-only"]; listed != tc.oidcListed {
			t.Errorf("public URL %q: account linked only to OpenID Connect listed = %v, want %v", tc.publicURL, listed, tc.oidcListed)
		}
		if _, listed := got["ldap-only"]; listed {
			t.Errorf("public URL %q: account linked to a directory provider listed", tc.publicURL)
		}
	}
}
