package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// networkSignInService wires a Service to a network identity plugin at the
// env's installation, whose binding it declares a network provider's.
func networkSignInService(t *testing.T, env *externalSignInEnv, plugin *peerPlugin, autoProvision bool) *Service {
	t.Helper()
	ctx := t.Context()
	if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_bindings SET capability_id = 'tailscale' WHERE plugin_installation_id = $1`, env.installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'auth_provider.v1', 'tailscale', '{"auth_modes":["network"]}') ON CONFLICT DO NOTHING`, env.installationID); err != nil {
		t.Fatal(err)
	}
	return networkSignInServiceAt(env.pool, env.resolver, env.installationID, plugin, autoProvision)
}

// networkSignInServiceAt wires a Service to a network identity plugin at
// installationID.
func networkSignInServiceAt(pool *pgxpool.Pool, resolver *AccountResolver, installationID int, plugin *peerPlugin, autoProvision bool) *Service {
	users := NewUserRepository(pool)
	sessions := NewSessionRepository(pool)
	svc := NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-jwt-secret-synthetic-jwt-secret", time.Minute, time.Hour), sessions, users, nil, nil, nil)
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: installationID, CapabilityID: "tailscale", AutoProvision: autoProvision},
		sessions, resolver, func(context.Context) (pluginAuthClient, error) { return plugin, nil })
	svc.SetPluginProviderSource(networkProviderSource(installationID, provider))
	return svc
}

func peerIdentity(env *externalSignInEnv, label string) *pluginv1.AuthenticateResponse {
	identity := env.identity(label)
	return &pluginv1.AuthenticateResponse{ExternalSubject: identity.Subject, Issuer: "https://controlplane.tailscale.com",
		Username: identity.Username, Email: identity.Email, DisplayName: identity.DisplayName}
}

