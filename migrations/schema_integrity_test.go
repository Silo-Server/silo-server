package migrations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const fkLeadingIndexesMigration = "20260912231017_fk_leading_indexes"
const userFKIntegrityMigration = "20260912231023_user_fk_integrity"
const usersNotNullMigration = "20260912231132_users_role_enabled_not_null"

var userFKContracts = []struct{ table, constraint, column, action string }{
	{"push_devices", "push_devices_user_id_fkey", "user_id", "CASCADE"},
	{"web_push_subscriptions", "web_push_subscriptions_user_id_fkey", "user_id", "CASCADE"},
	{"notification_webhooks", "notification_webhooks_user_id_fkey", "user_id", "CASCADE"},
	{"notification_email_prefs", "notification_email_prefs_user_id_fkey", "user_id", "CASCADE"},
	{"notification_discord_prefs", "notification_discord_prefs_user_id_fkey", "user_id", "CASCADE"},
	{"notification_discord_link_state", "notification_discord_link_state_user_id_fkey", "user_id", "CASCADE"},
	{"notification_deliveries", "notification_deliveries_user_id_fkey", "user_id", "CASCADE"},
	{"profile_series_interest", "profile_series_interest_user_id_fkey", "user_id", "CASCADE"},
	{"user_audio_preferences", "user_audio_preferences_user_id_fkey", "user_id", "CASCADE"},
	{"recommendation_cache", "recommendation_cache_user_id_fkey", "user_id", "CASCADE"},
	{"playback_sessions_sync", "playback_sessions_sync_user_id_fkey", "user_id", "CASCADE"},
	{"jellycompat_sessions", "jellycompat_sessions_streamapp_user_id_fkey", "streamapp_user_id", "CASCADE"},
	{"admin_jobs", "admin_jobs_created_by_user_id_fkey", "created_by_user_id", "CASCADE"},
	{"auth_sessions", "auth_sessions_impersonator_user_id_fkey", "impersonator_user_id", "CASCADE"},
	{"activity_log", "activity_log_impersonator_user_id_fkey", "impersonator_user_id", "SET NULL"},
	{"subtitle_ai_jobs", "subtitle_ai_jobs_requested_by_fkey", "requested_by", "SET NULL"},
	{"metadata_translation_jobs", "metadata_translation_jobs_requested_by_fkey", "requested_by", "SET NULL"},
}

