package userdb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// An existing v26 store must gain profiles.max_advisory_age, with its range
// check, and land on the current schema version.
func TestMigrateToV27AddsProfileAdvisoryAgeLimit(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	// Simulate an existing v26 database that predates the column.
	if _, err := db.Exec(`ALTER TABLE profiles DROP COLUMN max_advisory_age`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 26"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	version, err := userVersion(db)
	if err != nil {
		t.Fatalf("userVersion: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	store := NewSQLiteUserStore(db)
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Kid", MaxAdvisoryAge: 10}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	got, err := store.GetProfile(ctx, "p1")
	if err != nil || got == nil {
		t.Fatalf("GetProfile: profile=%v err=%v", got, err)
	}
	if got.MaxAdvisoryAge != 10 {
		t.Fatalf("MaxAdvisoryAge = %d, want 10", got.MaxAdvisoryAge)
	}

	if _, err := db.Exec(`UPDATE profiles SET max_advisory_age = 30 WHERE id = 'p1'`); err == nil {
		t.Fatal("an advisory-age limit outside 1..21 was stored")
	}
}
