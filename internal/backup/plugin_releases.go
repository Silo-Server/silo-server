package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pluginReleaseColumns are what the launcher reads of an installation
// besides its id, plugin, and switch: which release it runs and where. The
// refused-update record and the release signature check go with them, since
// both describe the release the row runs.
var pluginReleaseColumns = []string{"repository_id", "version", "install_path", "update_policy", "available_version", "kind", "runtime_generation",
	"update_failed_version", "update_failure_reason", "update_failed_at",
	"signature_state", "signature_source_repo", "signature_verified_at"}

// CarryPluginReleases gives each matching installation its live release, archive,
// and capabilities. Other restored installations stay off. It reports both why
// a restored release cannot run and any live release omitted from the restore.
func (l PGLoader) CarryPluginReleases(ctx context.Context, dbURL, liveURL string) ([]string, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var live *pgx.Conn
	var running map[int64]pluginReleaseState
	if liveURL != "" {
		if live, err = pgx.Connect(ctx, liveURL); err != nil {
			return nil, fmt.Errorf("reading the live server's plugins: %w", err)
		}
		defer func() { _ = live.Close(context.WithoutCancel(ctx)) }()
		if running, err = pluginReleaseStates(ctx, live); err != nil {
			return nil, err
		}
	}
	var off []string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		restored, err := pluginReleaseStates(ctx, tx)
		if err != nil {
			return err
		}
		for id, release := range restored {
			here, found := running[id]
			if found && here.pluginID == release.pluginID {
				delete(running, id)
				if err := copyPluginRelease(ctx, live, tx, id); err != nil {
					return fmt.Errorf("plugin installation %d: %w", id, err)
				}
				if !release.enabled && here.enabled {
					off = append(off, fmt.Sprintf("%s (installation %d) stayed off", release.pluginID, id))
				}
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE plugin_installations SET enabled = false WHERE id = $1`, id); err != nil {
				return err
			}
			off = append(off, fmt.Sprintf("%s (installation %d) turned off", release.pluginID, id))
		}
		for id, release := range running {
			off = append(off, fmt.Sprintf("%s (installation %d) absent from the restored database", release.pluginID, id))
		}
		return nil
	})
	slices.Sort(off)
	return off, err
}

type pluginReleaseState struct {
	pluginID string
	enabled  bool
}

func pluginReleaseStates(ctx context.Context, q queryer) (map[int64]pluginReleaseState, error) {
	rows, err := q.Query(ctx, `SELECT id, plugin_id, enabled FROM plugin_installations WHERE kind IS DISTINCT FROM 'builtin'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	releases := make(map[int64]pluginReleaseState)
	for rows.Next() {
		var id int64
		var release pluginReleaseState
		if err := rows.Scan(&id, &release.pluginID, &release.enabled); err != nil {
			return nil, err
		}
		releases[id] = release
	}
	return releases, rows.Err()
}

// copyPluginRelease writes the live installation id's release into tx:
// the release columns, the stored archive, and the capabilities.
func copyPluginRelease(ctx context.Context, live *pgx.Conn, tx pgx.Tx, id int64) error {
	set := make([]string, len(pluginReleaseColumns))
	args := make([]any, 0, len(pluginReleaseColumns)+1)
	args = append(args, id)
	row := live.QueryRow(ctx, `SELECT `+strings.Join(pluginReleaseColumns, ", ")+` FROM plugin_installations WHERE id = $1`, id)
	values := make([]any, len(pluginReleaseColumns))
	ptrs := make([]any, len(values))
	for i := range values {
		ptrs[i] = &values[i]
	}
	if err := row.Scan(ptrs...); err != nil {
		return err
	}
	for i, col := range pluginReleaseColumns {
		set[i] = fmt.Sprintf("%s = $%d", col, i+2)
		args = append(args, values[i])
	}
	// Its catalog is carried with the live catalogs (plugin_repositories);
	// one that is not there is dropped from the reference rather than left
	// to fail the restore on its foreign key.
	if repo := values[0]; repo != nil {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plugin_repositories WHERE id = $1)`, repo).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			args[1] = nil
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE plugin_installations SET `+strings.Join(set, ", ")+` WHERE id = $1`, args...); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_archives WHERE plugin_installation_id = $1`, id); err != nil {
		return err
	}
	var manifest, archive []byte
	var checksum string
	err := live.QueryRow(ctx, `SELECT manifest_json, checksum, archive_bytes FROM plugin_archives WHERE plugin_installation_id = $1`, id).Scan(&manifest, &checksum, &archive)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return err
	default:
		if _, err := tx.Exec(ctx, `INSERT INTO plugin_archives (plugin_installation_id, manifest_json, checksum, archive_bytes) VALUES ($1, $2, $3, $4)`,
			id, manifest, checksum, archive); err != nil {
			return err
		}
	}
	var capabilities string
	if err := live.QueryRow(ctx, `SELECT coalesce(json_agg(c), '[]')::text FROM plugin_capabilities c WHERE plugin_installation_id = $1`, id).Scan(&capabilities); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_capabilities WHERE plugin_installation_id = $1`, id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		SELECT r.plugin_installation_id, r.capability_type, r.capability_id, r.metadata FROM json_populate_recordset(NULL::plugin_capabilities, $1::json) r`, capabilities)
	return err
}

// carryPluginReleases reports releases the restored database cannot run.
func carryPluginReleases(ctx context.Context, loader DatabaseLoader, dbURL, liveURL string, out io.Writer) (bool, error) {
	off, err := loader.CarryPluginReleases(ctx, dbURL, liveURL)
	if err != nil {
		return false, fmt.Errorf("keeping the live server's plugin releases: %w", err)
	}
	if len(off) > 0 {
		_, _ = fmt.Fprintf(out, "Restricted restored plugin releases: %s. Review them after Silo starts.\n", strings.Join(off, ", "))
	}
	return len(off) > 0, nil
}

// DisablePlugins turns off every enabled plugin installation in dbURL,
// using only columns every schema with the table has. A schema without
// the table has no plugins to turn off.
func (l PGLoader) DisablePlugins(ctx context.Context, dbURL string) ([]string, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	off, err := collectStrings(ctx, conn, `UPDATE plugin_installations SET enabled = false WHERE enabled RETURNING plugin_id`)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "42P01" {
		return nil, nil
	}
	return off, err
}
