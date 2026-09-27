package userdb

import (
	"database/sql"
	"slices"
	"testing"
)

// An existing v28 store loses every stored profile theme choice at every scope
// and keeps every other setting value.
func TestMigrateToV29RetiresProfileThemes(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	const now = "2026-09-26T00:00:00Z"
	for _, row := range []struct{ key, scope, profile, device, value string }{
		{"ui.theme", "profile", "p1", "", `"cinema-light"`},
		{"ui.theme", "profile_device", "p1", "d1", `"cobalt-studio"`},
		{"ui.custom_theme_vars", "profile", "p1", "", `{"--primary":"#000"}`},
		{"ui.custom_css", "profile", "p1", "", `"body{}"`},
		{"ui.theme", "profile", "p2", "", `"oxblood-noir"`},
		{"ui.text_scale", "profile", "p1", "", `"large"`},
		{"ui.high_contrast", "profile_device", "p1", "d1", `true`},
		{"playback.preferred_quality", "profile", "p2", "", `"1080p"`},
	} {
		var device any
		if row.device != "" {
			device = row.device
		}
		if _, err := db.Exec(`INSERT INTO user_setting_values (key, scope, profile_id, device_id, value, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, row.key, row.scope, row.profile, device, row.value, now, now); err != nil {
			t.Fatalf("seed %s at %s: %v", row.key, row.scope, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 28"); err != nil {
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

	rows, err := db.Query(`SELECT key FROM user_setting_values ORDER BY key`)
	if err != nil {
		t.Fatalf("query setting values: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []string{"playback.preferred_quality", "ui.high_contrast", "ui.text_scale"}
	if !slices.Equal(got, want) {
		t.Fatalf("setting keys after v29 = %v, want %v", got, want)
	}
}
