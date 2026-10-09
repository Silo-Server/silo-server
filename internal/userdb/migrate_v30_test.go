package userdb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// An existing v29 store must gain profiles.pin_revision at 0 for every profile
// it already holds, land on the current schema version, and advance the
// revision on the next PIN change.
func TestMigrateToV30AddsProfilePINRevision(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	store := NewSQLiteUserStore(db)
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "old", Name: "Old"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	// Rebuild a v29 file: no trigger, no column.
	for _, stmt := range []string{
		`DROP TRIGGER profiles_pin_revision`,
		`ALTER TABLE profiles DROP COLUMN pin_revision`,
		"PRAGMA user_version = 29",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// Opening a store initializes the schema, migrates the column, and then
	// installs the trigger.
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema on v29: %v", err)
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

	old, err := store.GetProfile(ctx, "old")
	if err != nil || old == nil {
		t.Fatalf("GetProfile(old): profile=%v err=%v", old, err)
	}
	if old.PINRevision != 0 {
		t.Fatalf("migrated PINRevision = %d, want 0", old.PINRevision)
	}
	pin := "1234"
	if err := store.UpdateProfile(ctx, "old", userstore.UpdateProfileInput{PIN: &pin}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if got, _ := store.GetProfile(ctx, "old"); got == nil || got.PINRevision != 1 {
		t.Fatalf("PINRevision after PIN set = %v, want 1", got)
	}
	if _, err := db.Exec(`UPDATE profiles SET pin_hash = ? WHERE id = ?`, "other-hash", "old"); err != nil {
		t.Fatalf("pin_hash-only write: %v", err)
	}
	if got, _ := store.GetProfile(ctx, "old"); got == nil || got.PINRevision != 2 {
		t.Fatalf("PINRevision after pin_hash-only write = %v, want 2", got)
	}
}

// A process from the previous release can still hold a connection it opened
// before another process migrated the file to v30. Its PIN write names only
// pin_hash, and the profiles_pin_revision trigger must advance the revision
// anyway, or the new release keeps accepting tokens for the replaced PIN.
func TestPINRevisionAdvancesOnPINHashOnlyWrite(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	store := NewSQLiteUserStore(db)
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p", Name: "P"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	for i, write := range []struct {
		hash string
		want int64
	}{
		{"hash-a", 1},
		{"hash-a", 1}, // unchanged hash: no advance
		{"hash-b", 2},
		{"", 3},
	} {
		// The previous release's updateProfile statement shape.
		if _, err := db.Exec(`UPDATE profiles SET pin_hash = ?, updated_at = ? WHERE id = ?`, write.hash, "2026-01-01T00:00:00Z", "p"); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		got, err := store.GetProfile(ctx, "p")
		if err != nil || got == nil {
			t.Fatalf("write %d: GetProfile: profile=%v err=%v", i, got, err)
		}
		if got.PINRevision != write.want {
			t.Fatalf("write %d (%q): PINRevision = %d, want %d", i, write.hash, got.PINRevision, write.want)
		}
	}
}

func TestPINRevisionTriggerAllowsOlderSchemaRenames(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
DROP TRIGGER profiles_pin_revision;
ALTER TABLE profiles DROP COLUMN pin_revision;
PRAGMA user_version = 19;
`); err != nil {
		t.Fatal(err)
	}
	// Opening a pre-v30 file must not install a trigger that references a
	// missing column before v20 renames collection_sort_preferences.
	if err := InitSchema(db); err != nil {
		t.Fatalf("initialize v19 store: %v", err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("upgrade v19 store: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, name, created_at, updated_at) VALUES ('old', 'Old', '', '');
UPDATE profiles SET pin_hash = 'new-hash' WHERE id = 'old';`); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.QueryRow(`SELECT pin_revision FROM profiles WHERE id = 'old'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("PIN revision = %d, want 1", revision)
	}
}

func TestCurrentSchemaInstallsMissingPINRevisionTrigger(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER profiles_pin_revision`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'profiles_pin_revision'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("PIN revision triggers = %d, want 1", count)
	}
}
