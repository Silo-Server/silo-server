package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/models"
)

// networkSignInService wires a Service to a network identity plugin at the
// env's installation.
func networkSignInService(t *testing.T, env *externalSignInEnv, plugin *peerPlugin, autoProvision bool) *Service {
	t.Helper()
	users := NewUserRepository(env.pool)
	sessions := NewSessionRepository(env.pool)
	svc := NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-jwt-secret-synthetic-jwt-secret", time.Minute, time.Hour), sessions, users, nil, nil, nil)
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: env.installationID, CapabilityID: "tailscale", AutoProvision: autoProvision},
		sessions, env.resolver, func(context.Context) (pluginAuthClient, error) { return plugin, nil })
	svc.SetPluginProviderSource(networkProviderSource(env.installationID, provider))
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
// peer signs in to that account.
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
}
