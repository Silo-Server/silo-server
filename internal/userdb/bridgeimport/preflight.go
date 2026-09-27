// Package bridgeimport inventories offline SQLite user databases for the bridge
// release. It deliberately cannot import data or select a storage backend.
package bridgeimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// Mapping describes a required semantic transformation, not an implemented writer.
type Mapping struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Rule   string `json:"rule"`
}

// Manifest returns every persistent application table in the current SQLite schema.
func Manifest() []Mapping {
	var result []Mapping
	for _, name := range strings.Fields("profiles profile_allowed_libraries watch_progress watch_history favorites watchlist home_item_dismissals personal_collections personal_collection_items personal_collection_profiles collection_sort_preferences audio_preferences subtitle_preferences series_playback_preferences library_playback_preferences profile_onboarding") {
		result = append(result, Mapping{name, "user_" + name, "preserve account-scoped identities, values, nulls, ordering and timestamps"})
	}
	result = append(result, Mapping{"hidden_history_items", "user_history_hidden_items", "preserve hidden-before cutoffs"})
	for _, name := range strings.Fields("user_settings user_device_settings user_devices user_setting_values user_setting_mutations user_setting_migration_rejects jellycompat_displayprefs") {
		result = append(result, Mapping{name, name, "add account identity; preserve semantic scope, revisions, replay and opaque values; remap integer surrogate IDs"})
	}
	return append(result,
		Mapping{"personal_collection_revisions", "user_collection_revisions", "add account identity; preserve per-collection revision witnesses including tombstones without live collections; reconcile destination trigger increments before enabling validators"},
		Mapping{"personal_collection_order_revision", "user_collection_order_revisions", "map singleton 1 to the account user_id, not a profile or group; preserve account-wide order witness; reconcile destination revisions before enabling validators"},
		Mapping{"profile_section_overrides", "user_settings", "group all profiles into section_overrides:<scope>:<libraryID> JSON; preserve IDs and timestamps"},
		Mapping{"playback_sessions", "", "legacy disposition required if nonempty"},
		Mapping{"downloads", "", "legacy disposition required if nonempty"})
}

// Table contains only structural metadata. Unknown names are redacted from output.
type Table struct {
	Name    string `json:"name"`
	Rows    int64  `json:"rows"`
	Columns int    `json:"columns"`
}

// Report never authorizes importing or switching providers.
type Report struct {
	Version  int      `json:"schema_version"`
	Tables   []Table  `json:"tables"`
	Blockers []string `json:"blockers"`
	Ready    bool     `json:"ready"`
}

// Inspect requires an explicit existing <accountID>.db file. It uses immutable,
// read-only SQLite access so even shared-memory sidecars cannot be created. WAL
// and rollback-journal sources are refused: this milestone requires a standalone
// offline backup, and does not claim to verify how that backup was captured.
func Inspect(ctx context.Context, path string, accountID int64) (Report, error) {
	r := Report{Blockers: []string{
		"backup consistency and source-to-central-account provenance not verified",
		"target account, profile and catalog references and target conflicts not checked",
		"column-level transformations and semantic equality not verified",
		"progress sync-sequence transition not implemented",
		"transaction writer, durable replay receipt and recovery verification not implemented",
		"all-account completeness and backend-switch gate not implemented",
	}}
	if accountID <= 0 || filepath.Base(path) != strconv.FormatInt(accountID, 10)+".db" {
		return r, errors.New("source filename must match the positive account ID")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return r, errors.New("source must be an existing regular database file")
	}
	if err := checkSidecars(path); err != nil {
		return r, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return r, errors.New("cannot resolve source")
	}
	uri := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro&immutable=1&_query_only=1"}
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return r, errors.New("cannot open source read-only")
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return r, errors.New("cannot read source database")
	}
	defer tx.Rollback() //nolint:errcheck
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&r.Version); err != nil {
		return r, errors.New("cannot read source version")
	}
	if r.Version != 22 {
		r.Blockers = append(r.Blockers, "source schema is not current version 22; upgrades are not performed")
	}
	var integrity string
	if err := tx.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return r, errors.New("source integrity check failed")
	}
	mappings := map[string]Mapping{}
	for _, m := range Manifest() {
		mappings[m.Source] = m
	}
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return r, errors.New("cannot enumerate source tables")
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return r, errors.New("cannot read table inventory")
		}
		names = append(names, name)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return r, errors.New("cannot read table inventory")
	}
	for _, name := range names {
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM "`+strings.ReplaceAll(name, `"`, `""`)+`"`).Scan(&count); err != nil {
			return r, errors.New("cannot count source table")
		}
		var columns int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info(?)", name).Scan(&columns); err != nil {
			return r, errors.New("cannot inspect source columns")
		}
		mapping, known := mappings[name]
		label := name
		if !known {
			label = "[unknown table]"
			r.Blockers = append(r.Blockers, "unmapped source table requires classification")
		} else {
			delete(mappings, name)
			if mapping.Target == "" && count > 0 {
				r.Blockers = append(r.Blockers, fmt.Sprintf("nonempty %s requires legacy disposition", name))
			}
		}
		r.Tables = append(r.Tables, Table{label, count, columns})
	}
	for _, m := range Manifest() {
		if _, missing := mappings[m.Source]; missing {
			r.Blockers = append(r.Blockers, "missing source table: "+m.Source)
		}
	}
	if err := checkSidecars(path); err != nil {
		return r, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return r, errors.New("source changed during inspection")
	}
	return r, nil
}

func checkSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
			return errors.New("source has WAL or journal state; provide a consistent standalone offline backup")
		}
	}
	return nil
}
