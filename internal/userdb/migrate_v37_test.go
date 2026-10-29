package userdb

import (
	"testing"
)

// v37 rewrites every stored card caption to title_metadata and bumps that
// row's revision, keeping poster_size, updated_at, and every other setting.
func TestMigrateToV37RetiresCardCaptions(t *testing.T) {
	db := newSchemaDB(t)

	const then = "2026-09-26T00:00:00Z"
	for _, row := range []struct{ key, scope, family, device, value string }{
		{"ui.card_presentation", "profile_client", "web", "", `{"poster_size":"large","caption":"artwork"}`},
		{"ui.card_presentation", "profile_device", "", "d1", `{"poster_size":"compact","caption":"title"}`},
		{"ui.card_presentation", "profile", "", "", `{"poster_size":"standard","caption":"title_metadata"}`},
		{"ui.text_scale", "profile", "", "", `"large"`},
	} {
		var family, device any
		if row.family != "" {
			family = row.family
		}
		if row.device != "" {
			device = row.device
		}
		if _, err := db.Exec(`INSERT INTO user_setting_values (key, scope, profile_id, client_family, device_id, value, created_at, updated_at)
			VALUES (?, ?, 'p1', ?, ?, ?, ?, ?)`, row.key, row.scope, family, device, row.value, then, then); err != nil {
			t.Fatalf("seed %s at %s: %v", row.key, row.scope, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 36"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if version, err := userVersion(db); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}

	want := map[string]struct {
		posterSize, caption string
		revision            int
	}{
		"profile_client": {"large", "title_metadata", 2},
		"profile_device": {"compact", "title_metadata", 2},
		"profile":        {"standard", "title_metadata", 1},
	}
	rows, err := db.Query(`SELECT scope, json_extract(value, '$.poster_size'), json_extract(value, '$.caption'), revision, updated_at
		FROM user_setting_values WHERE key = 'ui.card_presentation'`)
	if err != nil {
		t.Fatalf("query card presentation: %v", err)
	}
	defer func() { _ = rows.Close() }()
	seen := 0
	for rows.Next() {
		var scope, posterSize, caption, updatedAt string
		var revision int
		if err := rows.Scan(&scope, &posterSize, &caption, &revision, &updatedAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++
		w := want[scope]
		if posterSize != w.posterSize || caption != w.caption || revision != w.revision || updatedAt != then {
			t.Fatalf("%s row = (%s, %s, rev %d, %s), want (%s, %s, rev %d, %s)",
				scope, posterSize, caption, revision, updatedAt, w.posterSize, w.caption, w.revision, then)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if seen != len(want) {
		t.Fatalf("card presentation rows = %d, want %d", seen, len(want))
	}

	var textScale string
	var textScaleRevision int
	if err := db.QueryRow(`SELECT value, revision FROM user_setting_values WHERE key = 'ui.text_scale'`).Scan(&textScale, &textScaleRevision); err != nil {
		t.Fatalf("read text scale: %v", err)
	}
	if textScale != `"large"` || textScaleRevision != 1 {
		t.Fatalf("ui.text_scale = %s rev %d, want \"large\" rev 1", textScale, textScaleRevision)
	}
}
