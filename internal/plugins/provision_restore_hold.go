package plugins

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ProvisionRestoreHoldKey is a machine-managed setting written by full restore.
const ProvisionRestoreHoldKey = "plugins.provision_restore_hold"

// ProvisionRestoreHold returns the current restore hold ID, or an empty ID
// when provisioning is allowed. The writer must check it in the transaction
// that commits the declaration.
func ProvisionRestoreHold(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, ProvisionRestoreHoldKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

var errProvisionRestoreHeld = errors.New("plugin provisioning restore hold")

// Full restore adds the hold before activating the restored database. A
// release admission reads it from the transaction that will write the release,
// including when the service pool reconnects after the restore.
func checkPluginRestoreHold(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	hold, err := ProvisionRestoreHold(ctx, q)
	if err != nil {
		return err
	}
	if hold != "" {
		return errProvisionRestoreHeld
	}
	return nil
}

func admitPluginInstallationChange(ctx context.Context, tx pgx.Tx, automatic bool) error {
	if !automatic {
		return nil
	}
	return checkPluginRestoreHold(ctx, tx)
}

// ErrProvisionRestoreHoldChanged means there is no hold matching the supplied ID.
var ErrProvisionRestoreHoldChanged = errors.New("no plugin provisioning restore hold matches that ID")

// AcknowledgeProvisionRestoreHold clears only the hold an operator reviewed.
func AcknowledgeProvisionRestoreHold(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id string) error {
	if id == "" {
		return ErrProvisionRestoreHoldChanged
	}
	tag, err := q.Exec(ctx, `DELETE FROM server_settings WHERE key = $1 AND value = $2`, ProvisionRestoreHoldKey, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrProvisionRestoreHoldChanged
	}
	return nil
}
