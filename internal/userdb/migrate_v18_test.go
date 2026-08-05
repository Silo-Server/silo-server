package userdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const settingValuesSchemaV17 = `
CREATE TABLE user_setting_values (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT NOT NULL,
    scope       TEXT NOT NULL,
    profile_id  TEXT,
    device_id   TEXT,
    library_id  INTEGER,
    series_id   TEXT,
    value       TEXT NOT NULL CHECK (json_valid(value)),
    revision    INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    CHECK (scope IN ('account', 'profile', 'profile_device', 'profile_library', 'profile_series')),
    CHECK (
      (scope = 'account' AND profile_id IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_device' AND profile_id IS NOT NULL AND device_id IS NOT NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_library' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NOT NULL AND series_id IS NULL) OR
      (scope = 'profile_series' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NOT NULL)
    )
);`

func TestMigrateToV18AddsClientFamilyAndSeedsShortcuts(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("initial InitSchema: %v", err)
	}

	// Replace only the canonical values table with its released v17 shape. On a
	// real open, InitSchema sees exactly this table before runMigrations.
	for _, index := range []string{
		"user_setting_values_account_uq", "user_setting_values_profile_uq",
		"user_setting_values_profile_client_uq", "user_setting_values_profile_device_uq",
		"user_setting_values_profile_library_uq", "user_setting_values_profile_series_uq",
		"user_setting_values_resolution_idx", "user_setting_values_series_idx",
		"user_setting_values_library_idx",
	} {
		if _, err := db.Exec("DROP INDEX IF EXISTS " + index); err != nil {
			t.Fatalf("drop %s: %v", index, err)
		}
	}
	if _, err := db.Exec("DROP TABLE user_setting_values"); err != nil {
		t.Fatalf("drop current values table: %v", err)
	}
	if _, err := db.Exec(settingValuesSchemaV17); err != nil {
		t.Fatalf("create v17 values table: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 17"); err != nil {
		t.Fatalf("set v17: %v", err)
	}

	const created = "2026-07-28T10:00:00Z"
	const updated = "2026-08-01T12:00:00Z"
	legacy := `{"12":[{"type":"collection","id":"classics","label":"Classics"}],"7":[{"type":"section","id":"recently-added","label":"Recently Added"},{"type":"section","id":"   ","label":"Whitespace ID"},{"type":"collection","id":"horror","label":"Horror"}],"favorites":[{"type":"collection","id":"keep-me","label":"Legacy group"}]}`
	encodedLegacy, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("encode legacy JSON string: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO user_setting_values
    (key, scope, profile_id, value, revision, created_at, updated_at)
VALUES ('ui.sidebar_pins', 'profile', 'p1', ?, 4, ?, ?),
       ('ui.sidebar_pins', 'profile', 'p2', ?, 2, ?, ?),
       ('ui.sidebar_pins', 'profile', 'p3', ?, 5, ?, ?),
       ('nav.shortcuts', 'profile', 'p2', '{"items":[]}', 3, ?, ?)`,
		legacy, created, updated,
		legacy, created, updated,
		string(encodedLegacy), created, updated,
		created, updated,
	); err != nil {
		t.Fatalf("seed v17 rows: %v", err)
	}

	if err := InitSchema(db); err != nil {
		t.Fatalf("upgrade InitSchema: %v", err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	version, err := userVersion(db)
	if err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d (%v), want %d", version, err, schemaVersion)
	}

	var columnCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('user_setting_values')
        WHERE name = 'client_family'`).Scan(&columnCount); err != nil || columnCount != 1 {
		t.Fatalf("client_family column count = %d (%v), want 1", columnCount, err)
	}

	// The legacy row is preserved byte-for-byte and keeps its revision.
	var kept string
	var revision int
	if err := db.QueryRow(`SELECT value, revision FROM user_setting_values
        WHERE key = 'ui.sidebar_pins' AND profile_id = 'p1'`).Scan(&kept, &revision); err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if kept != legacy || revision != 4 {
		t.Fatalf("legacy row changed to %s at revision %d", kept, revision)
	}

	var migrated string
	if err := db.QueryRow(`SELECT value FROM user_setting_values
        WHERE key = 'nav.shortcuts' AND profile_id = 'p1'`).Scan(&migrated); err != nil {
		t.Fatalf("read migrated shortcuts: %v", err)
	}
	assertJSONValueEqual(t, migrated, `{"items":[{"label":"Recently Added","library_id":7,"section_id":"recently-added","type":"section"},{"collection_id":"horror","label":"Horror","library_id":7,"type":"collection"},{"collection_id":"classics","label":"Classics","library_id":12,"type":"collection"}]}`)

	// Legacy web builds also wrote the grouped object as one JSON string. It is
	// preserved as authored while seeding the same canonical shortcut document.
	if err := db.QueryRow(`SELECT value, revision FROM user_setting_values
        WHERE key = 'ui.sidebar_pins' AND profile_id = 'p3'`).Scan(&kept, &revision); err != nil {
		t.Fatalf("read JSON-string legacy row: %v", err)
	}
	if kept != string(encodedLegacy) || revision != 5 {
		t.Fatalf("JSON-string legacy row changed to %s at revision %d", kept, revision)
	}
	if err := db.QueryRow(`SELECT value FROM user_setting_values
        WHERE key = 'nav.shortcuts' AND profile_id = 'p3'`).Scan(&migrated); err != nil {
		t.Fatalf("read shortcuts migrated from JSON string: %v", err)
	}
	assertJSONValueEqual(t, migrated, `{"items":[{"label":"Recently Added","library_id":7,"section_id":"recently-added","type":"section"},{"collection_id":"horror","label":"Horror","library_id":7,"type":"collection"},{"collection_id":"classics","label":"Classics","library_id":12,"type":"collection"}]}`)

	// Existing new-format data wins rather than being overwritten by legacy.
	if err := db.QueryRow(`SELECT value, revision FROM user_setting_values
        WHERE key = 'nav.shortcuts' AND profile_id = 'p2'`).Scan(&migrated, &revision); err != nil {
		t.Fatalf("read pre-existing shortcuts: %v", err)
	}
	assertJSONValueEqual(t, migrated, `{"items":[]}`)
	if revision != 3 {
		t.Fatalf("pre-existing shortcut revision = %d, want 3", revision)
	}

	store := NewSQLiteUserStore(db)
	id := userstore.SettingIdentity{
		Key: "nav.primary_menu", Scope: settingscontract.ScopeProfileClient,
		ProfileID: "p1", ClientFamily: settingscontract.ClientFamilyTV,
	}
	if _, err := store.UpsertSettingValue(context.Background(), id,
		json.RawMessage(`{"items":[{"type":"builtin","destination":"home"}]}`)); err != nil {
		t.Fatalf("write profile_client value: %v", err)
	}
	got, err := store.GetSettingValue(context.Background(), id)
	if err != nil || got == nil || got.ClientFamily != settingscontract.ClientFamilyTV {
		t.Fatalf("profile_client round trip = %+v (%v)", got, err)
	}
}

