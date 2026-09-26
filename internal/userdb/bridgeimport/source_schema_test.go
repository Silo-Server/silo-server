package bridgeimport

import (
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userdb"

	"github.com/Silo-Server/silo-server/internal/userdb/bridgeimport/testdata"
)

func TestImportSchema22AccountsForEveryColumn(t *testing.T) {
	source, err := testdata.NewSource(filepath.Join(t.TempDir(), "7.db"), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close() //nolint:errcheck
	tx, err := source.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := validateSourceSchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if len(sourceColumns) != 29 || len(Manifest()) != 29 {
		t.Fatal("complete mapping changed")
	}
	for _, mapping := range Manifest() {
		if len(sourceColumns[mapping.Source]) == 0 {
			t.Fatalf("no columns for %s", mapping.Source)
		}
	}
}

func TestImportSchemaRefusesUnclassifiedState(t *testing.T) {
	for name, statement := range map[string]string{
		"generated column": "ALTER TABLE favorites ADD COLUMN private_generated TEXT GENERATED ALWAYS AS (media_item_id) VIRTUAL",
		"extra column":     "ALTER TABLE favorites ADD COLUMN private_extra TEXT",
		"extra table":      "CREATE TABLE private_extra(value TEXT)",
		"missing table":    "DROP TABLE favorites",
		"legacy rows":      "INSERT INTO playback_sessions VALUES('s','p',1,'direct',0,0,'2026-01-01','2026-01-01')",
		"future version":   "PRAGMA user_version=23",
	} {
		t.Run(name, func(t *testing.T) {
			source, err := testdata.NewSource(filepath.Join(t.TempDir(), "7.db"), 7)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close() //nolint:errcheck
			assertSchema22(t, source)
			if _, err := source.DB.Exec(statement); err != nil {
				t.Fatal(err)
			}
			tx, err := source.DB.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback() //nolint:errcheck
			if err := validateSourceSchema(t.Context(), tx); err == nil {
				t.Fatal("unsupported source accepted")
			}
		})
	}
}

func assertSchema22(t *testing.T, source *userdb.UserDB) {
	t.Helper()
	tx, err := source.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := validateSourceSchema(t.Context(), tx); err != nil {
		t.Fatalf("baseline schema22 invalid: %v", err)
	}
}

func TestImportSchemaRefusesNewAndUpgraded23(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		name := "new"
		if upgrade {
			name = "upgraded"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "7.db")
			var source *userdb.UserDB
			var err error
			if upgrade {
				source, err = testdata.NewSource(path, 7)
				if err != nil {
					t.Fatal(err)
				}
				assertSchema22(t, source)
				if _, err = source.DB.Exec(testdata.Schema23Migration); err != nil {
					t.Fatal(err)
				}
			} else {
				source, err = testdata.NewSource23(path, 7)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer source.Close() //nolint:errcheck
			assertUnsupportedVersion(t, source, 23)
			var count int
			if err := source.DB.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='playback_progress_sinks'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("not genuine schema23: %d %v", count, err)
			}
			if err := source.DB.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='playback_source_markers'").Scan(&count); err != nil || count != 0 {
				t.Fatalf("historical23 contains future marker: %d %v", count, err)
			}
		})
	}
}

func TestImportSchemaRefusesNewAndUpgraded24(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		name := "new"
		if upgrade {
			name = "upgraded"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "7.db")
			var source *userdb.UserDB
			var err error
			if upgrade {
				source, err = testdata.NewSource23(path, 7)
				if err != nil {
					t.Fatal(err)
				}
				assertUnsupportedVersion(t, source, 23)
				if _, err = source.DB.Exec(testdata.Schema24Migration); err != nil {
					t.Fatal(err)
				}
			} else {
				source, err = testdata.NewSource24(path, 7)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer source.Close() //nolint:errcheck
			assertUnsupportedVersion(t, source, 24)
			var count int
			if err := source.DB.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='playback_source_markers'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("schema24 missing actual marker: %d %v", count, err)
			}
		})
	}
}

func TestImportSchemaRefusesCurrentUserDB(t *testing.T) {
	source, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "7.db"), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close() //nolint:errcheck
	var version int
	if err := source.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version <= 24 {
		t.Fatalf("current userdb schema %d is not newer than the frozen fixtures", version)
	}
	assertUnsupportedVersion(t, source, version)
}

func assertUnsupportedVersion(t *testing.T, source *userdb.UserDB, expected int) {
	t.Helper()
	var version int
	if err := source.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != expected {
		t.Fatalf("expected schema%d: %d %v", expected, version, err)
	}
	tx, err := source.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := validateSourceSchema(t.Context(), tx); err == nil {
		t.Fatalf("schema%d accepted", expected)
	}
}
