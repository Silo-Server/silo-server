package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// directoryPlugin is a credentials (LDAP) auth plugin: it answers
// Authenticate from a fixed table of directory users.
type directoryPlugin struct {
	users map[string]*pluginv1.AuthenticateResponse
	asked []string
}

func (p *directoryPlugin) Authenticate(_ context.Context, req *pluginv1.AuthenticateRequest) (*pluginv1.AuthenticateResponse, error) {
	p.asked = append(p.asked, req.GetUsername())
	if response, ok := p.users[req.GetUsername()+"\x00"+req.GetPassword()]; ok {
		return response, nil
	}
	return &pluginv1.AuthenticateResponse{Denial: pluginv1.AuthDenial_AUTH_DENIAL_INVALID_CREDENTIALS}, nil
}

func (p *directoryPlugin) InitAuthorize(context.Context, *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "credentials only")
}

func (p *directoryPlugin) ExchangeCode(context.Context, *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error) {
	return nil, status.Error(codes.Unimplemented, "credentials only")
}

// TestLinkCredentialsIdentityDB: a signed-in account links the directory
// identity it proves with the directory username and password, after
// re-entering its local password, under the rules of every other linking
// path; each refusal keeps its own error.
func TestLinkCredentialsIdentityDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	users := NewUserRepository(env.pool)
	sessions := NewSessionRepository(env.pool)
	svc := NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-jwt-secret-synthetic-jwt-secret", time.Minute, time.Hour), sessions, users, nil, nil, nil)
	directory := &directoryPlugin{users: map[string]*pluginv1.AuthenticateResponse{}}
	person := func(label string) *pluginv1.AuthenticateResponse {
		identity := env.identity(label)
		return &pluginv1.AuthenticateResponse{ExternalSubject: identity.Subject, Issuer: "ldaps://ldap.example.test",
			Username: identity.Username, Email: identity.Email, DisplayName: identity.DisplayName}
	}
	directory.users[env.name("alice")+"\x00dir-pass"] = person("alice")
	directory.users[env.name("glass")+"\x00dir-pass"] = person("glass")
	directory.users[env.name("glass-2")+"\x00dir-pass"] = person("glass-2")
	directory.users[env.name("locked")+"\x00dir-pass"] = &pluginv1.AuthenticateResponse{Denial: pluginv1.AuthDenial_AUTH_DENIAL_ACCOUNT_DISABLED}
	directory.users[env.name("outsider")+"\x00dir-pass"] = &pluginv1.AuthenticateResponse{Denial: pluginv1.AuthDenial_AUTH_DENIAL_NOT_PERMITTED}
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: env.installationID, CapabilityID: "ldap", DisplayName: "Directory"},
		sessions, env.resolver, func(context.Context) (pluginAuthClient, error) { return directory, nil })
	svc.SetPluginProviderSource(staticProviderSource{{
		Info:     LoginProviderInfo{ID: PluginProviderID(env.installationID, "ldap"), DisplayName: "Directory", Mode: ProviderModeCredentials, InstallationID: env.installationID},
		Provider: provider,
	}})
	link := func(user *models.User, password, username string) (*LinkedIdentity, error) {
		return svc.LinkCredentialsIdentity(ctx, CredentialsLinkInput{UserID: user.ID, InstallationID: env.installationID,
			Password: password, DirectoryUsername: username, DirectoryPassword: "dir-pass"})
	}

	member := env.localAccount(t, "member", models.RoleUser)
	if _, err := link(member, "wrong password", env.name("alice")); !errors.Is(err, ErrLinkTicketPassword) {
		t.Fatalf("wrong local password = %v", err)
	}
	if len(directory.asked) != 0 {
		t.Fatal("the directory was asked before the local password was confirmed")
	}
	if _, err := link(member, "correct horse battery", env.name("nobody")); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("refused directory credentials = %v", err)
	}
	if _, err := link(member, "correct horse battery", env.name("locked")); !errors.Is(err, ErrProviderAccountDisabled) {
		t.Fatalf("disabled directory account = %v", err)
	}
	if _, err := link(member, "correct horse battery", env.name("outsider")); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("directory group gate = %v", err)
	}
	if _, err := svc.LinkCredentialsIdentity(ctx, CredentialsLinkInput{UserID: member.ID, InstallationID: env.installationID + 1000,
		Password: "correct horse battery", DirectoryUsername: env.name("alice"), DirectoryPassword: "dir-pass"}); !errors.Is(err, ErrUnknownAuthInstallation) {
		t.Fatalf("unknown installation = %v", err)
	}

	linked, err := link(member, "correct horse battery", env.name("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if linked.UserID != member.ID || linked.InstallationID != env.installationID || linked.ExternalSubject != env.identity("alice").Subject {
		t.Fatalf("linked = %+v", linked)
	}
	after, err := users.GetByID(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LocalPasswordLoginEnabled {
		t.Fatal("linking kept local password sign-in on")
	}
	// Local password sign-in is now off: there is no password to confirm.
	if _, err := link(member, "correct horse battery", env.name("alice")); !errors.Is(err, ErrPasswordLoginDisabled) {
		t.Fatalf("second link without local password = %v", err)
	}

	// The same directory person cannot be linked to another account.
	other := env.localAccount(t, "other", models.RoleUser)
	if _, err := link(other, "correct horse battery", env.name("alice")); !errors.Is(err, ErrIdentityLinkedElsewhere) {
		t.Fatalf("identity of another account = %v", err)
	}

	// A break-glass admin keeps its local password, and holds one identity
	// per installation.
	admin := env.localAccount(t, "glass-admin", models.RoleAdmin)
	glass := true
	if err := users.Update(ctx, admin.ID, models.UpdateUserInput{BreakGlass: &glass}); err != nil {
		t.Fatal(err)
	}
	if _, err := link(admin, "correct horse battery", env.name("glass")); err != nil {
		t.Fatal(err)
	}
	if _, err := link(admin, "correct horse battery", env.name("glass-2")); !errors.Is(err, ErrAccountAlreadyLinked) {
		t.Fatalf("second identity at the installation = %v", err)
	}

	// A disabled Silo account links nothing.
	disabled := env.localAccount(t, "disabled", models.RoleUser)
	off := false
	if err := users.Update(ctx, disabled.ID, models.UpdateUserInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := link(disabled, "correct horse battery", env.name("glass-2")); !errors.Is(err, ErrUserDisabled) || errors.Is(err, ErrProviderAccountDisabled) {
		t.Fatalf("disabled account = %v", err)
	}
}

// TestExternalSignInCredentialsSessionLocksAccountDB: a directory sign-in
// opens its login session under the account lock, so an account disabled
// after account resolution committed is refused and keeps no live session.
func TestExternalSignInCredentialsSessionLocksAccountDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	users := NewUserRepository(env.pool)
	sessions := NewSessionRepository(env.pool)
	svc := NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-jwt-secret-synthetic-jwt-secret", time.Minute, time.Hour), sessions, users, nil, nil, nil)
	identity := env.identity("dana")
	answer := &pluginv1.AuthenticateResponse{ExternalSubject: identity.Subject, Issuer: "ldaps://ldap.example.test",
		Username: identity.Username, Email: identity.Email, DisplayName: identity.DisplayName,
		ManagedRole: pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_USER}
	directory := &directoryPlugin{users: map[string]*pluginv1.AuthenticateResponse{identity.Username + "\x00dir-pass": answer}}
	// A role the provider changes signs the account out everywhere, and the
	// resolver reports that after its transaction commits: an administrator's
	// disable can land there, before the sign-in opens its session.
	disableOnRevoke := false
	resolver := NewAccountResolver(env.pool, NewAccountProvisioner(users, pgstore.NewPostgresProvider(env.pool)), func(ctx context.Context, userID int) {
		if !disableOnRevoke {
			return
		}
		if _, err := env.pool.Exec(ctx, `UPDATE users SET enabled = false WHERE id = $1`, userID); err != nil {
			t.Error(err)
		}
	})
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: env.installationID, CapabilityID: "ldap", DisplayName: "Directory", AutoProvision: true},
		sessions, resolver, func(context.Context) (pluginAuthClient, error) { return directory, nil })
	providerID := PluginProviderID(env.installationID, "ldap")
	svc.SetPluginProviderSource(staticProviderSource{{
		Info:     LoginProviderInfo{ID: providerID, DisplayName: "Directory", Mode: ProviderModeCredentials, InstallationID: env.installationID},
		Provider: provider,
	}})
	liveSessions := func(userID int) int {
		t.Helper()
		var n int
		if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE user_id = $1 AND revoked_at IS NULL`, userID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	_, user, err := svc.LoginWithProvider(ctx, providerID, identity.Username, "dir-pass", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if n := liveSessions(user.ID); n != 1 {
		t.Fatalf("first sign-in left %d live sessions, want 1", n)
	}

	answer.ManagedRole = pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN
	disableOnRevoke = true
	if _, _, err := svc.LoginWithProvider(ctx, providerID, identity.Username, "dir-pass", "test", ""); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("sign-in of an account disabled after resolution = %v, want ErrUserDisabled", err)
	}
	if n := liveSessions(user.ID); n != 0 {
		t.Fatalf("a disabled account kept %d live sessions", n)
	}
}