// TestNetworkSignInDB: the overlay peer of the request signs in with no
// password; an unknown person gets an account while account creation is on,
// and the session is vouched for by the identity like an OAuth session.
func TestNetworkSignInDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	admin := peerIdentity(env, "tv-owner")
	admin.ManagedRole = pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN
	plugin := &peerPlugin{peers: map[string]*pluginv1.AuthenticateResponse{
		"100.64.0.7": peerIdentity(env, "friend"),
		"100.64.0.8": admin,
	}}
	svc := networkSignInService(t, env, plugin, true)
	signIn := func(peer string) (*TokenPair, error) {
		return svc.NetworkSignIn(overlayContext(ctx, env.installationID, peer), NetworkSignInInput{InstallationID: env.installationID, DeviceName: "Silo/1.0 (tvOS)", IP: peer})
	}

	pair, err := signIn("100.64.0.7")
	if err != nil {
		t.Fatal(err)
	}
	if pair.User == nil || pair.User.Username != env.name("friend") || pair.User.Role != models.RoleUser || pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("pair = %+v", pair)
	}
	identity := env.identityRow(t, env.identity("friend").Subject)
	var sessions int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id = $1 AND identity_id = $2
		AND provider_since IS NOT NULL AND device_name = 'Silo/1.0 (tvOS)'`, pair.User.ID, identity.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("%d sessions vouched for by the identity, want 1", sessions)
	}
	// Signing in again lands on the same account.
	again, err := signIn("100.64.0.7")
	if err != nil || again.User.ID != pair.User.ID {
		t.Fatalf("second sign-in = %+v, %v", again, err)
	}
	if env.activeSessions(t, pair.User.ID) != 2 {
		t.Fatalf("active sessions = %d, want 2", env.activeSessions(t, pair.User.ID))
	}

	// The plugin's policy decides the role.
	promoted, err := signIn("100.64.0.8")
	if err != nil || promoted.User.Role != models.RoleAdmin {
		t.Fatalf("admin grant = %+v, %v", promoted, err)
	}

	if _, err := signIn("100.64.0.9"); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("refused peer = %v, want ErrNotPermitted", err)
	}
	if _, err := svc.NetworkSignIn(ctx, NetworkSignInInput{InstallationID: env.installationID}); !errors.Is(err, ErrNetworkIdentityRequired) {
		t.Fatalf("default path = %v, want ErrNetworkIdentityRequired", err)
	}

	// A disabled account signs in no more.
	off := false
	if err := NewUserRepository(env.pool).Update(ctx, pair.User.ID, models.UpdateUserInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := signIn("100.64.0.7"); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("disabled account = %v, want ErrUserDisabled", err)
	}
}

// TestNetworkSignInWithoutAccountCreationDB: with account creation off an
// unknown person is refused with ErrAccountRequired, and an account holding
// the person's email is not taken over.
func TestNetworkSignInWithoutAccountCreationDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	plugin := &peerPlugin{peers: map[string]*pluginv1.AuthenticateResponse{"100.64.0.7": peerIdentity(env, "stranger")}}
	svc := networkSignInService(t, env, plugin, false)
	if _, err := svc.NetworkSignIn(overlayContext(ctx, env.installationID, "100.64.0.7"), NetworkSignInInput{InstallationID: env.installationID}); !errors.Is(err, ErrAccountRequired) {
		t.Fatalf("err = %v, want ErrAccountRequired", err)
	}

	owner := env.localAccount(t, "owner", models.RoleUser)
	plugin.peers["100.64.0.8"] = peerIdentity(env, "owner")
	svc = networkSignInService(t, env, plugin, true)
	if _, err := svc.NetworkSignIn(overlayContext(ctx, env.installationID, "100.64.0.8"), NetworkSignInInput{InstallationID: env.installationID}); !errors.Is(err, ErrEmailInUse) {
		t.Fatalf("email of a local account = %v, want ErrEmailInUse", err)
	}
	if env.activeSessions(t, owner.ID) != 0 {
		t.Fatal("a refused network sign-in opened a session for the local account")
	}
}

// TestLinkNetworkIdentityDB: a signed-in account links the network identity
// of its request's peer after re-entering its local password; afterwards the
// peer signs in to that account, and the local password still does too.
func TestLinkNetworkIdentityDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	plugin := &peerPlugin{peers: map[string]*pluginv1.AuthenticateResponse{"100.64.0.7": peerIdentity(env, "owner")}}
	svc := networkSignInService(t, env, plugin, true)
	owner := env.localAccount(t, "owner", models.RoleUser)
	overlay := overlayContext(ctx, env.installationID, "100.64.0.7")
	link := func(ctx context.Context, password string) (*LinkedIdentity, error) {
		return svc.LinkNetworkIdentity(ctx, NetworkLinkInput{UserID: owner.ID, InstallationID: env.installationID, Password: password})
	}

	if _, err := link(ctx, "correct horse battery"); !errors.Is(err, ErrNetworkIdentityRequired) {
		t.Fatalf("default path = %v, want ErrNetworkIdentityRequired", err)
	}
	if _, err := link(overlay, "wrong password"); !errors.Is(err, ErrLinkTicketPassword) {
		t.Fatalf("wrong password = %v", err)
	}
	if plugin.askedCount() != 0 {
		t.Fatal("the plugin was asked before the local password was confirmed")
	}
	linked, err := link(overlay, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if linked.UserID != owner.ID || linked.ExternalSubject != env.identity("owner").Subject {
		t.Fatalf("linked = %+v", linked)
	}
	pair, err := svc.NetworkSignIn(overlay, NetworkSignInInput{InstallationID: env.installationID})
	if err != nil || pair.User.ID != owner.ID {
		t.Fatalf("sign-in after linking = %+v, %v", pair, err)
	}
	users := NewUserRepository(env.pool)
	if user, err := NewLocalProvider(users, NewSessionRepository(env.pool)).Authenticate(ctx,
		Credentials{Username: owner.Username, Password: "correct horse battery"}); err != nil || user.ID != owner.ID {
		t.Fatalf("password sign-in after linking = %+v, %v; want the account", user, err)
	}
}

// TestNetworkRefusalBlocksLocalPasswordDB: linking a network identity keeps
// the account's local password and its open sessions. The scheduled pass
// re-checks that identity even without a credential to bound; a refusal ends
// every session and API key of the account and refuses its password until
// the provider vouches for the person again. Break-glass accounts and a
// turned-off network provider are never blocked.
func TestNetworkRefusalBlocksLocalPasswordDB(t *testing.T) {
	env := newRecheckEnv(t, "network-password", "")
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := env.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	network := env.enableNetworkProvider(t, "network-password")
	owner := env.localAccount(t, "owner", models.RoleUser)
	session := models.AuthSession{ID: uuid.New().String(), UserID: owner.ID, DeviceName: "network-password-test",
		ExpiresAt: time.Now().Add(recheckRefreshExpiry)}
	if err := NewSessionRepository(env.pool).Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	link := func(user *models.User, label string) int64 {
		t.Helper()
		_, identityID, err := env.resolver.Resolve(ctx, ResolveInput{InstallationID: network, Network: true, LinkingUserID: user.ID,
			Identity: ExternalIdentity{Subject: "controlplane.tailscale.com|" + env.name(label), Username: user.Username}})
		if err != nil {
			t.Fatal(err)
		}
		return identityID
	}
	networkIdentityID := link(owner, "owner")
	if user, err := NewUserRepository(env.pool).GetByID(ctx, owner.ID); err != nil || !user.LocalPasswordLoginEnabled {
		t.Fatalf("after linking = %+v, %v; want local password sign-in on", user, err)
	}
	if row := env.sessionRow(t, session.ID); row.IdentityID != nil {
		t.Fatalf("linking moved the local session under the network identity %d", *row.IdentityID)
	}
	key, err := NewAPIKeyRepository(env.pool).Create(ctx, owner.ID, "network-password", nil)
	if err != nil {
		t.Fatal(err)
	}
	signIn := func(user *models.User) error {
		_, _, err := env.svc.Login(ctx, user.Username, "correct horse battery", "network-password-test", "")
		return err
	}
	if err := signIn(owner); err != nil {
		t.Fatalf("password sign-in after linking = %v", err)
	}
	recheck := func(status pluginv1.CheckAccountStatus) {
		t.Helper()
		env.checker.respond = answer(status, "")
		exec(`UPDATE plugin_auth_identities SET last_checked_at = NOW() - INTERVAL '13 hours' WHERE id = $1`, networkIdentityID)
		if _, err := env.recheck.RecheckIdleIdentities(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// Removed from the overlay: signed out everywhere, password refused.
	recheck(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND)
	if env.sessionRow(t, session.ID).RevokedAt == nil {
		t.Fatal("the password session survived the network provider's refusal")
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(ctx, key.Key); err == nil {
		t.Fatal("the API key survived the network provider's refusal")
	}
	if err := signIn(owner); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("password sign-in after the refusal = %v, want ErrNotPermitted", err)
	}
	if _, err := NewLocalProvider(NewUserRepository(env.pool), NewSessionRepository(env.pool)).Authenticate(ctx,
		Credentials{Username: owner.Username, Password: "wrong password"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password after the refusal = %v, want ErrInvalidCredentials", err)
	}

	// An answer that decides nothing keeps the refusal on record.
	for _, status := range []pluginv1.CheckAccountStatus{pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNAVAILABLE,
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED} {
		recheck(status)
		if err := signIn(owner); !errors.Is(err, ErrNotPermitted) {
			t.Fatalf("password sign-in after a %v answer = %v, want ErrNotPermitted", status, err)
		}
	}

	// An identity that answered unsupported, with nothing to bound, is still
	// asked again, so a plugin that gains checks can refuse the person.
	exec(`UPDATE plugin_auth_identities SET last_check_status = $2, last_checked_at = NOW() - INTERVAL '13 hours' WHERE id = $1`,
		networkIdentityID, CheckStatusUnsupported)
	if due, err := env.recheck.IdleRecheckDue(ctx); err != nil || !due {
		t.Fatalf("re-check due after an unsupported answer = %v, %v; want due", due, err)
	}
	exec(`UPDATE plugin_auth_identities SET last_check_status = $2 WHERE id = $1`, networkIdentityID, CheckStatusNotFound)

	// So does one that arrives while a refusal waits to be applied.
	exec(`UPDATE plugin_auth_identities SET last_check_status = $2, pending_refusal = $3 WHERE id = $1`,
		networkIdentityID, CheckStatusActive, CheckStatusNotFound)
	if err := recordIdentityCheck(ctx, env.pool, networkIdentityID, CheckStatusUnavailable, nil); err != nil {
		t.Fatal(err)
	}
	if err := signIn(owner); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("password sign-in after an unavailable answer to a pending refusal = %v, want ErrNotPermitted", err)
	}
	exec(`UPDATE plugin_auth_identities SET last_check_status = $2, pending_refusal = '' WHERE id = $1`,
		networkIdentityID, CheckStatusNotFound)

	// A break-glass admin is never blocked.
	admin := env.localAccount(t, "glass", models.RoleAdmin)
	exec(`UPDATE users SET break_glass = true WHERE id = $1`, admin.ID)
	adminIdentity := link(admin, "glass")
	exec(`UPDATE plugin_auth_identities SET last_check_status = $2 WHERE id = $1`, adminIdentity, CheckStatusNotFound)
	if err := signIn(admin); err != nil {
		t.Fatalf("break-glass password sign-in during a network refusal = %v", err)
	}

	// Neither is anyone once the network provider is turned off.
	exec(`UPDATE plugin_auth_bindings SET enabled = false WHERE plugin_installation_id = $1`, network)
	if err := signIn(owner); err != nil {
		t.Fatalf("password sign-in with the network provider off = %v", err)
	}
	exec(`UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, network)

	// Added back: the next scheduled re-check lifts the block.
	recheck(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE)
	if err := signIn(owner); err != nil {
		t.Fatalf("password sign-in after the provider vouches again = %v", err)
	}
}

