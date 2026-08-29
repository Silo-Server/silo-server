package userdb

import (
	"database/sql"
	"testing"
)

func TestMigrateToV20AllowsPersonalSortPreferenceKinds(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if _, err := db.Exec(`
DROP TABLE collection_sort_preferences;
CREATE TABLE collection_sort_preferences (
    profile_id TEXT NOT NULL,
    collection_kind TEXT NOT NULL,
    collection_id TEXT NOT NULL,
    sort_field TEXT NOT NULL DEFAULT '',
    sort_order TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, collection_kind, collection_id),
    CHECK (collection_kind IN ('library', 'user')),
    CHECK (sort_order IN ('', 'asc', 'desc'))
);
INSERT INTO collection_sort_preferences VALUES ('profile-1', 'library', 'collection-1', 'title', 'asc', '2026-08-26T00:00:00Z');
PRAGMA user_version = 19;
`); err != nil {
		t.Fatalf("seed v19 schema: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO collection_sort_preferences VALUES ('profile-1', 'watchlist', 'personal', 'added_at', 'desc', '2026-08-26T00:00:01Z');
INSERT INTO collection_sort_preferences VALUES ('profile-1', 'favorites', 'personal', 'title', 'asc', '2026-08-26T00:00:02Z');
`); err != nil {
		t.Fatalf("insert personal preferences after migration: %v", err)
	}
	var field string
	if err := db.QueryRow(`
SELECT sort_field FROM collection_sort_preferences
WHERE profile_id = 'profile-1' AND collection_kind = 'library' AND collection_id = 'collection-1'
`).Scan(&field); err != nil {
		t.Fatalf("read preserved preference: %v", err)
	}
	if field != "title" {
		t.Fatalf("preserved sort_field = %q, want title", field)
	}
}
