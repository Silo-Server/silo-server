package plugins

import (
	"context"

	"github.com/jackc/pgx/v5"
)

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
