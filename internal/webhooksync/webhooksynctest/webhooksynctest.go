// Package webhooksynctest seeds webhook sync state for tests that run against
// a migrated Postgres schema.
package webhooksynctest

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BeginAfterPlexDrop opens a transaction on SILO_TEST_DATABASE_URL that rolls
// back when the test ends. It skips when the URL is unset and fails unless the
// schema has dropped the retired Plex sync tables.
func BeginAfterPlexDrop(t *testing.T) pgx.Tx {
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
	var absent bool
	if err := tx.QueryRow(t.Context(), `SELECT to_regclass('public.plex_sync_item_state') IS NULL AND to_regclass('public.plex_sync_item_bindings') IS NULL`).Scan(&absent); err != nil || !absent {
		t.Fatalf("requires migrated schema: %t %v", absent, err)
	}
	return tx
}

// SeedItemState inserts an account, a Plex webhook connection for it, and one
// item state row that points at mediaItemID with the given playback position.
// Every key is generated, so the rows never collide with existing data. It
// returns the connection ID.
func SeedItemState(t *testing.T, tx pgx.Tx, mediaItemID string, positionSeconds float64) string {
	t.Helper()
	suffix := uuid.NewString()
	var userID int
	if err := tx.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "webhook-sync-"+suffix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	connectionID := uuid.NewString()
	if _, err := tx.Exec(t.Context(), `INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret) VALUES($1,$2,'plex',$3)`, connectionID, userID, suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO webhook_sync_item_state(connection_id,external_user_id,external_item_id,media_item_id,last_event_at,last_position_seconds) VALUES($1,'actor','item',$2,now(),$3)`, connectionID, mediaItemID, positionSeconds); err != nil {
		t.Fatal(err)
	}
	return connectionID
}
