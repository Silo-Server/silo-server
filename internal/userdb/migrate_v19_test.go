package userdb

import (
	"database/sql"
	"testing"
)

// An existing v18 store from main must gain the collection sort preferences
// table without replaying or replacing any of main's earlier migrations.
func TestMigrateToV19AddsCollectionSortPreferences(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	if _, err := db.Exec(`DROP TABLE IF EXISTS collection_sort_preferences`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 18"); err != nil {
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

	var tableName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'collection_sort_preferences'`).Scan(&tableName); err != nil {
		t.Fatalf("collection_sort_preferences table: %v", err)
	}
}
