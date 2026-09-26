package userdb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// An existing v27 store must gain profiles.require_advisory_age, off for every
// profile it already holds, and land on the current schema version.
func TestMigrateToV28AddsProfileRequireAdvisoryAge(t *testing.T) {
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

	// Simulate an existing v27 database: a profile that already has an
	// advisory-age limit, then the column removed from under it.
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "old", Name: "Old", MaxAdvisoryAge: 10}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE profiles DROP COLUMN require_advisory_age`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 27"); err != nil {
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

	// Existing profiles keep today's lenient limit.
	old, err := store.GetProfile(ctx, "old")
	if err != nil || old == nil {
		t.Fatalf("GetProfile(old): profile=%v err=%v", old, err)
	}
	if old.MaxAdvisoryAge != 10 || old.RequireAdvisoryAge {
		t.Fatalf("migrated profile = limit %d, require %v; want limit 10, require false", old.MaxAdvisoryAge, old.RequireAdvisoryAge)
	}

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Kid", MaxAdvisoryAge: 8, RequireAdvisoryAge: true}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	got, err := store.GetProfile(ctx, "p1")
	if err != nil || got == nil {
		t.Fatalf("GetProfile(p1): profile=%v err=%v", got, err)
	}
	if !got.RequireAdvisoryAge {
		t.Fatal("RequireAdvisoryAge did not round-trip through CreateProfile")
	}
}
