package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/migrations"
)

// This test creates its own database: rewinding a shared test database would
// invalidate other packages' fixtures. The test role needs CREATEDB.
func TestDropDeadTablesMigrationPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("silo_dead_tables_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		if err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	provider, err := newMigrationProvider(pool, migrations.FS, "sql")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	exec := func(query string) {
		t.Helper()
		if _, err := pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.UpTo(ctx, 59); err != nil {
		t.Fatal(err)
	}
	exec(`
 INSERT INTO users(id,username) VALUES(879,'dead-table-fixture');
 INSERT INTO plex_sync_connections(id,user_id,plex_server_id,plex_base_url,plex_server_token,webhook_secret)
 VALUES('00000000-0000-0000-0000-000000000879',879,'fixture','https://example.invalid','fixture-token','fixture-secret');
 INSERT INTO plex_sync_actor_mappings(connection_id,plex_account_id,silo_profile_id)
 VALUES('00000000-0000-0000-0000-000000000879',12,'profile');
 INSERT INTO plex_sync_item_bindings(connection_id,media_item_id,plex_rating_key,plex_type)
 VALUES('00000000-0000-0000-0000-000000000879','legacy-item','rating-key','movie');
 INSERT INTO plex_sync_item_state(mapping_id,media_item_id,last_plex_position_ms,last_silo_position_ms)
 SELECT id,'legacy-item',12000,10000 FROM plex_sync_actor_mappings;
 INSERT INTO user_playback_sessions(session_id,user_id,profile_id,media_file_id,play_method)
 VALUES('retired',879,'profile',1,'direct');`)
	// The last historical migrations, including every registered Go migration,
	// run before the new timestamp. No old migration is renumbered or replaced.
	if _, err := provider.UpTo(ctx, 20260912230856); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO content_id_migration_map(old_id,new_id,entity) VALUES('old-fixture','new-fixture','movie')`)
	tables := []string{"plex_sync_item_state", "plex_sync_item_bindings", "plex_sync_actor_mappings", "plex_sync_connections", "user_playback_sessions", "content_id_migration_map"}
	schema := func() string {
		t.Helper()
		var result string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
   'columns', (SELECT jsonb_agg(to_jsonb(c) ORDER BY table_name,ordinal_position) FROM
    (SELECT table_name,column_name,ordinal_position,column_default,is_nullable,data_type,udt_name,collation_name,is_identity,identity_generation,identity_start,identity_increment FROM information_schema.columns WHERE table_schema='public' AND table_name=ANY($1)) c),
   'constraints', (SELECT jsonb_agg(jsonb_build_array(r.relname,c.conname,pg_get_constraintdef(c.oid)) ORDER BY r.relname,c.conname) FROM pg_constraint c JOIN pg_class r ON r.oid=c.conrelid WHERE r.relnamespace='public'::regnamespace AND r.relname=ANY($1)),
   'indexes', (SELECT jsonb_agg(jsonb_build_array(tablename,indexname,indexdef) ORDER BY tablename,indexname) FROM pg_indexes WHERE schemaname='public' AND tablename=ANY($1))
  )::text`, tables).Scan(&result)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := schema()
	webhook := func() string {
		t.Helper()
		var result string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array(
   (SELECT to_jsonb(c) FROM webhook_sync_connections c WHERE id='00000000-0000-0000-0000-000000000879'),
   (SELECT to_jsonb(m) FROM webhook_sync_profile_mappings m WHERE connection_id='00000000-0000-0000-0000-000000000879'),
   (SELECT to_jsonb(s) FROM webhook_sync_item_state s WHERE connection_id='00000000-0000-0000-0000-000000000879')
  )::text`).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	var copied bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM webhook_sync_connections c JOIN webhook_sync_profile_mappings m ON m.connection_id=c.id JOIN webhook_sync_item_state s ON s.connection_id=c.id WHERE c.id='00000000-0000-0000-0000-000000000879' AND c.provider='plex' AND m.silo_profile_id='profile' AND s.external_item_id='rating-key' AND s.last_position_seconds=12)`).Scan(&copied); err != nil || !copied {
		t.Fatalf("legacy webhook data copy: %t %v", copied, err)
	}
	active := webhook()
	for cycle := range 2 {
		if _, err := provider.UpTo(ctx, 20260912230857); err != nil {
			t.Fatal(err)
		}
		for _, table := range tables {
			var absent bool
			if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NULL", "public."+table).Scan(&absent); err != nil || !absent {
				t.Fatalf("table %s still exists: %v", table, err)
			}
		}
		if got := webhook(); got != active {
			t.Fatal("drop changed active webhook data")
		}
		if cycle == 0 {
			if _, err := provider.DownTo(ctx, 20260912230856); err != nil {
				t.Fatal(err)
			}
			if got := schema(); got != before {
				t.Fatalf("Down schema differs:\nbefore %s\nafter %s", before, got)
			}
			for _, table := range tables {
				var n int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM public."+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil || n != 0 {
					t.Fatalf("Down table %s: count %d error %v", table, n, err)
				}
			}
		}
	}
	if _, err := provider.UpTo(ctx, 20260912230857); err != nil {
		t.Fatalf("already migrated: %v", err)
	}
}
