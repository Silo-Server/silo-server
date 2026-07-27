package userdb

import (
	"database/sql"
	"strings"
	"testing"
)

// TestSQLiteSettingValueConstraints pins the schema's own guarantees rather than
// the Go validation in front of them. SettingIdentity.Validate normally keeps a
// malformed row from reaching SQL, so nothing else here would notice if the
// CHECK constraints or the partial unique indexes were dropped — and the
// one-time migration will write these rows in bulk, without going through the
// per-request path.
func TestSQLiteSettingValueConstraints(t *testing.T) {
	db := newSchemaDB(t)

	rejected := []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "unknown scope",
			sql:  insertSettingValueSQL,
			args: []any{"playback.audio_language", "wishful", nil, nil, nil, nil, `"en"`},
		},
		{
			name: "account scope carrying a profile",
			sql:  insertSettingValueSQL,
			args: []any{"playback.audio_language", "account", "p1", nil, nil, nil, `"en"`},
		},
		{
			name: "profile scope without a profile",
			sql:  insertSettingValueSQL,
			args: []any{"playback.audio_language", "profile", nil, nil, nil, nil, `"en"`},
		},
		{
			name: "device scope without a device",
			sql:  insertSettingValueSQL,
			args: []any{"playback.audio_language", "profile_device", "p1", nil, nil, nil, `"en"`},
		},
		{
			name: "library scope carrying a series",
			sql:  insertSettingValueSQL,
			args: []any{"playback.audio_language", "profile_library", "p1", nil, 42, "s-1", `"en"`},
		},
		{
			name: "series scope without a series",
			sql:  insertSettingValueSQL,
			args: []any{"playback.audio_language", "profile_series", "p1", nil, nil, nil, `"en"`},
		},
		{
			name: "value is not JSON",
			sql:  insertSettingValueSQL,
			args: []any{"playback.audio_language", "profile", "p1", nil, nil, nil, `en`},
		},
	}

	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(tc.sql, tc.args...); err == nil {
				t.Fatalf("insert was accepted; the table must reject %s", tc.name)
			} else if !strings.Contains(err.Error(), "CHECK constraint failed") {
				t.Fatalf("insert failed with %v, want a CHECK constraint violation", err)
			}
		})
	}
}

// TestSQLiteSettingValuePartialUniqueIndexes pins that each partial unique index
// exists and applies only to its own scope: a duplicate identity is refused, and
// the same key at a neighboring identity is not.
func TestSQLiteSettingValuePartialUniqueIndexes(t *testing.T) {
	const key = "playback.audio_language"

	duplicates := []struct {
		name     string
		row      []any
		neighbor []any
	}{
		{
			name:     "account",
			row:      []any{key, "account", nil, nil, nil, nil, `"en"`},
			neighbor: []any{key + ".other", "account", nil, nil, nil, nil, `"en"`},
		},
		{
			name:     "profile",
			row:      []any{key, "profile", "p1", nil, nil, nil, `"en"`},
			neighbor: []any{key, "profile", "p2", nil, nil, nil, `"en"`},
		},
		{
			name:     "profile_device",
			row:      []any{key, "profile_device", "p1", "apple-tv", nil, nil, `"en"`},
			neighbor: []any{key, "profile_device", "p1", "iphone", nil, nil, `"en"`},
		},
		{
			name:     "profile_library",
			row:      []any{key, "profile_library", "p1", nil, 42, nil, `"en"`},
			neighbor: []any{key, "profile_library", "p1", nil, 43, nil, `"en"`},
		},
		{
			name:     "profile_series",
			row:      []any{key, "profile_series", "p1", nil, nil, "s-1", `"en"`},
			neighbor: []any{key, "profile_series", "p1", nil, nil, "s-2", `"en"`},
		},
	}

	for _, tc := range duplicates {
		t.Run(tc.name, func(t *testing.T) {
			db := newSchemaDB(t)
			if _, err := db.Exec(insertSettingValueSQL, tc.row...); err != nil {
				t.Fatalf("first insert: %v", err)
			}
			if _, err := db.Exec(insertSettingValueSQL, tc.row...); err == nil {
				t.Fatal("duplicate identity was accepted; the partial unique index is missing")
			} else if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
				t.Fatalf("duplicate insert failed with %v, want a UNIQUE constraint violation", err)
			}
			if _, err := db.Exec(insertSettingValueSQL, tc.neighbor...); err != nil {
				t.Fatalf("neighboring identity was refused: %v", err)
			}
		})
	}
}

// insertSettingValueSQL writes a row without going through the store, so the
// schema is the only thing standing between the test and the table.
const insertSettingValueSQL = `
	INSERT INTO user_setting_values
		(key, scope, profile_id, device_id, library_id, series_id, value, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, '2026-07-27T00:00:00Z', '2026-07-27T00:00:00Z')`

func newSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}