func TestNavigationShortcutsFromSidebarPinsSkipsMalformedSiblingsAndBoundsLibraryIDs(t *testing.T) {
	raw := `{
      "7":[
        {"type":"section","id":"recent","label":"Recent"},
        {"type":"section","id":99,"label":"Wrong ID type"},
        {"type":"collection","id":"horror","label":"Horror"},
        {"type":"collection","id":"bad-label","label":42}
      ],
      "8":{"type":"collection","id":"not-an-array","label":"Skipped group"},
      "9":[null,{"type":"collection","id":"drama","label":"Drama"}],
      "2147483647":[{"type":"section","id":"boundary","label":"Boundary"}],
      "2147483648":[{"type":"section","id":"overflow","label":"Overflow"}],
      "999999999999999999999999999999":[{"type":"section","id":"huge","label":"Huge"}],
      "07":[{"type":"section","id":"leading-zero","label":"Leading Zero"}]
    }`

	value, ok := navigationShortcutsFromSidebarPins(raw)
	if !ok {
		t.Fatal("migration rejected valid siblings because malformed groups/items were present")
	}
	assertJSONValueEqual(t, string(value), `{"items":[
      {"type":"section","library_id":7,"section_id":"recent","label":"Recent"},
      {"type":"collection","library_id":7,"collection_id":"horror","label":"Horror"},
      {"type":"collection","library_id":9,"collection_id":"drama","label":"Drama"},
      {"type":"section","library_id":2147483647,"section_id":"boundary","label":"Boundary"}
    ]}`)
}

func TestNavigationShortcutsFromSidebarPinsRejectsOnlyOverflowLibraryIDs(t *testing.T) {
	for name, raw := range map[string]string{
		"one over int32": `{"2147483648":[{"type":"section","id":"overflow","label":"Overflow"}]}`,
		"parse overflow": `{"999999999999999999999999999999":[{"type":"section","id":"huge","label":"Huge"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if value, ok := navigationShortcutsFromSidebarPins(raw); ok || value != nil {
				t.Fatalf("overflow-only pins migrated as %s", value)
			}
		})
	}
}

func assertJSONValueEqual(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}
