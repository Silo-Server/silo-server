package userdb

import (
	"testing"
)

func TestSQLiteCollectionContinuationLegacyNullMigration(t *testing.T) {
	s := newConformanceStore(t).(*SQLiteUserStore)
	if _, err := s.db.Exec(`DROP TRIGGER personal_collection_items_position_insert;
 INSERT INTO personal_collection_items VALUES ('legacy', 'item', NULL, '2026-01-01');
 PRAGMA user_version=21;`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(s.db); err != nil {
		t.Fatal(err)
	}
	var position int
	if err := s.db.QueryRow(`SELECT position FROM personal_collection_items WHERE collection_id='legacy'`).Scan(&position); err != nil || position != 0 {
		t.Fatalf("legacy NULL position: %d %v", position, err)
	}
	if version, err := userVersion(s.db); err != nil || version != schemaVersion {
		t.Fatalf("version: %d %v", version, err)
	}
}