// TestNetworkLinkAtMixedInstallationTurnsPasswordOffDB: an installation that
// also has an OIDC or LDAP binding is that provider for linking, even while
// that binding is off, so linking there turns the password off.
func TestNetworkLinkAtMixedInstallationTurnsPasswordOffDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	if _, err := env.pool.Exec(ctx, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'auth_provider.v1', 'tailscale', '{"auth_modes":["network"]}')`, env.installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'tailscale', true)`,
		env.installationID); err != nil {
		t.Fatal(err)
	}
	owner := env.localAccount(t, "owner", models.RoleUser)
	if _, err := env.resolve(t, env.identity("owner"), false, owner.ID); err != nil {
		t.Fatal(err)
	}
	if user, err := NewUserRepository(env.pool).GetByID(ctx, owner.ID); err != nil || user.LocalPasswordLoginEnabled {
		t.Fatalf("after linking at a mixed installation = %+v, %v; want local password sign-in off", user, err)
	}
}

// enableNetworkProvider enables the env's primary binding and adds an
// enabled network identity provider installation beside it.
func (e *recheckEnv) enableNetworkProvider(t *testing.T, label string) int {
	t.Helper()
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, e.installationID)
	var network int
	if err := e.pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/network-sign-in-test', true, 'manual', 'plugin') RETURNING id`,
		label+"-"+e.suffix).Scan(&network); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM plugin_installations WHERE id = $1`, network)
	})
	exec(`INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'auth_provider.v1', 'tailscale', '{"auth_modes":["network"]}')`, network)
	exec(`INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'tailscale', true)`, network)
	return network
}

// TestNetworkIdentityDefersToPrimaryProviderDB: an account that also signs in
// through the primary provider takes its role from that provider alone, and
// a refusal from the network provider ends only the sessions the network
// identity opened, not what the primary provider still vouches for.
func TestNetworkIdentityDefersToPrimaryProviderDB(t *testing.T) {
	env := newRecheckEnv(t, "both-providers", "")
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := env.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	network := env.enableNetworkProvider(t, "network-sign-in")

	tailnetAdmin := ExternalIdentity{Subject: "controlplane.tailscale.com|" + env.suffix, Username: env.name("tv-owner"),
		ManagedRole: pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN}
	resolve := func(linking int) *models.User {
		t.Helper()
		user, _, err := env.resolver.Resolve(ctx, ResolveInput{InstallationID: network, Network: true, Identity: tailnetAdmin, LinkingUserID: linking})
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	if linked := resolve(env.user.ID); linked.ID != env.user.ID || linked.Role != models.RoleUser {
		t.Fatalf("linking = %+v, want the account with the primary provider's role", linked)
	}
	if signedIn := resolve(0); signedIn.ID != env.user.ID || signedIn.Role != models.RoleUser {
		t.Fatalf("network sign-in = %+v, want the account with the primary provider's role", signedIn)
	}
	networkIdentity, err := identityBySubject(ctx, env.pool, network, tailnetAdmin.Subject)
	if err != nil {
		t.Fatal(err)
	}
	ageNetworkIdentity := func() {
		t.Helper()
		exec(`UPDATE plugin_auth_identities SET last_checked_at = NOW() - INTERVAL '13 hours' WHERE id = $1`, networkIdentity.ID)
	}

	// A re-check that answers admin leaves the role alone too.
	networkSession, networkRefresh := env.sessionWithChain(t, &networkIdentity.ID, time.Now())
	env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		return &pluginv1.CheckAccountResponse{Status: pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE,
			Account: &pluginv1.AuthenticateResponse{ManagedRole: pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN}}, nil
	}
	ageNetworkIdentity()
	pair, err := env.svc.Refresh(ctx, networkRefresh)
	if err != nil {
		t.Fatalf("refresh of the network session = %v", err)
	}
	if user, err := NewUserRepository(env.pool).GetByID(ctx, env.user.ID); err != nil || user.Role != models.RoleUser {
		t.Fatalf("after a network re-check = %+v, %v; want the user role", user, err)
	}

	// A refusal ends the network session only.
	primarySession, _ := env.session(t, true)
	key, err := NewAPIKeyRepository(env.pool).Create(ctx, env.user.ID, "both-providers", nil)
	if err != nil {
		t.Fatal(err)
	}
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND, "")
	ageNetworkIdentity()
	if _, err := env.svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refresh after the refusal = %v, want ErrSessionRevoked", err)
	}
	if env.sessionRow(t, networkSession).RevokedAt == nil {
		t.Fatal("the network session survived the refusal")
	}
	if env.sessionRow(t, primarySession).RevokedAt != nil {
		t.Fatal("the network refusal revoked the primary provider's session")
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(ctx, key.Key); err != nil {
		t.Fatalf("the network refusal deleted the account's API key: %v", err)
	}

	// A network provider that cannot re-check removes nothing either, even
	// past the absolute age: the primary provider bounds the credentials.
	t.Cleanup(saveSettings(t, env.pool, config.AuthRefreshTokenExpirySettingKey))
	exec(`DELETE FROM server_settings WHERE key = $1`, config.AuthRefreshTokenExpirySettingKey)
	exec(`UPDATE plugin_auth_identities SET last_authenticated_at = NOW() - INTERVAL '31 days' WHERE id = $1`, networkIdentity.ID)
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, "")
	ageNetworkIdentity()
	if counts, err := env.recheck.RecheckIdleIdentities(ctx); err != nil || counts[CheckStatusUnsupported] != 1 {
		t.Fatalf("scheduled pass = %v, %v; want the network identity answered unsupported", counts, err)
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(ctx, key.Key); err != nil {
		t.Fatalf("a network provider that cannot re-check deleted the account's API key: %v", err)
	}

	// While the primary provider refuses the account, the overlay cannot sign
	// it back in; once it vouches again, it can.
	overlay := networkSignInServiceAt(env.pool, env.resolver, network, &peerPlugin{peers: map[string]*pluginv1.AuthenticateResponse{
		"100.64.0.7": {ExternalSubject: tailnetAdmin.Subject, Username: tailnetAdmin.Username, ManagedRole: tailnetAdmin.ManagedRole},
	}}, false)
	signIn := func() error {
		_, err := overlay.NetworkSignIn(overlayContext(ctx, network, "100.64.0.7"), NetworkSignInInput{InstallationID: network})
		return err
	}
	exec(`UPDATE plugin_auth_identities SET last_check_status = $2 WHERE id = $1`, env.identityID, CheckStatusNotFound)
	if err := signIn(); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("network sign-in after the primary provider's refusal = %v, want ErrNotPermitted", err)
	}
	// A refusal committed after account resolution still stops the session.
	if _, err := overlay.OpenIdentitySession(ctx, nil, IdentitySession{UserID: env.user.ID, IdentityID: networkIdentity.ID, Network: true}); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("network session opened after the primary provider's refusal = %v, want ErrNotPermitted", err)
	}
	// So does a refusal whose revocation rolled back: it waits in
	// pending_refusal while last_check_status keeps the previous answer.
	exec(`UPDATE plugin_auth_identities SET last_check_status = $2, pending_refusal = $3 WHERE id = $1`,
		env.identityID, CheckStatusActive, CheckStatusNotFound)
	if err := signIn(); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("network sign-in during the primary provider's pending refusal = %v, want ErrNotPermitted", err)
	}
	if _, err := overlay.OpenIdentitySession(ctx, nil, IdentitySession{UserID: env.user.ID, IdentityID: networkIdentity.ID, Network: true}); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("network session opened during the primary provider's pending refusal = %v, want ErrNotPermitted", err)
	}
	exec(`UPDATE plugin_auth_identities SET pending_refusal = '' WHERE id = $1`, env.identityID)
	if err := signIn(); err != nil {
		t.Fatalf("network sign-in once the primary provider vouches again = %v", err)
	}
	if user, err := NewUserRepository(env.pool).GetByID(ctx, env.user.ID); err != nil || user.Role != models.RoleUser {
		t.Fatalf("after a network sign-in = %+v, %v; want the primary provider's role", user, err)
	}
}

// TestScheduledRecheckCoversNetworkSessionsDB: an account whose only
// credential is a network session still has its primary identity re-checked
// by the scheduled pass, so the primary provider's removal ends the session.
func TestScheduledRecheckCoversNetworkSessionsDB(t *testing.T) {
	env := newRecheckEnv(t, "network-only-sessions", "")
	ctx := t.Context()
	network := env.enableNetworkProvider(t, "network-only-sessions")
	_, networkIdentityID, err := env.resolver.Resolve(ctx, ResolveInput{InstallationID: network, Network: true, LinkingUserID: env.user.ID,
		Identity: ExternalIdentity{Subject: "controlplane.tailscale.com|" + env.suffix, Username: env.name("tv-owner")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_identities SET last_checked_at = NOW(), last_check_status = $2 WHERE id = $1`,
		networkIdentityID, CheckStatusActive); err != nil {
		t.Fatal(err)
	}
	networkSession, _ := env.sessionWithChain(t, &networkIdentityID, time.Now())
	env.makeDue(t)

	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND, "")
	if _, err := env.recheck.RecheckIdleIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	if status := env.identityState(t).LastCheckStatus; status != CheckStatusNotFound {
		t.Fatalf("primary identity status = %q, want %q: the scheduled pass skipped it", status, CheckStatusNotFound)
	}
	if env.sessionRow(t, networkSession).RevokedAt == nil {
		t.Fatal("the network session survived the primary provider's refusal")
	}
}

// TestNetworkSignInNeverMatchesByEmailDB: with email auto-match on, a
// network identity whose provider claims a verified email still does not
// link to the account holding that email.
func TestNetworkSignInNeverMatchesByEmailDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	env.setSetting(t, config.AuthEmailAutoMatchSettingKey, "true")
	owner := env.localAccount(t, "owner", models.RoleUser)
	verified := peerIdentity(env, "owner")
	yes := true
	verified.EmailVerified = &yes
	plugin := &peerPlugin{peers: map[string]*pluginv1.AuthenticateResponse{"100.64.0.7": verified}}
	svc := networkSignInService(t, env, plugin, true)
	if _, err := svc.NetworkSignIn(overlayContext(ctx, env.installationID, "100.64.0.7"), NetworkSignInInput{InstallationID: env.installationID}); !errors.Is(err, ErrEmailInUse) {
		t.Fatalf("verified email of a local account = %v, want ErrEmailInUse", err)
	}
	if env.activeSessions(t, owner.ID) != 0 {
		t.Fatal("a network sign-in matched the local account by email")
	}
}
