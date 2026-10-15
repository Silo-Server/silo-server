package userdb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A v31 store's user_devices table has no custom_name column; v32 adds it and
// keeps the registered devices, which can then be renamed.
func TestMigrateToV32AddsDeviceCustomName(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE user_devices;
CREATE TABLE user_devices (
    profile_id TEXT NOT NULL,
    device_id TEXT NOT NULL,
    device_name TEXT NOT NULL DEFAULT '',
    device_platform TEXT NOT NULL DEFAULT '',
    last_seen_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, device_id)
);
INSERT INTO user_devices (profile_id, device_id, device_name, last_seen_at) VALUES ('p1', 'tv', 'Apple TV', '2026-10-01T00:00:00Z');
PRAGMA user_version = 31;`); err != nil {
		t.Fatalf("seed v31 store: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if version, err := userVersion(db); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}
	store := NewSQLiteUserStore(db)
	if err := store.RenameDevice(context.Background(), "p1", "tv", "Living Room"); err != nil {
		t.Fatalf("RenameDevice after migration: %v", err)
	}
	page, err := store.ListDeviceSettingsPage(context.Background(), userstore.DevicePageOptions{Limit: 10})
	if err != nil || len(page) != 1 || page[0].DeviceName != "Apple TV" || page[0].CustomName != "Living Room" {
		t.Fatalf("migrated device = %+v, %v", page, err)
	}
}
