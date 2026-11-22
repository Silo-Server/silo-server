package userdb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// An existing v37 store gains profiles.requests_blocked, off for every
// profile it already holds.
func TestMigrateToV38AddsProfileRequestsBlocked(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	store := NewSQLiteUserStore(db)
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "old", Name: "Old"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE profiles DROP COLUMN requests_blocked`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 37"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if version, err := userVersion(db); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}
	old, err := store.GetProfile(ctx, "old")
	if err != nil || old == nil || old.RequestsBlocked {
		t.Fatalf("migrated profile = %+v, %v; want not blocked", old, err)
	}
}
