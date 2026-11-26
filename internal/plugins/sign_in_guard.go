package plugins

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// ErrSignInProviderRequired is the guard's refusal: the change would leave
// local password sign-in off with no OIDC or LDAP provider people can use,
// so nobody but the break-glass admins could sign in.
var ErrSignInProviderRequired = errors.New("a sign-in provider people can use is required while local password sign-in is off")

// SignInProviderWarning is what a provisioning apply logs and reports on the
// declared sign-in plugins when it leaves that state: the deployment's
// declaration is applied, not refused (provisionSignInWarning).
//
// It names the ways back in the order to try them: the declaration itself, a
// break-glass administrator (who keeps a password while it is off), and the
// recovery command, which needs only a shell with the server's DATABASE_URL.
const SignInProviderWarning = "Password sign-in is off and no sign-in provider can sign people in, so only break-glass administrators can sign in. " +
	"To restore sign-in for everyone else: put the sign-in provider back in SILO_CONFIG or SILO_PLUGINS and restart the server; " +
	"or have a break-glass administrator sign in with their password at /login?local=1 and turn on Allow password sign-in under Settings > Sign-in; " +
	"or run `silo auth local-login enable` on a server with its DATABASE_URL."

// SignInProviderGuard refuses a plugin write that would leave the server with
// password sign-in off and no sign-in provider people can use. It runs in
// the write's transaction after the change, so it judges the state the
// write would commit; removing is an installation it treats as gone (0 for
// none), for a check made before a change. It takes the server-settings
// lock after the write's own locks, the order every holder of both uses.
//
// The auth package owns the rule (which providers can sign people in) and
// registers it; plugins cannot import auth.
type SignInProviderGuard func(ctx context.Context, tx pgx.Tx, removing int) error

var signInProviderGuard SignInProviderGuard

// RegisterSignInProviderGuard installs the guard every auth binding and
// installation write runs.
func RegisterSignInProviderGuard(guard SignInProviderGuard) {
	signInProviderGuard = guard
}

func checkSignInProvider(ctx context.Context, tx pgx.Tx, removing int) error {
	if signInProviderGuard == nil {
		return nil
	}
	return signInProviderGuard(ctx, tx, removing)
}

// CheckSignInProviderRemoval answers whether disabling or uninstalling
// installation id would be refused by the guard, before anything changes:
// the plugin process is stopped before those writes, so a refusal is made
// here first. The write's own check still decides a concurrent change.
func (s *InstallationStore) CheckSignInProviderRemoval(ctx context.Context, id int) error {
	if s.pool == nil {
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return checkSignInProvider(ctx, tx, id)
	})
}

// provisionSignInWarning applies the guard to a provisioning apply without
// refusing it. Provisioning applies the operator's declared configuration at
// startup, with nobody at a screen; refusing it would leave the server unable
// to converge on its own configuration, so a declaration that leaves no
// provider people can sign in with is applied, logged as a warning, and
// reported in the provisioning status of each declared plugin with a sign-in
// binding. A full restore applies stated intent the same way.
func provisionSignInWarning(ctx context.Context, tx pgx.Tx, statuses []ProvisionStatus, bindingIDs map[string]int) error {
	err := checkSignInProvider(ctx, tx, 0)
	if !errors.Is(err, ErrSignInProviderRequired) {
		return err
	}
	slog.WarnContext(ctx, SignInProviderWarning, "component", "plugins")
	for i := range statuses {
		if _, declared := bindingIDs[statuses[i].PluginID]; declared && statuses[i].Detail == "" {
			statuses[i].Detail = SignInProviderWarning
		}
	}
	return nil
}
