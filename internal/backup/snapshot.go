package backup

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

// Store names inside blobs.tar. Avatars live in the operational store; every
// other blob a backup copies lives in the assets store.
const (
	storeAssets      = "assets"
	storeOperational = "operational"
)

// inventoryTables is the core table inventory every manifest records and
// every full restore compares (docs/architecture/backups.md).
var inventoryTables = []string{
	"users", "user_profiles", "media_folders", "media_items", "media_files",
	"server_settings", "plugin_installations", tableLibraryCollections,
	"user_watch_history", "user_watch_progress",
}

const tableLibraryCollections = "library_collections"

// sourceLibraryPoster names what references a library poster blob.
const sourceLibraryPoster = "library poster"

// excludedTableData lists the tables whose rows a dump leaves out: logs and
// queues that are large and rebuildable or disposable; and backup_status,
// whose row describes the backup being taken and would read as a stale run
// after a restore. Their schema is always kept. Each entry also excludes its
// partitions.
func excludedTableData(includeIntroFingerprints bool) []string {
	tables := []string{"activity_log", "operational_logs", "policy_decisions", "catalog_search_index_events", "backup_status"}
	if !includeIntroFingerprints {
		tables = append(tables, "media_intro_fingerprints")
	}
	return tables
}

// blobRef is one blob the snapshot references.
type blobRef struct {
	store string
	key   string
	// ladder copies the stored variants of an artwork original beside it:
	// every key in the image-type directory that belongs to the same
	// revision. Only the referenced key itself is required.
	ladder bool
	source string
}

// snapshotFacts is everything a backup reads from its snapshot besides the
// dump itself.
type snapshotFacts struct {
	gooseVersion    int64
	applied         []int64
	postgresVersion string
	serverMajor     int
	tables          []TableCount
	blobs           []blobRef
}

// readSnapshot reads the inventory, versions, and referenced blob keys in
// the transaction that exported the dump's snapshot, so all three describe
// the same database state.
func readSnapshot(ctx context.Context, tx pgx.Tx, includeSubtitles bool) (snapshotFacts, error) {
	var facts snapshotFacts
	var versionNum int
	if err := tx.QueryRow(ctx, `SELECT current_setting('server_version'), current_setting('server_version_num')::int`).Scan(&facts.postgresVersion, &versionNum); err != nil {
		return facts, fmt.Errorf("reading the server version: %w", err)
	}
	facts.serverMajor = versionNum / 10000
	var err error
	if facts.gooseVersion, facts.applied, err = appliedMigrations(ctx, tx); err != nil {
		return facts, err
	}
	if facts.tables, err = tableInventory(ctx, tx); err != nil {
		return facts, err
	}
	if facts.blobs, err = referencedBlobs(ctx, tx, includeSubtitles); err != nil {
		return facts, err
	}
	return facts, nil
}

