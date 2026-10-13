package userdb

import (
	"database/sql"
	"maps"
	"testing"
)

// An existing v30 store keeps a collection shared only when its allow list
// covers every profile; a collection shared with some profiles, or with
// nobody but its creator, becomes private (#1615).
func TestMigrateToV31AppliesLoginSharingRule(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		if err := CreateProfile(db, Profile{ID: id, Name: id, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
			t.Fatalf("CreateProfile(%s): %v", id, err)
		}
	}
	const now = "2026-09-26T00:00:00Z"
	for _, c := range []struct {
		id       string
		shared   bool
		audience []string
	}{
		{"all", true, []string{"p1", "p2", "p3"}},
		{"stale", true, []string{"p1", "p2", "p3", "gone"}},
		{"subset", true, []string{"p1", "p2"}},
		{"creator_only", true, []string{"p1"}},
		{"implied", true, []string{"p2", "p3"}},
		{"private", false, []string{"p1"}},
	} {
		if _, err := db.Exec(`INSERT INTO personal_collections (id, profile_id, creator_profile_id, name, is_shared, created_at, updated_at)
			VALUES (?, 'p1', 'p1', ?, ?, ?, ?)`, c.id, c.id, c.shared, now, now); err != nil {
			t.Fatalf("seed collection %s: %v", c.id, err)
		}
		for _, profileID := range c.audience {
			if _, err := db.Exec(`INSERT INTO personal_collection_profiles (collection_id, profile_id) VALUES (?, ?)`, c.id, profileID); err != nil {
				t.Fatalf("seed allow list %s/%s: %v", c.id, profileID, err)
			}
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 30"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if version, err := userVersion(db); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}

	rows, err := db.Query(`SELECT id, is_shared FROM personal_collections`)
	if err != nil {
		t.Fatalf("query collections: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]bool{}
	for rows.Next() {
		var id string
		var shared bool
		if err := rows.Scan(&id, &shared); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = shared
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := map[string]bool{"all": true, "stale": true, "implied": true, "subset": false, "creator_only": false, "private": false}
	if !maps.Equal(got, want) {
		t.Fatalf("is_shared after v31 = %v, want %v", got, want)
	}
}

// A store a pre-merge collections revamp build opened is at v30 with login
// sharing already applied and without main's v30 pin_revision column. It gains
// the column and keeps collections shared since then, which have no allow list.
func TestMigrateRevampV30StoreAddsPINRevisionAndKeepsSharing(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	for _, id := range []string{"p1", "p2"} {
		if err := CreateProfile(db, Profile{ID: id, Name: id, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
			t.Fatalf("CreateProfile(%s): %v", id, err)
		}
	}
	const now = "2026-10-05T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO personal_collections (id, profile_id, creator_profile_id, name, is_shared, created_at, updated_at)
		VALUES ('shared', 'p1', 'p1', 'shared', 1, ?, ?)`, now, now); err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	for _, stmt := range []string{
		`DROP TRIGGER profiles_pin_revision`,
		`ALTER TABLE profiles DROP COLUMN pin_revision`,
		"PRAGMA user_version = 30",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema on the revamp's v30: %v", err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if version, err := userVersion(db); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}
	var revision int
	if err := db.QueryRow(`SELECT pin_revision FROM profiles WHERE id = 'p1'`).Scan(&revision); err != nil {
		t.Fatalf("read pin_revision: %v", err)
	}
	var shared bool
	if err := db.QueryRow(`SELECT is_shared FROM personal_collections WHERE id = 'shared'`).Scan(&shared); err != nil {
		t.Fatalf("read is_shared: %v", err)
	}
	if !shared {
		t.Fatal("a collection shared after the revamp's v30 became private")
	}
}