var integrityIndexes = []struct{ name, table, column string }{
	{"idx_abs_playback_sessions_content_id", "abs_playback_sessions", "content_id"},
	{"idx_abs_playback_sessions_media_file_id", "abs_playback_sessions", "media_file_id"},
	{"idx_abs_rss_feeds_library_item_id", "abs_rss_feeds", "library_item_id"},
	{"idx_admin_jobs_created_by_user_id", "admin_jobs", "created_by_user_id"},
	{"idx_auth_sessions_user_id", "auth_sessions", "user_id"},
	{"idx_autoscan_connections_request_integration_id", "autoscan_connections", "request_integration_id"},
	{"idx_autoscan_sources_connection_id", "autoscan_sources", "connection_id"},
	{"idx_autoscan_webhook_deliveries_source_id", "autoscan_webhook_deliveries", "source_id"},
	{"idx_device_login_requests_approved_by_user_id", "device_login_requests", "approved_by_user_id"},
	{"idx_device_login_requests_auth_session_id", "device_login_requests", "auth_session_id"},
	{"idx_downloaded_subtitles_downloaded_by", "downloaded_subtitles", "downloaded_by"},
	{"idx_downloads_media_file_id", "downloads", "media_file_id"},
	{"idx_intro_season_analysis_state_media_folder_id", "intro_season_analysis_state", "media_folder_id"},
	{"idx_invitations_accepted_user_id", "invitations", "accepted_user_id"},
	{"idx_invitations_access_group_id", "invitations", "access_group_id"},
	{"idx_invitations_invited_by", "invitations", "invited_by"},
	{"idx_invite_codes_created_by", "invite_codes", "created_by"},
	{"idx_jellycompat_sessions_streamapp_user_id", "jellycompat_sessions", "streamapp_user_id"},
	{"idx_library_collection_items_media_item_id", "library_collection_items", "media_item_id"},
	{"idx_library_collection_libraries_group_id", "library_collection_libraries", "group_id"},
	{"idx_library_provider_chains_plugin_installation_id", "library_provider_chains", "plugin_installation_id"},
	{"idx_literary_work_match_decisions_created_by", "literary_work_match_decisions", "created_by"},
	{"idx_literary_works_primary_cover_content_id", "literary_works", "primary_cover_content_id"},
	{"idx_marker_edit_audit_api_key_id", "marker_edit_audit", "api_key_id"},
	{"idx_marker_edit_audit_impersonator_user_id", "marker_edit_audit", "impersonator_user_id"},
	{"idx_media_group_overrides_created_by_user_id", "media_group_overrides", "created_by_user_id"},
	{"idx_media_group_overrides_updated_by_user_id", "media_group_overrides", "updated_by_user_id"},
	{"idx_media_identity_overrides_created_by_user_id", "media_identity_overrides", "created_by_user_id"},
	{"idx_media_identity_overrides_updated_by_user_id", "media_identity_overrides", "updated_by_user_id"},
	{"idx_media_request_events_actor_user_id", "media_request_events", "actor_user_id"},
	{"idx_media_request_targets_integration_id", "media_request_targets", "integration_id"},
	{"idx_media_root_overrides_created_by_user_id", "media_root_overrides", "created_by_user_id"},
	{"idx_media_root_overrides_updated_by_user_id", "media_root_overrides", "updated_by_user_id"},
	{"idx_metadata_translation_jobs_requested_by", "metadata_translation_jobs", "requested_by"},
	{"idx_notification_deliveries_release_event_id", "notification_deliveries", "release_event_id"},
	{"idx_notification_discord_link_state_user_id", "notification_discord_link_state", "user_id"},
	{"idx_notification_webhooks_user_id", "notification_webhooks", "user_id"},
	{"idx_page_sections_library_id", "page_sections", "library_id"},
	{"idx_playback_route_events_user_id", "playback_route_events", "user_id"},
	{"idx_playback_sessions_sync_user_id", "playback_sessions_sync", "user_id"},
	{"idx_playback_v3_attempts_effective_media_file_id", "playback_v3_attempts", "effective_media_file_id"},
	{"idx_playback_v3_attempts_requested_media_file_id", "playback_v3_attempts", "requested_media_file_id"},
	{"idx_playback_v3_attempts_user_id", "playback_v3_attempts", "user_id"},
	{"idx_plugin_auth_identities_user_id", "plugin_auth_identities", "user_id"},
	{"idx_plugin_installations_repository_id", "plugin_installations", "repository_id"},
	{"idx_policy_documents_active_version_id", "policy_documents", "active_version_id"},
	{"idx_profile_series_interest_user_id", "profile_series_interest", "user_id"},
	{"idx_push_devices_user_id", "push_devices", "user_id"},
	{"idx_subtitle_ai_jobs_requested_by", "subtitle_ai_jobs", "requested_by"},
	{"idx_user_profile_allowed_libraries_library_id", "user_profile_allowed_libraries", "library_id"},
	{"idx_watch_provider_auth_sessions_user_id", "watch_provider_auth_sessions", "user_id"},
	{"idx_watch_provider_connections_user_id", "watch_provider_connections", "user_id"},
	{"idx_watch_provider_scrobble_sessions_connection_id", "watch_provider_scrobble_sessions", "connection_id"},
	{"idx_watch_together_rooms_host_user_id", "watch_together_rooms", "host_user_id"},
	{"idx_watch_together_suggestions_suggester_user_id", "watch_together_suggestions", "suggester_user_id"},
	{"idx_web_push_subscriptions_user_id", "web_push_subscriptions", "user_id"},
}

