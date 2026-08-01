package userdb

import (
	"database/sql"
	"testing"
)

// seedLegacyDisplayPrefs writes the user_settings rows a pre-cutover install
// holds: handler-written DisplayPreferences blobs, a real user setting that
// must not move, and one jellycompat row only the legacy settings API's
// removed unknown-key carve-out could have produced.
func seedLegacyDisplayPrefs(t *testing.T, db *sql.DB) {
	t.Helper()
	for key, value := range map[string]string{
		// Verbatim copy matters: unusual spacing and key order must survive.
		"jellycompat:displayprefs:usersettings:emby": `{"SortBy":"SortName",  "CustomPrefs":{"b":"2","a":"1"}}`,
		"jellycompat:displayprefs:f137a2dd:":         `{"SortBy":"DateCreated"}`,
		"jellycompat:stray":                          "not a displayprefs blob",
		"ui_theme":                                   "cobalt-studio",
	} {
		if _, err := db.Exec(
			`INSERT INTO user_settings (key, value) VALUES (?, ?)`, key, value); err != nil {
			t.Fatalf("seeding user_settings %s: %v", key, err)
		}
	}
}

func runDisplayPrefsMove(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrateToV17(tx); err != nil {
		t.Fatalf("migrateToV17: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestMigrateToV17MovesDisplayPrefs runs the real migration against a real
// database. The parsing rules are unit-tested in
// internal/jellycompat/displayprefs; what this covers is the wiring — blobs
// land verbatim in the dedicated table, user_settings comes out with no
// jellycompat tenants, and the one unparseable row is recorded rather than
// silently deleted.
func TestMigrateToV17MovesDisplayPrefs(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	seedLegacyDisplayPrefs(t, db)
	runDisplayPrefsMove(t, db)

	t.Run("blobs move verbatim", func(t *testing.T) {
		for _, want := range []struct{ prefsID, client, value string }{
			{"usersettings", "emby", `{"SortBy":"SortName",  "CustomPrefs":{"b":"2","a":"1"}}`},
			{"f137a2dd", "", `{"SortBy":"DateCreated"}`},
		} {
			got, err := GetJellycompatDisplayPrefs(db, want.prefsID, want.client)
			if err != nil {
				t.Fatalf("reading %s/%s: %v", want.prefsID, want.client, err)
			}
			if got != want.value {
				t.Errorf("%s/%s = %q, want the blob byte-for-byte", want.prefsID, want.client, got)
			}
		}
	})

	t.Run("user_settings keeps no jellycompat tenants", func(t *testing.T) {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM user_settings WHERE key LIKE 'jellycompat:%'`).Scan(&count); err != nil {
			t.Fatalf("counting: %v", err)
		}
		if count != 0 {
			t.Errorf("%d jellycompat rows still ride user_settings", count)
		}
		// The real settings stay put.
		if theme, err := GetSetting(db, "ui_theme"); err != nil || theme != "cobalt-studio" {
			t.Errorf("ui_theme = (%q, %v); the move touched a non-jellycompat row", theme, err)
		}
	})

	t.Run("unparseable rows are recorded, not silently deleted", func(t *testing.T) {
		var value, reason string
		err := db.QueryRow(`
SELECT value, reason FROM user_setting_migration_rejects
 WHERE source_table = 'user_settings' AND source_key = 'jellycompat:stray'`).
			Scan(&value, &reason)
		if err != nil {
			t.Fatalf("the stray row was dropped rather than recorded: %v", err)
		}
		if value != "not a displayprefs blob" || reason == "" {
			t.Errorf("reject = (%q, %q); the original value and a reason must survive", value, reason)
		}
	})

	t.Run("a second run is a no-op", func(t *testing.T) {
		countAll := func() (blobs, rejects int) {
			t.Helper()
			if err := db.QueryRow(`SELECT COUNT(*) FROM jellycompat_displayprefs`).Scan(&blobs); err != nil {
				t.Fatalf("counting blobs: %v", err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM user_setting_migration_rejects`).Scan(&rejects); err != nil {
				t.Fatalf("counting rejects: %v", err)
			}
			return blobs, rejects
		}
		blobsBefore, rejectsBefore := countAll()

		runDisplayPrefsMove(t, db)

		blobsAfter, rejectsAfter := countAll()
		if blobsAfter != blobsBefore || rejectsAfter != rejectsBefore {
			t.Errorf("re-run changed counts: blobs %d→%d, rejects %d→%d",
				blobsBefore, blobsAfter, rejectsBefore, rejectsAfter)
		}
	})
}

// TestMigrateToV17IsAtomic: the move runs inside the caller's transaction, so
// a rollback must leave the legacy rows exactly where they were.
func TestMigrateToV17IsAtomic(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	seedLegacyDisplayPrefs(t, db)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrateToV17(tx); err != nil {
		t.Fatalf("migrateToV17: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var legacy, moved int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM user_settings WHERE key LIKE 'jellycompat:%'`).Scan(&legacy); err != nil {
		t.Fatalf("counting legacy rows: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM jellycompat_displayprefs`).Scan(&moved); err != nil {
		t.Fatalf("counting moved rows: %v", err)
	}
	if legacy != 3 || moved != 0 {
		t.Errorf("rollback left %d legacy rows (want 3) and %d moved rows (want 0)", legacy, moved)
	}
}
