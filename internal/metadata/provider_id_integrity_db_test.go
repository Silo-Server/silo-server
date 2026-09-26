package metadata

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSyncMergeStepsAfterPlexTableDropPostgres(t *testing.T) {
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
 ('drop-test-source','movie','Source','pending'),
 ('drop-test-canonical','movie','Canonical','pending');
 INSERT INTO users(id,username) VALUES(879002,'merge-drop-test');
 INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret)
 VALUES('00000000-0000-0000-0000-000000879002',879002,'plex','merge-drop-test');
 INSERT INTO webhook_sync_item_state(connection_id,external_user_id,external_item_id,media_item_id,last_event_at,last_position_seconds)
 VALUES('00000000-0000-0000-0000-000000879002','actor','item','drop-test-source','2026-01-01',12);`); err != nil {
		t.Fatal(err)
	}
	// Execute the sync steps from the production canonicalization sequence.
	// Including retired Plex steps here makes their removal a regression guard.
	for _, step := range mediaItemMergeSteps {
		if !strings.Contains(step.sql, "plex_sync_") && !strings.Contains(step.sql, "webhook_sync_") {
			continue
		}
		if _, err := tx.Exec(t.Context(), step.sql, mergeStepArgs(step.sql, "drop-test-source", "drop-test-canonical")...); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}
	var id string
	var position float64
	if err := tx.QueryRow(t.Context(), `SELECT media_item_id,last_position_seconds FROM webhook_sync_item_state WHERE connection_id='00000000-0000-0000-0000-000000879002'`).Scan(&id, &position); err != nil {
		t.Fatal(err)
	}
	if id != "drop-test-canonical" || position != 12 {
		t.Fatalf("webhook state = %s, %v", id, position)
	}
}