// queryer is what inventory and blob enumeration need: a snapshot
// transaction while backing up, a plain connection while verifying a restore.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// appliedMigrations reads the applied goose versions, ascending and without
// goose's version 0, and the newest of them: none, and 0, when the database
// has no goose table. goose deletes a version's row when it rolls it back.
func appliedMigrations(ctx context.Context, q queryer) (int64, []int64, error) {
	applied := []int64{}
	present, err := tableExists(ctx, q, "goose_db_version")
	if err != nil || !present {
		return 0, applied, err
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT version_id FROM public.goose_db_version WHERE is_applied AND version_id > 0 ORDER BY version_id`)
	if err != nil {
		return 0, nil, fmt.Errorf("reading the applied migrations: %w", err)
	}
	applied, err = pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, nil, fmt.Errorf("reading the applied migrations: %w", err)
	}
	if len(applied) == 0 {
		return 0, []int64{}, nil
	}
	return applied[len(applied)-1], applied, nil
}

// tableExists checks presence first, so an older schema without a table
// records "absent" instead of failing the query.
func tableExists(ctx context.Context, q queryer, table string) (bool, error) {
	var present bool
	if err := q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&present); err != nil {
		return false, fmt.Errorf("checking for table %s: %w", table, err)
	}
	return present, nil
}

func tableInventory(ctx context.Context, q queryer) ([]TableCount, error) {
	out := make([]TableCount, 0, len(inventoryTables))
	for _, table := range inventoryTables {
		present, err := tableExists(ctx, q, table)
		if err != nil {
			return nil, err
		}
		row := TableCount{Name: table, Present: present}
		if present {
			if err := q.QueryRow(ctx, `SELECT count(*) FROM public.`+pgx.Identifier{table}.Sanitize()).Scan(&row.Rows); err != nil {
				return nil, fmt.Errorf("counting %s: %w", table, err)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// referencedBlobs enumerates the irreplaceable blobs the snapshot references.
// Listing the store instead would copy whatever happens to be there, including
// objects nothing references, and miss nothing it should only by luck.
func referencedBlobs(ctx context.Context, q queryer, includeSubtitles bool) ([]blobRef, error) {
	var refs []blobRef
	add := func(store, source string, ladder bool, keys []string) {
		for _, key := range keys {
			refs = append(refs, blobRef{store: store, key: key, ladder: ladder, source: source})
		}
	}
	type query struct {
		table, source, sql string
		store              string
		ladder             bool
	}
	cached := func(column string) string {
		return fmt.Sprintf(`coalesce(%[1]s, '') NOT IN ('', '-') AND %[1]s NOT LIKE '%%://%%'`, column)
	}
	var queries []query
	for _, col := range metadata.UploadedArtworkColumns() {
		queries = append(queries, query{
			table: col.Table, source: "uploaded artwork", store: storeAssets, ladder: true,
			sql: fmt.Sprintf(`SELECT DISTINCT %[1]s FROM public.%[2]s WHERE lower(coalesce(%[3]s, '')) LIKE '%[4]s' AND %[5]s`,
				col.PathColumn, col.Table, col.SourceColumn, metadata.IsUploadedSourceSQL, cached(col.PathColumn)),
		})
	}
	queries = append(queries,
		query{table: tableLibraryCollections, source: "collection poster", store: storeAssets, ladder: true,
			sql: `SELECT DISTINCT poster_url FROM public.library_collections WHERE ` + cached("poster_url")},
		query{table: tableLibraryCollections, source: "collection backdrop", store: storeAssets, ladder: true,
			sql: `SELECT DISTINCT backdrop_url FROM public.library_collections WHERE ` + cached("backdrop_url")},
		query{table: "user_personal_collections", source: "personal collection poster", store: storeAssets, ladder: true,
			sql: `SELECT DISTINCT poster_url FROM public.user_personal_collections WHERE ` + cached("poster_url")},
		query{table: "media_folders", source: sourceLibraryPoster, store: storeAssets,
			sql: `SELECT DISTINCT poster_path FROM public.media_folders WHERE ` + cached("poster_path")},
		// An uploaded avatar is "upload:<key>"; presets and URLs are not blobs.
		query{table: "user_profiles", source: "profile avatar", store: storeOperational, ladder: true,
			sql: `SELECT DISTINCT substr(avatar, 8) FROM public.user_profiles WHERE avatar LIKE 'upload:%' AND avatar NOT LIKE 'upload:%://%' AND length(avatar) > 7`},
	)
	if includeSubtitles {
		queries = append(queries, query{table: "downloaded_subtitles", source: "downloaded subtitle", store: storeAssets,
			sql: `SELECT DISTINCT s3_key FROM public.downloaded_subtitles WHERE ` + cached("s3_key")})
	}
	for _, qy := range queries {
		present, err := tableExists(ctx, q, qy.table)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		keys, err := collectStrings(ctx, q, qy.sql)
		if err != nil {
			return nil, fmt.Errorf("listing %s keys: %w", qy.source, err)
		}
		add(qy.store, qy.source, qy.ladder, keys)
	}
	branded, err := brandingKeys(ctx, q)
	if err != nil {
		return nil, err
	}
	add(storeAssets, "branding asset", false, branded)
	return dedupeRefs(refs), nil
}

func brandingKeys(ctx context.Context, q queryer) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT key, value FROM public.server_settings WHERE key = ANY($1)`, branding.AssetSettingKeys())
	if err != nil {
		return nil, fmt.Errorf("reading branding refs: %w", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key, ref string
		if err := rows.Scan(&key, &ref); err != nil {
			return nil, err
		}
		if object := branding.AssetObjectKey(key, strings.TrimSpace(ref)); object != "" {
			keys = append(keys, object)
		}
	}
	return keys, rows.Err()
}

func collectStrings(ctx context.Context, q queryer, sql string) ([]string, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSpace(value))
	}
	return out, rows.Err()
}

func dedupeRefs(refs []blobRef) []blobRef {
	seen := make(map[string]int, len(refs))
	out := refs[:0]
	for _, ref := range refs {
		id := ref.store + "/" + ref.key
		if i, ok := seen[id]; ok {
			out[i].ladder = out[i].ladder || ref.ladder
			continue
		}
		seen[id] = len(out)
		out = append(out, ref)
	}
	return out
}
