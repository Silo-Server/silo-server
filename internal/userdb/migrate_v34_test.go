package userdb

import (
	"database/sql"
	"slices"
	"testing"
)

// v34 deletes the Next Up rows profiles added, by user_section_type or the
// legacy section_type, and leaves every other override, including an
// override of an admin row, as it was.
func TestMigrateToV34DeletesAddedNextUpRows(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO profile_section_overrides (id, profile_id, scope, library_id, section_id, hidden, section_type, is_user_added, user_section_type, created_at, updated_at) VALUES
    ('added', 'p1', 'home', NULL, NULL, 0, NULL, 1, 'next_up', 'now', 'now'),
    ('added-library', 'p2', 'library', '1', '', 0, NULL, 1, 'next_up', 'now', 'now'),
    ('legacy', 'p1', 'home', NULL, NULL, 0, 'next_up', 1, '', 'now', 'now'),
    ('admin-row', 'p1', 'home', NULL, 'admin-1', 1, 'next_up', 0, NULL, 'now', 'now'),
    ('gems', 'p1', 'home', NULL, NULL, 0, NULL, 1, 'hidden_gems', 'now', 'now'),
    ('continue', 'p1', 'home', NULL, 'admin-2', 0, NULL, 0, NULL, 'now', 'now');
PRAGMA user_version = 33;`); err != nil {
		t.Fatalf("seed v33 store: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if version, err := userVersion(db); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}
	rows, err := db.Query(`SELECT id FROM profile_section_overrides ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"admin-row", "continue", "gems"}; !slices.Equal(ids, want) {
		t.Fatalf("overrides left = %v, want %v", ids, want)
	}
}
