package auth

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// passwordSignInFixture holds a sign-in service without a directory, one with
// a credentials directory at the env's installation, and a check that reads
// the account fresh before asking.
type passwordSignInFixture struct {
	env           *externalSignInEnv
	users         *UserRepository
	plain         *Service
	withDirectory *Service
}

func newPasswordSignInFixture(t *testing.T) *passwordSignInFixture {
	t.Helper()
	env := newExternalSignInEnv(t)
	users := NewUserRepository(env.pool)
	sessions := NewSessionRepository(env.pool)
	plain := NewService(NewLocalProvider(users, sessions), nil, sessions, users, nil, nil, nil)
	withDirectory := NewService(NewLocalProvider(users, sessions), nil, sessions, users, nil, nil, nil)
	withDirectory.SetPluginProviderSource(staticProviderSource{{
		Info:     LoginProviderInfo{ID: PluginProviderID(env.installationID, "ldap"), DisplayName: "Directory", Mode: ProviderModeCredentials, InstallationID: env.installationID},
		Provider: &credentialsDirectory{},
	}})
	return &passwordSignInFixture{env: env, users: users, plain: plain, withDirectory: withDirectory}
}

func (f *passwordSignInFixture) available(t *testing.T, svc *Service, user *models.User) bool {
	t.Helper()
	fresh, err := f.users.GetByID(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := svc.PasswordSignInAvailable(t.Context(), fresh)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func (f *passwordSignInFixture) withoutLocalPassword(t *testing.T, label, role string) *models.User {
	t.Helper()
	user := f.env.localAccount(t, label, role)
	off := false
	if err := f.users.Update(t.Context(), user.ID, models.UpdateUserInput{LocalPasswordLoginEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	return user
}

// TestPasswordSignInAvailableDB pins what Connect Apps tells an account about
// Jellyfin sign-in against what a password sign-in naming no provider does
// (#2871): a local password counts while LocalProvider.Authenticate would
// accept it, and an account without one counts when the directory takes its
// password, through a link or the email match its first sign-in would make.
func TestPasswordSignInAvailableDB(t *testing.T) {
	f := newPasswordSignInFixture(t)
	ctx := t.Context()

	member := f.env.localAccount(t, "signin-member", models.RoleUser)
	glass := f.env.localAccount(t, "signin-glass", models.RoleAdmin)
	yes := true
	if err := f.users.Update(ctx, glass.ID, models.UpdateUserInput{BreakGlass: &yes}); err != nil {
		t.Fatal(err)
	}
	if !f.available(t, f.plain, member) || !f.available(t, f.withDirectory, member) {
		t.Fatal("a local password account: want available")
	}
	noHash, err := f.users.GetByID(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	noHash.PasswordHash = ""
	if ok, err := f.plain.PasswordSignInAvailable(ctx, noHash); err != nil || ok {
		t.Fatalf("an account without a password = %v, %v; want unavailable", ok, err)
	}

	// The server switch stops every local password but a break-glass one.
	f.env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
	if f.available(t, f.plain, member) {
		t.Fatal("a local password account with local sign-in off: want unavailable")
	}
	if !f.available(t, f.plain, glass) {
		t.Fatal("a break-glass account with local sign-in off: want available")
	}
	f.env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")

	// Linking to the directory turns the account's own password off; its
	// password sign-in then goes to the directory, which takes the
	// directory's password. Without a directory, or unlinked, nothing does.
	directoryAccount := f.env.localAccount(t, "signin-ldap", models.RoleUser)
	unlinked := f.withoutLocalPassword(t, "signin-unlinked", models.RoleUser)
	if _, err := NewIdentityService(f.env.pool).AdminLink(ctx, AdminLinkInput{UserID: directoryAccount.ID, InstallationID: f.env.installationID,
		Identity: ExternalIdentity{Subject: "ldap|" + f.env.name("signin-ldap"), Username: f.env.name("signin-ldap")}}); err != nil {
		t.Fatal(err)
	}
	if !f.available(t, f.withDirectory, directoryAccount) {
		t.Fatal("an account linked to the directory: want available")
	}
	if f.available(t, f.plain, directoryAccount) {
		t.Fatal("an account linked to a directory that isn't a credentials provider: want unavailable")
	}
	if f.available(t, f.withDirectory, unlinked) {
		t.Fatal("an account without a password or a directory link: want unavailable")
	}

	// With email auto-match on, the directory's first sign-in links an
	// ordinary account by its email, so its directory password works. An
	// administrator is never matched by email.
	unlinkedAdmin := f.withoutLocalPassword(t, "signin-unlinked-admin", models.RoleAdmin)
	f.env.setSetting(t, config.AuthEmailAutoMatchSettingKey, "true")
	if !f.available(t, f.withDirectory, unlinked) {
		t.Fatal("an unlinked account the directory would match by email: want available")
	}
	if f.available(t, f.plain, unlinked) {
		t.Fatal("an unlinked account without a directory, email auto-match on: want unavailable")
	}
	if f.available(t, f.withDirectory, unlinkedAdmin) {
		t.Fatal("an unlinked administrator, email auto-match on: want unavailable")
	}
}

// TestPasswordSignInAvailableNetworkRefusalDB: a network provider that
// refuses the person stops the local password, as LocalProvider.Authenticate
// does.
func TestPasswordSignInAvailableNetworkRefusalDB(t *testing.T) {
	f := newPasswordSignInFixture(t)
	ctx := t.Context()
	member := f.env.localAccount(t, "refusal-member", models.RoleUser)

	network := newExternalSignInEnv(t)
	for _, stmt := range []string{
		`UPDATE plugin_auth_bindings SET capability_id = 'tailscale', enabled = true WHERE plugin_installation_id = $1`,
		`INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
			VALUES ($1, 'auth_provider.v1', 'tailscale', '{"auth_modes":["network"]}') ON CONFLICT DO NOTHING`,
	} {
		if _, err := f.env.pool.Exec(ctx, stmt, network.installationID); err != nil {
			t.Fatal(err)
		}
	}
	linked, err := NewIdentityService(f.env.pool).AdminLink(ctx, AdminLinkInput{UserID: member.ID, InstallationID: network.installationID,
		Identity: ExternalIdentity{Subject: "tailnet|" + f.env.name("refusal-member"), Username: f.env.name("refusal-member")}})
	if err != nil {
		t.Fatal(err)
	}
	if !f.available(t, f.plain, member) {
		t.Fatal("a local password account linked to a network provider: want available")
	}
	if _, err := f.env.pool.Exec(ctx, `UPDATE plugin_auth_identities SET last_check_status = $2 WHERE id = $1`, linked.ID, CheckStatusNotFound); err != nil {
		t.Fatal(err)
	}
	if f.available(t, f.plain, member) {
		t.Fatal("a local password account its network provider refuses: want unavailable")
	}
}
