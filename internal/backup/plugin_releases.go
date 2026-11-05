package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pluginReleaseColumns are what the launcher reads of an installation
// besides its id, plugin, and switch: which release it runs and where.
var pluginReleaseColumns = []string{"repository_id", "version", "install_path", "update_policy", "available_version", "kind", "runtime_generation"}

// CarryPluginReleases gives each plugin installation of the restored
// database (dbURL) that the live server (liveURL) has, by id and plugin,
// the live release: its row's release columns, its stored archive, and its
// capabilities, so a release replaced since the backup is never launched
// again. An installation of the same id but another plugin, and every
// installation when liveURL is "" (another deployment, or a separate
// restore), is turned off and reported; one only the backup has is turned
// off by the stricter switches (stricterFlags). It returns the
// installations it turned off here.
func (l PGLoader) CarryPluginReleases(ctx context.Context, dbURL, liveURL string) ([]string, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var live *pgx.Conn
	if liveURL != "" {
		if live, err = pgx.Connect(ctx, liveURL); err != nil {
			return nil, fmt.Errorf("reading the live server's plugins: %w", err)
		}
		defer func() { _ = live.Close(context.WithoutCancel(ctx)) }()
	}
	var off []string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		restored, err := collectPairs(ctx, tx, `SELECT id, plugin_id FROM plugin_installations WHERE kind IS DISTINCT FROM 'builtin'`)
		if err != nil {
			return err
		}
		for id, plugin := range restored {
			matched := false
			if live != nil {
				var livePlugin string
				err := live.QueryRow(ctx, `SELECT plugin_id FROM plugin_installations WHERE id = $1`, id).Scan(&livePlugin)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if errors.Is(err, pgx.ErrNoRows) {
					continue // only the backup has it: stricterFlags turns it off
				}
				matched = livePlugin == plugin
			}
			if matched {
				if err := copyPluginRelease(ctx, live, tx, id); err != nil {
					return fmt.Errorf("plugin installation %d: %w", id, err)
				}
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE plugin_installations SET enabled = false WHERE id = $1`, id); err != nil {
				return err
			}
			off = append(off, fmt.Sprintf("%s (installation %d)", plugin, id))
		}
		return nil
	})
	return off, err
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

// collectPairs reads (id, text) rows into a map.
func collectPairs(ctx context.Context, q queryer, sql string, args ...any) (map[int64]string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

// carryPluginReleases runs CarryPluginReleases and says what it turned off.
func carryPluginReleases(ctx context.Context, loader DatabaseLoader, dbURL, liveURL string, out io.Writer) error {
	off, err := loader.CarryPluginReleases(ctx, dbURL, liveURL)
	if err != nil {
		return fmt.Errorf("keeping the live server's plugin releases: %w", err)
	}
	if len(off) > 0 {
		_, _ = fmt.Fprintf(out, "Turned off %d plugins whose release this server does not have; reinstall or enable them after Silo starts: %s.\n", len(off), strings.Join(off, ", "))
	}
	return nil
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
