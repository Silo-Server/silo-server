package catalog

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Use the actual cleanup predicate against the migrated schema and live webhook
// references. A stale Plex-table reference makes this query fail with 42P01.
func TestOrphanCleanupAfterPlexTableDropPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var absent bool
	if err := tx.QueryRow(t.Context(), `SELECT to_regclass('public.plex_sync_item_state') IS NULL AND to_regclass('public.plex_sync_item_bindings') IS NULL`).Scan(&absent); err != nil || !absent {
		t.Fatalf("requires migrated schema: %t %v", absent, err)
	}
	if _, err := tx.Exec(t.Context(), `
 INSERT INTO media_items(content_id,type,title,status) VALUES
 ('drop-test-orphan','movie','Orphan','pending'),
 ('drop-test-webhook','movie','Webhook','pending');
 INSERT INTO users(id,username,role) VALUES(879001,'orphan-drop-test','user');
 INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret)
 VALUES('00000000-0000-0000-0000-000000879001',879001,'plex','orphan-drop-test');
 INSERT INTO webhook_sync_item_state(connection_id,external_user_id,external_item_id,media_item_id,last_event_at)
 VALUES('00000000-0000-0000-0000-000000879001','actor','item','drop-test-webhook',now());`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(t.Context(), `SELECT mi.content_id FROM public.media_items mi `+orphanedProvisionalMediaItemPredicate+` AND mi.content_id IN ('drop-test-orphan','drop-test-webhook') ORDER BY mi.content_id`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"drop-test-orphan"}) {
		t.Fatalf("cleanup candidates = %v", got)
	}
}