func TestSchemaIntegrityContracts(t *testing.T) {
	for _, name := range []string{userFKIntegrityMigration, usersNotNullMigration} {
		raw, err := FS.ReadFile("sql/" + name + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		sql := string(raw)
		if strings.Contains(sql, "+goose NO TRANSACTION") {
			t.Fatal("constraints must be transactional")
		}
		// The migration repairs orphaned account-owned rows before adding FKs.
	}

	fkRaw, err := FS.ReadFile("sql/" + userFKIntegrityMigration + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	fkSQL := strings.Join(strings.Fields(string(fkRaw)), " ")
	if strings.Count(fkSQL, "ADD CONSTRAINT ") != len(userFKContracts) {
		t.Fatal("unexpected foreign key count")
	}
	for _, fk := range userFKContracts {
		want := fmt.Sprintf("ALTER TABLE public.%s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES public.users(id) ON DELETE %s;", fk.table, fk.constraint, fk.column, fk.action)
		if !strings.Contains(fkSQL, want) {
			t.Errorf("missing FK contract: %s", want)
		}
		if !strings.Contains(fkSQL, fmt.Sprintf("('%s', '%s')", fk.table, fk.column)) {
			t.Errorf("missing orphan check: %s", fk.table)
		}
	}
	raw, err := FS.ReadFile("sql/" + fkLeadingIndexesMigration + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	if !strings.HasPrefix(sql, "-- +goose NO TRANSACTION") {
		t.Fatal("concurrent indexes require NO TRANSACTION")
	}
	if strings.Count(sql, "CREATE INDEX CONCURRENTLY IF NOT EXISTS ") != len(integrityIndexes) {
		t.Fatal("unexpected index count")
	}
	for _, index := range integrityIndexes {
		want := fmt.Sprintf("CREATE INDEX CONCURRENTLY IF NOT EXISTS %s\n    ON public.%s (%s);", index.name, index.table, index.column)
		if !strings.Contains(sql, want) || !strings.Contains(sql, "'"+index.name+"'") || !strings.Contains(sql, "DROP INDEX CONCURRENTLY IF EXISTS public."+index.name+";") {
			t.Errorf("missing creation/recovery/rollback for %s", index.name)
		}
	}
}

// Exercise actual migration SQL in an isolated schema. Check every foreign key,
// orphan cleanup, nullable attribution, and account deletion.
func TestUserFKIntegrityPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, "CREATE TABLE users(id integer PRIMARY KEY); INSERT INTO users VALUES (1),(2)")
	for _, fk := range userFKContracts {
		nullable := " NOT NULL"
		if fk.column != "user_id" && fk.column != "streamapp_user_id" && fk.column != "created_by_user_id" {
			nullable = ""
		}
		ddl := fmt.Sprintf("CREATE TABLE %s (row_id integer, %s integer%s)", fk.table, fk.column, nullable)
		if fk.table == "activity_log" {
			ddl += " PARTITION BY RANGE (row_id)"
		}
		migrationExec(t, tx, ddl)
		if fk.table == "activity_log" {
			migrationExec(t, tx, "CREATE TABLE activity_log_initial PARTITION OF activity_log FOR VALUES FROM (0) TO (100)")
		}
		migrationExec(t, tx, fmt.Sprintf("INSERT INTO %s VALUES (1,1),(2,2)", fk.table))
		if nullable == "" {
			migrationExec(t, tx, fmt.Sprintf("INSERT INTO %s VALUES (3,NULL)", fk.table))
		}
	}
	up := adminMigrationSQL(t, userFKIntegrityMigration, schema, false)
	down := adminMigrationSQL(t, userFKIntegrityMigration, schema, true)
	for _, fk := range userFKContracts {
		migrationExec(t, tx, fmt.Sprintf("INSERT INTO %s VALUES (99,999)", fk.table))
	}
	migrationExec(t, tx, up)
	migrationExec(t, tx, down)
	migrationExec(t, tx, up)
	// New audit partitions must inherit the FK too.
	migrationExec(t, tx, "CREATE TABLE activity_log_later PARTITION OF activity_log FOR VALUES FROM (100) TO (200); INSERT INTO activity_log VALUES(100,1)")
	for _, fk := range userFKContracts {
		requireMigrationSQLState(t, tx, fmt.Sprintf("INSERT INTO %s VALUES (99,999)", fk.table), "23503")
	}
	migrationExec(t, tx, "DELETE FROM users WHERE id=1")
	for _, fk := range userFKContracts {
		var retained int
		if err := tx.QueryRow(t.Context(), fmt.Sprintf("SELECT count(*) FROM %s WHERE %s=2", fk.table, fk.column)).Scan(&retained); err != nil || retained != 1 {
			t.Fatalf("unrelated account row changed in %s: %d %v", fk.table, retained, err)
		}
		var count int
		predicate := "row_id=1"
		want := 0
		if fk.action == "SET NULL" {
			predicate += " AND " + fk.column + " IS NULL"
			want = 1
		}
		if err := tx.QueryRow(t.Context(), fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", fk.table, predicate)).Scan(&count); err != nil || count != want {
			t.Errorf("%s delete action: %d want %d: %v", fk.table, count, want, err)
		}
	}
	var nullAudit bool
	if err := tx.QueryRow(t.Context(), "SELECT impersonator_user_id IS NULL FROM activity_log WHERE row_id=100").Scan(&nullAudit); err != nil || !nullAudit {
		t.Fatalf("new partition attribution: %t %v", nullAudit, err)
	}
}

func TestUsersRoleEnabledIntegrityPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, "CREATE TABLE users(id integer PRIMARY KEY, role text, enabled boolean DEFAULT true); INSERT INTO users VALUES(1,'admin',false),(2,NULL,NULL)")
	up := adminMigrationSQL(t, usersNotNullMigration, schema, false)
	requireMigrationSQLState(t, tx, up, "P0001")
	var preserved bool
	if err := tx.QueryRow(t.Context(), "SELECT role IS NULL AND enabled IS NULL FROM users WHERE id=2").Scan(&preserved); err != nil || !preserved {
		t.Fatal("NULL account state changed", err)
	}
	migrationExec(t, tx, "UPDATE users SET role='user' WHERE id=2")
	requireMigrationSQLState(t, tx, up, "P0001")
	migrationExec(t, tx, "UPDATE users SET enabled=false WHERE id=2")
	migrationExec(t, tx, up)
	requireMigrationSQLState(t, tx, "INSERT INTO users(id) VALUES(3)", "23502")
	requireMigrationSQLState(t, tx, "UPDATE users SET enabled=NULL WHERE id=2", "23502")
	migrationExec(t, tx, "INSERT INTO users(id,role) VALUES(3,'user')")
	var correct bool
	if err := tx.QueryRow(t.Context(), "SELECT (SELECT role='admin' AND NOT enabled FROM users WHERE id=1) AND (SELECT NOT enabled FROM users WHERE id=2) AND (SELECT enabled FROM users WHERE id=3)").Scan(&correct); err != nil || !correct {
		t.Fatal("account values/default changed", err)
	}
	migrationExec(t, tx, adminMigrationSQL(t, usersNotNullMigration, schema, true))
	migrationExec(t, tx, "UPDATE users SET role=NULL,enabled=NULL WHERE id=2")
}

