package migrations

import (
	"fmt"
	"strings"
	"testing"
)

const (
	serialToIdentityMigration = "20260912231311_serial_ids_to_identity"
	varcharToTextMigration    = "20260912231313_notification_varchar_to_text"
	renameTablesMigration     = "20260912231314_rename_tables_for_consistency"
)

var serialToIdentityTables = []string{
	"abs_sessions", "activity_log", "api_keys", "artwork_revision_gc_candidates",
	"autoscan_events", "autoscan_webhook_deliveries", "catalog_search_index_events",
	"download_artifact_orphans", "downloaded_subtitles", "invite_codes", "marker_edit_audit",
	"media_files", "media_folder_paths", "media_folders", "media_identity_overrides",
	"metadata_image_cache_jobs", "metadata_translation_jobs", "playback_route_events",
	"plugin_auth_bindings", "plugin_auth_identities", "plugin_capabilities", "plugin_installations",
	"plugin_repositories", "plugin_runtime_configs", "plugin_task_bindings", "stream_nodes",
	"subtitle_ai_jobs", "task_executions", "task_triggers", "user_setting_migration_rejects",
	"user_setting_values", "users",
}

type varcharColumn struct {
	table, column string
	length        int
}

var varcharToTextColumns = []varcharColumn{
	{"notification_server_channels", "name", 64}, {"notification_server_channels", "url_host", 253},
	{"notification_server_channels", "disabled_reason", 256}, {"notification_server_channels", "last_failure_message", 256},
	{"notification_webhooks", "name", 64}, {"notification_webhooks", "url_host", 253},
	{"notification_webhooks", "disabled_reason", 256}, {"notification_webhooks", "last_failure_message", 256},
	{"push_delivery_attempts", "upstream_reason", 256}, {"push_delivery_attempts", "failure_message", 256},
	{"push_devices", "device_id", 128}, {"web_push_delivery_attempts", "failure_message", 256},
	{"web_push_subscriptions", "device_name", 128}, {"webhook_delivery_attempts", "failure_message", 256},
	{"apple_push_installations", "device_id", 128}, {"android_push_installations", "device_id", 128},
}

func TestStandardizationMigrationScope(t *testing.T) {
	for _, direction := range []bool{false, true} {
		sql := adminMigrationSQL(t, serialToIdentityMigration, "public", direction)
		for _, table := range serialToIdentityTables {
			if strings.Count(sql, "'"+table+"'") != 1 {
				t.Errorf("identity target %s missing or duplicated", table)
			}
		}
		for _, name := range []string{serialToIdentityMigration, varcharToTextMigration, renameTablesMigration} {
			sql := adminMigrationSQL(t, name, "public", direction)
			if strings.Contains(sql, "UPDATE server_settings") || strings.Contains(sql, "DELETE FROM") || strings.Contains(sql, "CASCADE") {
				t.Errorf("unexpected data mutation in %s", name)
			}
		}
	}
}

func TestSerialToIdentityPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	// Independent fixture list: every target must work, including the partitioned
	// activity log, integer and bigint keys, unused sequences and deleted-row gaps.
	for i, table := range serialToIdentityTables {
		typ := "serial"
		if i%2 == 0 {
			typ = "bigserial"
		}
		partition := ""
		if table == "activity_log" {
			partition = " PARTITION BY RANGE(id)"
		}
		migrationExec(t, tx, fmt.Sprintf("CREATE TABLE %s(id %s PRIMARY KEY)%s", table, typ, partition))
		if table == "activity_log" {
			migrationExec(t, tx, "CREATE TABLE activity_log_default PARTITION OF activity_log DEFAULT")
		}
		switch i % 3 {
		case 1:
			migrationExec(t, tx, fmt.Sprintf("SELECT setval('%s_id_seq',50,true)", table))
		case 2:
			migrationExec(t, tx, fmt.Sprintf("INSERT INTO %s DEFAULT VALUES; INSERT INTO %s DEFAULT VALUES; DELETE FROM %s WHERE id=2; SELECT setval('%s_id_seq',70,true)", table, table, table, table))
		}
	}
	migrationExec(t, tx, "CREATE SEQUENCE revision_seq; ALTER TABLE users ADD COLUMN revision bigint DEFAULT nextval('revision_seq'); CREATE TABLE user_reference(user_id bigint REFERENCES users(id)); INSERT INTO users(id) VALUES(900); INSERT INTO user_reference VALUES(900)")
	// Exercise sequence options and a noncanonical sequence name in both directions.
	migrationExec(t, tx, "ALTER SEQUENCE users_id_seq RENAME TO custom_user_ids; ALTER SEQUENCE custom_user_ids INCREMENT BY 3 START WITH 4 MINVALUE 1 MAXVALUE 100000 CACHE 4 CYCLE")
	up := adminMigrationSQL(t, serialToIdentityMigration, schema, false)
	// A dependency on the last sequence makes the entire migration fail after
	// earlier conversions. A retry must see their original defaults and positions.
	migrationExec(t, tx, "CREATE TABLE sequence_blocker(id bigint DEFAULT nextval('custom_user_ids'))")
	requireMigrationSQLState(t, tx, up, "2BP01")
	var identity string
	if err := tx.QueryRow(t.Context(), "SELECT attidentity::text FROM pg_attribute WHERE attrelid='abs_sessions'::regclass AND attname='id'").Scan(&identity); err != nil || identity != "" {
		t.Fatalf("partial conversion escaped rollback: %q %v", identity, err)
	}
	migrationExec(t, tx, "DROP TABLE sequence_blocker")
	for phase, down := range []bool{false, true, false} {
		migrationExec(t, tx, adminMigrationSQL(t, serialToIdentityMigration, schema, down))
		for i, table := range serialToIdentityTables {
			wantKind := "d"
			if down {
				wantKind = ""
			}
			if err := tx.QueryRow(t.Context(), "SELECT attidentity::text FROM pg_attribute WHERE attrelid=$1::regclass AND attname='id'", table).Scan(&identity); err != nil || identity != wantKind {
				t.Fatalf("%s identity=%q want=%q: %v", table, identity, wantKind, err)
			}
			var got int64
			if err := tx.QueryRow(t.Context(), fmt.Sprintf("INSERT INTO %s DEFAULT VALUES RETURNING id", table)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			want := int64(1)
			switch i % 3 {
			case 1:
				want = 51
			case 2:
				want = 71
			}
			step := int64(1)
			if table == "users" {
				want = 53
				step = 12 // Each conversion preserves the end of the four-value reservation.
			}
			want += int64(phase) * step
			if got != want {
				t.Errorf("phase %d %s next id=%d want=%d", phase, table, got, want)
			}
		}
		var sequence string
		var increment, start, maximum, cache int64
		var cycle bool
		if err := tx.QueryRow(t.Context(), "SELECT c.relname,s.seqincrement,s.seqstart,s.seqmax,s.seqcache,s.seqcycle FROM pg_sequence s JOIN pg_class c ON c.oid=s.seqrelid WHERE s.seqrelid=pg_get_serial_sequence('users','id')::regclass").Scan(&sequence, &increment, &start, &maximum, &cache, &cycle); err != nil || sequence != "custom_user_ids" || increment != 3 || start != 4 || maximum != 100000 || cache != 4 || !cycle {
			t.Fatalf("sequence options changed: %s %d %d %d %t %v", sequence, increment, start, maximum, cycle, err)
		}
		requireMigrationSQLState(t, tx, "INSERT INTO users(id) VALUES(900)", "23505")
		requireMigrationSQLState(t, tx, "DELETE FROM users WHERE id=900", "23503")
		requireMigrationSQLState(t, tx, "INSERT INTO users(id) VALUES(NULL)", "23502")
	}
	migrationExec(t, tx, "INSERT INTO activity_log_default DEFAULT VALUES; INSERT INTO users(id) VALUES(901)")
	var revisionIdentity string
	if err := tx.QueryRow(t.Context(), "SELECT attidentity::text FROM pg_attribute WHERE attrelid='users'::regclass AND attname='revision'").Scan(&revisionIdentity); err != nil || revisionIdentity != "" {
		t.Fatalf("revision generator changed: %v", err)
	}
}

func TestNotificationTextPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	tables := map[string]bool{}
	for _, c := range varcharToTextColumns {
		if !tables[c.table] {
			migrationExec(t, tx, "CREATE TABLE "+c.table+"(id integer PRIMARY KEY)")
			tables[c.table] = true
		}
		migrationExec(t, tx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s varchar(%d)", c.table, c.column, c.length))
	}
	migrationExec(t, tx, "ALTER TABLE web_push_subscriptions ALTER COLUMN device_name SET DEFAULT ''; CREATE UNIQUE INDEX push_device_test_unique ON push_devices(device_id)")
	for table := range tables {
		migrationExec(t, tx, "INSERT INTO "+table+"(id) VALUES(1)")
	}
	for _, c := range varcharToTextColumns {
		migrationExec(t, tx, fmt.Sprintf("UPDATE %s SET %s=repeat('界',%d)", c.table, c.column, c.length))
	}
	var before string
	snapshot := `SELECT jsonb_object_agg(relname,relfilenode)::text FROM pg_class WHERE relnamespace=$1::regnamespace AND relkind IN ('r','i')`
	if err := tx.QueryRow(t.Context(), snapshot, schema).Scan(&before); err != nil {
		t.Fatal(err)
	}
	migrationExec(t, tx, adminMigrationSQL(t, varcharToTextMigration, schema, false))
	var after string
	if err := tx.QueryRow(t.Context(), snapshot, schema).Scan(&after); err != nil || before != after {
		t.Fatalf("widening rewrote storage: %v", err)
	}
	for _, c := range varcharToTextColumns {
		var typ string
		if err := tx.QueryRow(t.Context(), "SELECT data_type FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 AND column_name=$3", schema, c.table, c.column).Scan(&typ); err != nil || typ != "text" {
			t.Fatalf("%s.%s=%s %v", c.table, c.column, typ, err)
		}
	}
	down := adminMigrationSQL(t, varcharToTextMigration, schema, true)
	// This is the last table narrowed: a failure must also undo earlier narrowing.
	migrationExec(t, tx, "UPDATE notification_server_channels SET name=repeat('界',65)")
	requireMigrationSQLState(t, tx, down, "22001")
	migrationExec(t, tx, "UPDATE android_push_installations SET device_id=repeat('x',129)")
	migrationExec(t, tx, "UPDATE android_push_installations SET device_id=repeat('x',128); UPDATE notification_server_channels SET name=repeat('界',64)")
	migrationExec(t, tx, down)
	for _, c := range varcharToTextColumns {
		var length int
		if err := tx.QueryRow(t.Context(), "SELECT character_maximum_length FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 AND column_name=$3", schema, c.table, c.column).Scan(&length); err != nil || length != c.length {
			t.Fatalf("%s.%s cap=%d %v", c.table, c.column, length, err)
		}
	}
	migrationExec(t, tx, adminMigrationSQL(t, varcharToTextMigration, schema, false))
	requireMigrationSQLState(t, tx, "INSERT INTO push_devices(id,device_id) SELECT 2,device_id FROM push_devices WHERE id=1", "23505")
}

func TestTableRenameFailurePostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, adminMigrationSQL(t, "126_oauth_session", schema, false))
	migrationExec(t, tx, `CREATE TABLE users(id integer PRIMARY KEY);
 CREATE TABLE playback_history_admin(session_id text PRIMARY KEY,user_id integer REFERENCES users(id),started_at timestamptz,ended_at timestamptz,profile_id text);
 CREATE INDEX idx_playback_history_admin_ended ON playback_history_admin(ended_at);
 CREATE INDEX idx_playback_history_admin_started ON playback_history_admin(started_at);
 CREATE INDEX idx_playback_history_admin_user_ended ON playback_history_admin(user_id,ended_at);
 CREATE INDEX idx_playback_history_admin_user_profile_ended ON playback_history_admin(user_id,profile_id,ended_at);
 CREATE TABLE admin_playback_history(blocker integer);`)
	up := adminMigrationSQL(t, renameTablesMigration, schema, false)
	requireMigrationSQLState(t, tx, up, "42P07")
	// Failure on the last table must undo the preceding OAuth table/index renames.
	migrationExec(t, tx, "SELECT * FROM oauth_session; SELECT * FROM oauth_completion")
	migrationExec(t, tx, "DROP TABLE admin_playback_history")
	migrationExec(t, tx, up)
	var n int
	if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint c JOIN pg_class r ON r.oid=c.conrelid WHERE r.relnamespace=$1::regnamespace AND (starts_with(c.conname,'oauth_session_') OR starts_with(c.conname,'oauth_completion_') OR starts_with(c.conname,'playback_history_admin_'))`, schema).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stale constraint names: %d %v", n, err)
	}
	migrationExec(t, tx, adminMigrationSQL(t, renameTablesMigration, schema, true))
	migrationExec(t, tx, up)
}
