package userdb

import (
	"database/sql"
	"testing"
)

// TestMigrateToV26MaterializesRetiredSettingsFallbacks is the SQLite half of
// TestPostgresRetiredSettingsFallbacks: every profile without a canonical row
// gets the value the legacy fallback returned, stored rows win, and the legacy
// rows stay.
func TestMigrateToV26MaterializesRetiredSettingsFallbacks(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO profiles (id, name, created_at, updated_at) VALUES
    ('p-new', 'New', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z'),
    ('p-stored', 'Stored', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z');
INSERT INTO user_settings (key, value) VALUES
    ('disabled_library_ids', '[0,4,4,7]'),
    ('next_up_mode', 'separate');
INSERT INTO user_setting_values (key, scope, profile_id, value, revision, created_at, updated_at) VALUES
    ('ui.disabled_library_ids', 'profile', 'p-stored', '[9]', 3, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z');
PRAGMA user_version = 25;
`); err != nil {
		t.Fatalf("seed v25 store: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	canonical := func(profileID, key string) string {
		t.Helper()
		var value string
		err := db.QueryRow(`
SELECT value FROM user_setting_values WHERE scope = 'profile' AND profile_id = ? AND key = ?`,
			profileID, key).Scan(&value)
		if err != nil {
			return ""
		}
		return value
	}
	for _, check := range []struct {
		profileID, key, want string
	}{
		{"p-new", "ui.disabled_library_ids", `[4,7]`},
		{"p-new", "ui.next_up_mode", `"separate"`},
		{"p-stored", "ui.disabled_library_ids", `[9]`},
		{"p-stored", "ui.next_up_mode", `"separate"`},
	} {
		if got := canonical(check.profileID, check.key); got != check.want {
			t.Errorf("%s %s = %q, want %q", check.profileID, check.key, got, check.want)
		}
	}

	var legacy int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_settings`).Scan(&legacy); err != nil || legacy != 2 {
		t.Errorf("legacy rows = %d (%v), want both left in place", legacy, err)
	}
	if version, err := userVersion(db); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d (%v), want %d", version, err, schemaVersion)
	}
}