// Run the concurrent migration through Goose on disposable tables. A failed
// concurrent build leaves an invalid index that IF NOT EXISTS alone would skip.
func TestFKLeadingIndexesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("integrity_indexes_%d", time.Now().UnixNano())
	exec := func(sql string) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + schema)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	tables := map[string][]string{}
	for _, index := range integrityIndexes {
		tables[index.table] = append(tables[index.table], index.column+" integer")
	}
	for table, columns := range tables {
		exec("CREATE TABLE " + schema + "." + table + " (" + strings.Join(columns, ",") + ")")
	}
	first := integrityIndexes[0]
	exec(fmt.Sprintf("INSERT INTO %s.%s (%s) VALUES(1),(1)", schema, first.table, first.column))
	_, err = db.ExecContext(t.Context(), fmt.Sprintf("CREATE UNIQUE INDEX CONCURRENTLY %s ON %s.%s (%s)", first.name, schema, first.table, first.column))
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || pgErr.Code != "23505" {
		t.Fatalf("seed invalid concurrent index: %v", err)
	}
	raw, err := FS.ReadFile("sql/" + fkLeadingIndexesMigration + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.NewReplacer("public.", schema+".", "'public'", "'"+schema+"'").Replace(string(raw))
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fstest.MapFS{"1_indexes.sql": &fstest.MapFile{Data: []byte(sql)}}, goose.WithTableName(schema+".goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	for cycle := range 2 {
		if _, err := provider.Up(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, index := range integrityIndexes {
			var valid bool
			err := db.QueryRowContext(t.Context(), `SELECT i.indisvalid AND i.indisready AND i.indpred IS NULL AND i.indnkeyatts=1 AND a.attname=$3 AND am.amname='btree'
FROM pg_index i JOIN pg_class c ON c.oid=i.indrelid JOIN pg_class ix ON ix.oid=i.indexrelid JOIN pg_am am ON am.oid=ix.relam
JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=i.indkey[0]
WHERE n.nspname=$1 AND c.relname=$2 AND ix.relname=$4`, schema, index.table, index.column, index.name).Scan(&valid)
			if err != nil || !valid {
				t.Fatalf("cycle %d index %s invalid: %v", cycle, index.name, err)
			}
		}
		if _, err := provider.Down(t.Context()); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM pg_indexes WHERE schemaname=$1 AND indexname LIKE 'idx_%'", schema).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback left %d indexes: %v", count, err)
		}
	}
}
