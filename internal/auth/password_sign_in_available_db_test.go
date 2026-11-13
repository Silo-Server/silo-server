package auth

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestPasswordSignInAvailableDB pins what Connect Apps tells an account about
// Jellyfin sign-in against what a password sign-in naming no provider does
// (#2871): a local password counts while LocalProvider.Authenticate would
// accept it, and an account without one counts when the directory it is
// linked to takes the password.
func TestPasswordSignInAvailableDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	users := NewUserRepository(env.pool)
	sessions := NewSessionRepository(env.pool)
	plain := NewService(NewLocalProvider(users, sessions), nil, sessions, users, nil, nil, nil)
	withDirectory := NewService(NewLocalProvider(users, sessions), nil, sessions, users, nil, nil, nil)
	withDirectory.SetPluginProviderSource(staticProviderSource{{
		Info:     LoginProviderInfo{ID: PluginProviderID(env.installationID, "ldap"), DisplayName: "Directory", Mode: ProviderModeCredentials, InstallationID: env.installationID},
		Provider: &credentialsDirectory{},
	}})
	available := func(svc *Service, user *models.User) bool {
		t.Helper()
		fresh, err := users.GetByID(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := svc.PasswordSignInAvailable(ctx, fresh)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	member := env.localAccount(t, "signin-member", models.RoleUser)
	glass := env.localAccount(t, "signin-glass", models.RoleAdmin)
	yes := true
	if err := users.Update(ctx, glass.ID, models.UpdateUserInput{BreakGlass: &yes}); err != nil {
		t.Fatal(err)
	}
	if !available(plain, member) || !available(withDirectory, member) {
		t.Fatal("a local password account: want available")
	}
	noHash, err := users.GetByID(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	noHash.PasswordHash = ""
	if ok, err := plain.PasswordSignInAvailable(ctx, noHash); err != nil || ok {
		t.Fatalf("an account without a password = %v, %v; want unavailable", ok, err)
	}

	// The server switch stops every local password but a break-glass one.
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
	if available(plain, member) {
		t.Fatal("a local password account with local sign-in off: want unavailable")
	}
	if !available(plain, glass) {
		t.Fatal("a break-glass account with local sign-in off: want available")
	}
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")

	// Linking to the directory turns the account's own password off; its
	// password sign-in then goes to the directory, which takes the
	// directory's password. Without a directory, or unlinked, nothing does.
	directoryAccount := env.localAccount(t, "signin-ldap", models.RoleUser)
	unlinked := env.localAccount(t, "signin-unlinked", models.RoleUser)
	off := false
	if err := users.Update(ctx, unlinked.ID, models.UpdateUserInput{LocalPasswordLoginEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewIdentityService(env.pool).AdminLink(ctx, AdminLinkInput{UserID: directoryAccount.ID, InstallationID: env.installationID,
		Identity: ExternalIdentity{Subject: "ldap|" + env.name("signin-ldap"), Username: env.name("signin-ldap")}}); err != nil {
		t.Fatal(err)
	}
	if !available(withDirectory, directoryAccount) {
		t.Fatal("an account linked to the directory: want available")
	}
	if available(plain, directoryAccount) {
		t.Fatal("an account linked to a directory that isn't a credentials provider: want unavailable")
	}
	if available(withDirectory, unlinked) {
		t.Fatal("an account without a password or a directory link: want unavailable")
	}

	// A network provider that refuses the person stops the local password,
	// as LocalProvider.Authenticate does.
	network := newExternalSignInEnv(t)
	for _, stmt := range []string{
		`UPDATE plugin_auth_bindings SET capability_id = 'tailscale', enabled = true WHERE plugin_installation_id = $1`,
		`INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
			VALUES ($1, 'auth_provider.v1', 'tailscale', '{"auth_modes":["network"]}') ON CONFLICT DO NOTHING`,
	} {
		if _, err := env.pool.Exec(ctx, stmt, network.installationID); err != nil {
			t.Fatal(err)
		}
	}
	linked, err := NewIdentityService(env.pool).AdminLink(ctx, AdminLinkInput{UserID: member.ID, InstallationID: network.installationID,
		Identity: ExternalIdentity{Subject: "tailnet|" + env.name("signin-member"), Username: env.name("signin-member")}})
	if err != nil {
		t.Fatal(err)
	}
	if !available(plain, member) {
		t.Fatal("a local password account linked to a network provider: want available")
	}
	if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_identities SET last_check_status = $2 WHERE id = $1`, linked.ID, CheckStatusNotFound); err != nil {
		t.Fatal(err)
	}
	if available(plain, member) {
		t.Fatal("a local password account its network provider refuses: want unavailable")
	}
	if !available(plain, glass) {
		t.Fatal("a break-glass account: want available")
	}
}
