package migrations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Run the committed migration SQL in a temporary schema, rolled back with the
// fixture. No application tables or Goose version records are modified.
func adminMigrationFixture(t *testing.T) (pgx.Tx, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	schema := fmt.Sprintf("admin_migration_test_%d", time.Now().UnixNano())
	migrationExec(t, tx, "CREATE SCHEMA "+schema+"; SET LOCAL search_path TO "+schema+", public")
	return tx, schema
}

func migrationExec(t *testing.T, tx pgx.Tx, sql string) {
	t.Helper()
	if _, err := tx.Exec(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
}

func adminMigrationSQL(t *testing.T, name, schema string, down bool) string {
	t.Helper()
	data, err := os.ReadFile("sql/" + name + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	up, rollback, found := strings.Cut(string(data), "-- +goose Down")
	if !found {
		t.Fatal("migration has no down marker", name)
	}
	sql := up
	if down {
		sql = rollback
	}
	return strings.NewReplacer("public.", schema+".", "'public'", "'"+schema+"'").Replace(sql)
}

func requireMigrationSQLState(t *testing.T, tx pgx.Tx, sql, code string) {
	t.Helper()
	savepoint, err := tx.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = savepoint.Exec(t.Context(), sql)
	_ = savepoint.Rollback(t.Context())
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || pgErr.Code != code {
		t.Fatalf("expected SQLSTATE %s, got %v", code, err)
	}
}

func TestHistoryImportMappingColumnRepairPostgres(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_%t", legacy), func(t *testing.T) {
			tx, schema := adminMigrationFixture(t)
			migrationExec(t, tx, `
CREATE TABLE users (id integer PRIMARY KEY);
CREATE TABLE user_profiles (user_id integer REFERENCES users(id) ON DELETE CASCADE, id text, PRIMARY KEY (user_id, id));
CREATE TABLE history_import_sources (id integer PRIMARY KEY, name text, source_type text, base_url text, system_id text, enabled boolean, sort_order integer, admin_token text);
CREATE TABLE history_import_runs (id integer PRIMARY KEY, connection_mode text CONSTRAINT history_import_runs_connection_mode_check CHECK (connection_mode IN ('custom')));
INSERT INTO users VALUES (1);
INSERT INTO user_profiles VALUES (1, 'profile');
INSERT INTO history_import_sources(id) VALUES (1);`)
			migrationExec(t, tx, adminMigrationSQL(t, "049_admin_history_import", schema, false))
			migrationExec(t, tx, `INSERT INTO history_import_user_mappings(id,source_id,external_user_id,silo_user_id,silo_profile_id) VALUES (2,1,'external',1,'profile'); INSERT INTO history_import_runs(id,mapping_id) VALUES(1,2);`)
			if legacy {
				migrationExec(t, tx, `ALTER TABLE history_import_user_mappings RENAME COLUMN silo_user_id TO continuum_user_id; ALTER TABLE history_import_user_mappings RENAME COLUMN silo_profile_id TO continuum_profile_id;`)
			}
			migrationExec(t, tx, adminMigrationSQL(t, "20260905195759_add_history_import_editor_revisions", schema, false))
			var before int64
			if err := tx.QueryRow(t.Context(), "SELECT revision FROM history_import_user_mappings WHERE id=2").Scan(&before); err != nil {
				t.Fatal(err)
			}
			repair := adminMigrationSQL(t, "20260909162756_repair_history_import_mapping_columns", schema, false)
			if legacy {
				conflict, err := tx.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				migrationExec(t, conflict, "ALTER TABLE history_import_user_mappings ADD COLUMN silo_profile_id text")
				requireMigrationSQLState(t, conflict, repair, "P0001")
				// The first rename must roll back if the second column conflicts.
				migrationExec(t, conflict, "SELECT continuum_user_id FROM history_import_user_mappings WHERE id=2")
				if err := conflict.Rollback(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			migrationExec(t, tx, repair)
			migrationExec(t, tx, repair) // already canonical is a no-op
			var userID int
			var profileID string
			var revision int64
			if err := tx.QueryRow(t.Context(), "SELECT silo_user_id, silo_profile_id, revision FROM history_import_user_mappings WHERE id=2").Scan(&userID, &profileID, &revision); err != nil {
				t.Fatal(err)
			}
			if userID != 1 || profileID != "profile" || revision != before {
				t.Fatalf("mapping changed during repair: %d %s %d (was %d)", userID, profileID, revision, before)
			}
			migrationExec(t, tx, "UPDATE history_import_user_mappings SET external_user_name='Renamed' WHERE id=2")
			if err := tx.QueryRow(t.Context(), "SELECT revision FROM history_import_user_mappings WHERE id=2").Scan(&revision); err != nil || revision <= before {
				t.Fatalf("revision trigger did not advance: %d %v", revision, err)
			}
			requireMigrationSQLState(t, tx, "UPDATE history_import_user_mappings SET silo_profile_id='missing' WHERE id=2", "23503")
			migrationExec(t, tx, adminMigrationSQL(t, "20260909162756_repair_history_import_mapping_columns", schema, true))
			migrationExec(t, tx, "DELETE FROM users WHERE id=1")
			var mappings int
			if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM history_import_user_mappings").Scan(&mappings); err != nil || mappings != 0 {
				t.Fatalf("user FK cascade was lost: %d %v", mappings, err)
			}
			var runMapping *int
			if err := tx.QueryRow(t.Context(), "SELECT mapping_id FROM history_import_runs WHERE id=1").Scan(&runMapping); err != nil || runMapping != nil {
				t.Fatalf("run FK was lost: %v %v", runMapping, err)
			}
		})
	}
}

func TestNotificationRetentionConstraintRepairPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, "CREATE TABLE media_folders(id integer PRIMARY KEY)")
	migrationExec(t, tx, adminMigrationSQL(t, "20260611100000_profile_release_notifications", schema, false))
	migrationExec(t, tx, `
INSERT INTO release_events(id,library_id,series_id,episode_id,season_number,episode_number,episode_key,available_at,dedupe_key,processed_at,created_at)
VALUES ('event',1,'series','episode',1,1,1001,now(),'event',now()-interval '90 days',now()-interval '90 days');
INSERT INTO notification_deliveries(id,release_event_id,user_id,profile_id,library_id,series_id,episode_id,type,reason_flags)
VALUES ('delivery','event',1,'profile',1,'series','episode','episode.available','{}');`)
	// Reproduce the old daily retention failure before applying the repair.
	requireMigrationSQLState(t, tx, "DELETE FROM release_events WHERE id='event'", "23514")
	migrationExec(t, tx, adminMigrationSQL(t, "20260909162810_repair_notification_retention_constraint", schema, false))
	migrationExec(t, tx, "DELETE FROM release_events WHERE processed_at < now()-interval '30 days'")
	var eventID *string
	var libraryID int
	var seriesID, episodeID string
	if err := tx.QueryRow(t.Context(), "SELECT release_event_id, library_id, series_id, episode_id FROM notification_deliveries WHERE id='delivery'").Scan(&eventID, &libraryID, &seriesID, &episodeID); err != nil {
		t.Fatal(err)
	}
	if eventID != nil || libraryID != 1 || seriesID != "series" || episodeID != "episode" {
		t.Fatal("retention did not preserve the durable episode snapshot")
	}
	for _, column := range []string{"library_id", "series_id", "episode_id"} {
		requireMigrationSQLState(t, tx, "UPDATE notification_deliveries SET "+column+"=NULL WHERE id='delivery'", "23514")
	}
	migrationExec(t, tx, `INSERT INTO notification_deliveries(id,user_id,profile_id,type,reason_flags) VALUES ('operational',1,'profile','webhook.auto_disabled','{}')`)
	// A rollback must refuse to erase retained inbox rows to recreate the bug.
	requireMigrationSQLState(t, tx, adminMigrationSQL(t, "20260909162810_repair_notification_retention_constraint", schema, true), "23514")
}
