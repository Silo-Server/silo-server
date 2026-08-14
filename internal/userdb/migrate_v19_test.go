package userdb

import (
	"context"
	"database/sql"
	"testing"
)

const userDevicesSchemaV18 = `
CREATE TABLE user_devices (
    profile_id TEXT NOT NULL,
    device_id TEXT NOT NULL,
    device_name TEXT NOT NULL DEFAULT '',
    device_platform TEXT NOT NULL DEFAULT '',
    last_seen_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, device_id)
);`

func TestMigrateToV19AddsCustomNameKeepingRows(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("initial InitSchema: %v", err)
	}

	// Replace only the devices table with its released v18 shape. On a real
	// open, InitSchema sees exactly this table before runMigrations.
	if _, err := db.Exec("DROP TABLE user_devices"); err != nil {
		t.Fatalf("drop current devices table: %v", err)
	}
	if _, err := db.Exec(userDevicesSchemaV18); err != nil {
		t.Fatalf("create v18 devices table: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 18"); err != nil {
		t.Fatalf("set v18: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO user_devices (profile_id, device_id, device_name, device_platform, last_seen_at)
VALUES ('p1', 'apple-tv', 'Apple TV', 'tvOS', '2026-08-01T12:00:00Z')`); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// The existing row survives with no custom name, and is renameable
	// through the migrated column.
	devices, err := ListDevices(db)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 1 || devices[0].DeviceName != "Apple TV" || devices[0].CustomName != "" {
		t.Fatalf("migrated devices = %+v, want the seeded row with an empty custom name", devices)
	}
	store := NewSQLiteUserStore(db)
	if err := store.RenameDevice(context.Background(), "p1", "apple-tv", "Bedroom TV"); err != nil {
		t.Fatalf("RenameDevice after migration: %v", err)
	}
	devices, err = ListDevices(db)
	if err != nil {
		t.Fatalf("ListDevices after rename: %v", err)
	}
	if len(devices) != 1 || devices[0].CustomName != "Bedroom TV" {
		t.Fatalf("renamed devices = %+v, want custom name %q", devices, "Bedroom TV")
	}
}
