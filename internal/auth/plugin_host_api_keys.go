package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// PluginHostAPIKeys issues the scoped API keys plugins call the host API
// with. It is the plugins.HostAPIKeyIssuer of plugins.PermissionService.
type PluginHostAPIKeys struct {
	Keys *APIKeyRepository
}

var _ plugins.HostAPIKeyIssuer = PluginHostAPIKeys{}

// KnownScopes is the API key scope catalog.
func (PluginHostAPIKeys) KnownScopes() []string { return ValidAPIKeyScopes() }

// LockAccounts share-locks, in tx, the accounts IssueTx and RevokeTx lock
// for actorID: the actor and the audit row's accounts, in id order.
func (PluginHostAPIKeys) LockAccounts(ctx context.Context, tx pgx.Tx, actorID int) error {
	_, _, err := lockAPIKeyAccounts(ctx, tx, actorID, actorID)
	return err
}

// IssueTx mints, in tx, a key with exactly scopes on actorID's own account.
// Like any key, it acts as that account: a key whose owner loses the admin
// role stops reaching admin routes, and one whose owner is deleted is
// deleted with it. The caller runs committed once tx commits.
func (k PluginHostAPIKeys) IssueTx(ctx context.Context, tx pgx.Tx, actorID int, label string, scopes []string) (int64, string, func(), error) {
	normalized, err := NormalizeAPIKeyScopes(scopes)
	if err != nil {
		return 0, "", nil, err
	}
	if len(normalized) == 0 {
		// An unscoped key is full access as its owner; a plugin never gets one.
		return 0, "", nil, errors.New("a plugin host API key needs at least one scope")
	}
	key, committed, err := k.Keys.CreateAsActorTx(ctx, tx, actorID, actorID, label, normalized)
	if err != nil {
		return 0, "", nil, err
	}
	return key.ID, key.Key, committed, nil
}

// RevokeTx deletes the key in tx. A key already deleted is not an error. The
// caller runs committed once tx commits.
func (k PluginHostAPIKeys) RevokeTx(ctx context.Context, tx pgx.Tx, id int64) (func(), error) {
	committed, err := k.Keys.DeleteByAdminTx(ctx, tx, id)
	if errors.Is(err, ErrAPIKeyNotFound) {
		return func() {}, nil
	}
	return committed, err
}
