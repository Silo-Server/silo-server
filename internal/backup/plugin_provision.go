package backup

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

const tablePluginProvisionStatus = "plugin_provision_status"

func readPluginCredentialState(ctx context.Context, tx pgx.Tx, state *liveCredentials) error {
	if err := tx.QueryRow(ctx, `SELECT coalesce(json_agg(t), '[]')::text FROM
		(SELECT id, plugin_id FROM plugin_installations) t`).Scan(&state.plugins); err != nil {
		return err
	}
	var err error
	state.pluginOwnership, err = tableExists(ctx, tx, tablePluginProvisionOwnership)
	if err != nil {
		return err
	}
	state.pluginCallbackMode, err = columnExists(ctx, tx, tablePluginAuthBindings, "callback_mode")
	return err
}

// preparePluginCredentialCarry makes both installation identities available to
// the credential carry. A backup from before provisioning or stable callbacks
// also needs room for those live values. Startup still records the Goose
// migrations after the swap.
func preparePluginCredentialCarry(ctx context.Context, tx pgx.Tx, state liveCredentials) error {
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE carried_plugins (id bigint PRIMARY KEY) ON COMMIT DROP`); err != nil {
		return err
	}
	hasPlugins, err := tableExists(ctx, tx, "plugin_installations")
	if err != nil || !hasPlugins {
		return err
	}
	if state.plugins != "[]" {
		if _, err := tx.Exec(ctx, `INSERT INTO carried_plugins (id)
			SELECT i.id FROM plugin_installations i
			JOIN json_to_recordset($1::json) AS live(id bigint, plugin_id text)
				ON live.id = i.id AND live.plugin_id = i.plugin_id`, state.plugins); err != nil {
			return err
		}
	}
	if state.pluginOwnership {
		// Keep this definition in sync with the provisioning ownership migration.
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS plugin_provision_ownership (
			plugin_installation_id BIGINT PRIMARY KEY REFERENCES plugin_installations(id) ON DELETE CASCADE,
			version_pin TEXT NOT NULL DEFAULT '',
			config_fields JSONB NOT NULL DEFAULT '{}',
			binding_fields JSONB NOT NULL DEFAULT '{}'
		)`); err != nil {
			return err
		}
	}
	if state.pluginCallbackMode {
		present, err := tableExists(ctx, tx, tablePluginAuthBindings)
		if err != nil || !present {
			return err
		}
		// Keep this definition in sync with the callback mode migration.
		if _, err := tx.Exec(ctx, `ALTER TABLE plugin_auth_bindings
			ADD COLUMN IF NOT EXISTS callback_mode TEXT NOT NULL DEFAULT 'installation'
			CHECK (callback_mode IN ('installation', 'stable'))`); err != nil {
			return err
		}
	}
	return nil
}

// FinishPluginProvisionRestore clears transient status and replaces any
// restored hold with a destination decision. A new ID invalidates an
// acknowledgement prepared before this restore.
func (l PGLoader) FinishPluginProvisionRestore(ctx context.Context, dbURL, liveURL string, foreign, restricted bool) (string, error) {
	restricted, err := liveProvisionRestoreRestricted(ctx, liveURL, foreign, restricted)
	if err != nil {
		return "", err
	}
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var id string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		present, err := tableExists(ctx, tx, tablePluginProvisionStatus)
		if err != nil {
			return err
		}
		if present {
			if _, err := tx.Exec(ctx, `DELETE FROM plugin_provision_status`); err != nil {
				return err
			}
		}
		if foreign && !restricted {
			// An earlier restore may have withheld provisioning without
			// leaving any plugin rows. Its hold ID is never carried.
			prior, err := plugins.ProvisionRestoreHold(ctx, tx)
			if err != nil {
				return err
			}
			restricted = prior != ""
		}
		if !restricted {
			_, err := tx.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, plugins.ProvisionRestoreHoldKey)
			return err
		}
		id = rand.Text()
		_, err = tx.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, plugins.ProvisionRestoreHoldKey, id)
		return err
	})
	return id, err
}

func liveProvisionRestoreRestricted(ctx context.Context, liveURL string, foreign, restricted bool) (bool, error) {
	if liveURL == "" {
		return restricted, nil
	}
	live, err := pgx.Connect(ctx, liveURL)
	if err != nil {
		return false, err
	}
	defer func() { _ = live.Close(context.WithoutCancel(ctx)) }()
	id, err := plugins.ProvisionRestoreHold(ctx, live)
	if err != nil {
		return false, err
	}
	restricted = restricted || id != ""
	if foreign && !restricted {
		return hasRestorablePlugins(ctx, live)
	}
	return restricted, nil
}

func hasRestorablePlugins(ctx context.Context, q queryer) (bool, error) {
	present, err := tableExists(ctx, q, "plugin_installations")
	if err != nil || !present {
		return false, err
	}
	hasKind, err := columnExists(ctx, q, "plugin_installations", "kind")
	if err != nil {
		return false, err
	}
	filter := ""
	if hasKind {
		filter = ` WHERE kind IS DISTINCT FROM 'builtin'`
	}
	var hasPlugins bool
	err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plugin_installations`+filter+`)`).Scan(&hasPlugins)
	return hasPlugins, err
}

func finishPluginProvisionRestore(ctx context.Context, loader DatabaseLoader, dbURL, liveURL string, foreign, restricted bool, out io.Writer) error {
	id, err := loader.FinishPluginProvisionRestore(ctx, dbURL, liveURL, foreign, restricted)
	if err != nil {
		return fmt.Errorf("setting the plugin provisioning restore hold: %w", err)
	}
	if id != "" {
		_, _ = fmt.Fprintf(out, "Plugin provisioning is held (%s). Review the declaration for plugin releases, external sign-in, and automatic account creation, then run silo plugins acknowledge-restore %s.\n", id, id)
	}
	return nil
}
