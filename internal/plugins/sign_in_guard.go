package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
)

// ErrSignInProviderRequired is the guard's refusal: the change would take
// away the last OIDC or LDAP provider people can use while local password
// sign-in is off, so nobody but the break-glass admins could sign in.
var ErrSignInProviderRequired = errors.New("a sign-in provider people can use is required while local password sign-in is off")

// SignInProviderWarning is what a provisioning apply logs, and the
// provisioning status of a declared sign-in plugin shows, while password
// sign-in is off and no provider can sign people in: the deployment's
// declaration is applied, not refused (logProvisionSignIn).
//
// It names the ways back in the order to try them: the declaration itself, a
// break-glass administrator (who keeps a password while it is off), and the
// recovery command, which needs only a shell with the server's DATABASE_URL.
const SignInProviderWarning = signInWarningLead + ", so only break-glass administrators can sign in. " +
	"To restore sign-in for everyone else: put the sign-in provider back in SILO_CONFIG or SILO_PLUGINS and restart the server; " +
	"or have a break-glass administrator sign in with their password at /login?local=1 and turn on Allow password sign-in under Settings > Sign-in; " +
	"or run `silo auth local-login enable` on a server with its DATABASE_URL."

// signInWarningLead opens every wording of the warning, including those
// earlier builds of this check stored in the provisioning status.
const signInWarningLead = "Password sign-in is off and no sign-in provider can sign people in"

// SignInQuerier is what the sign-in check reads through: a pool or a
// transaction.
type SignInQuerier = provisionConfigQuery

// SignInCheck answers whether everyone can still sign in: local password
// sign-in is on, or an OIDC or LDAP provider people can use is on.
//
// The auth package owns the rule (which providers can sign people in) and
// registers it; plugins cannot import auth.
type SignInCheck func(ctx context.Context, db SignInQuerier) (bool, error)

var signInCheck SignInCheck

// RegisterSignInCheck installs the check the auth binding and installation
// writes, and the provisioning status, use.
func RegisterSignInCheck(check SignInCheck) {
	signInCheck = check
}

func signInWorks(ctx context.Context, db SignInQuerier) (bool, error) {
	if signInCheck == nil {
		return true, nil
	}
	return signInCheck(ctx, db)
}

// lockSignInSettings takes the server-settings lock, which the settings save
// holds while it checks the other direction (turning passwords off). Plugin
// writes take it after their own bindings and installation locks, and the
// settings save takes neither of those, so the two serialize without
// deadlock.
func lockSignInSettings(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, config.ServerSettingsMutationLock); err != nil {
		return fmt.Errorf("acquiring settings lock: %w", err)
	}
	return nil
}

// signInGuard refuses a plugin write that takes away the last provider people
// can sign in with while password sign-in is off. Begin it in the write's
// transaction before the change and finish it after: a write is refused only
// when everyone could sign in before it and cannot after it. A server already
// without a way in (a provisioned declaration, a restore, a switch-off from
// before this check) can still manage its plugins, and any write that makes
// a provider usable is allowed.
type signInGuard struct{ workedBefore bool }

func beginSignInGuard(ctx context.Context, tx pgx.Tx) (signInGuard, error) {
	if err := lockSignInSettings(ctx, tx); err != nil {
		return signInGuard{}, err
	}
	worked, err := signInWorks(ctx, tx)
	return signInGuard{workedBefore: worked}, err
}

func (g signInGuard) finish(ctx context.Context, tx pgx.Tx) error {
	if !g.workedBefore {
		return nil
	}
	works, err := signInWorks(ctx, tx)
	if err != nil {
		return err
	}
	if !works {
		return ErrSignInProviderRequired
	}
	return nil
}

// logProvisionSignIn applies the check to a provisioning apply without
// refusing it. Provisioning applies the operator's declared configuration at
// startup, with nobody at a screen; refusing it would leave the server unable
// to converge on its own configuration, so a declaration that leaves no
// provider people can sign in with is applied and logged as a warning. A full
// restore applies stated intent the same way. The provisioning status says so
// for as long as it is true (withSignInWarning).
func logProvisionSignIn(ctx context.Context, tx pgx.Tx) error {
	works, err := signInWorks(ctx, tx)
	if err != nil || works {
		return err
	}
	slog.WarnContext(ctx, SignInProviderWarning, "component", "plugins")
	return nil
}

// withSignInWarning adds SignInProviderWarning, when it is true at read time,
// to the status of each provisioned plugin with an OIDC or LDAP binding that
// has no other detail. A plugin with only a network identity binding can
// never be the provider the warning asks for, so it gets none. Worked out on every read, it ends as soon as password
// sign-in is turned back on, by a break-glass admin or the recovery command,
// or a provider becomes usable, without waiting for the next apply.
func withSignInWarning(ctx context.Context, db SignInQuerier, statuses []ProvisionStatus) ([]ProvisionStatus, error) {
	if len(statuses) == 0 {
		return statuses, nil
	}
	// The read owns the warning: a stored one, written by an earlier build
	// that kept it in the status, is dropped and worked out again.
	for i := range statuses {
		if strings.HasPrefix(statuses[i].Detail, signInWarningLead) {
			statuses[i].Detail = ""
		}
	}
	works, err := signInWorks(ctx, db)
	if err != nil || works {
		return statuses, err
	}
	rows, err := db.Query(ctx, `SELECT DISTINCT i.plugin_id FROM plugin_installations i
		JOIN plugin_auth_bindings b ON b.plugin_installation_id = i.id
		WHERE NOT `+AuthBindingIsNetworkSQL("b.plugin_installation_id", "b.capability_id"))
	if err != nil {
		return statuses, err
	}
	pluginIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return statuses, err
	}
	signIn := make(map[string]bool, len(pluginIDs))
	for _, id := range pluginIDs {
		signIn[id] = true
	}
	for i := range statuses {
		if signIn[statuses[i].PluginID] && statuses[i].Detail == "" {
			statuses[i].Detail = SignInProviderWarning
		}
	}
	return statuses, nil
}
