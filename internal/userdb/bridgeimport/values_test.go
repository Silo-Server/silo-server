package bridgeimport

import (
	"database/sql"
	"math"
	"testing"
)

func TestImportValuesRefuseLossyConversion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  ColumnKind
		value any
	}{
		{"boolean integer", booleanColumn, int64(2)},
		{"integer text", integerColumn, "4"},
		{"infinite", realColumn, math.Inf(1)},
		{"NUL text", textColumn, "private\x00value"},
		{"timestamp precision", instantColumn, "2026-01-01T00:00:00.000000001Z"},
		{"unknown time", instantColumn, ""},
		{"duplicate JSON", jsonColumn, `{"value":false,"value":true}`},
		{"JSON NUL", jsonColumn, `{"value":"\u0000"}`},
		{"trailing JSON", jsonColumn, `{} true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := importValue(SourceColumn{Kind: tc.kind}, tc.value); err == nil {
				t.Fatal("lossy value accepted")
			}
		})
	}
	for _, tc := range []struct {
		kind  ColumnKind
		value any
	}{
		{booleanColumn, int64(0)}, {booleanColumn, nil}, {integerColumn, int64(7)},
		{instantColumn, "2026-01-01T01:00:00.123456+01:00"},
		{jsonColumn, `{"value":null,"nested":[false,0,""]}`},
		{textColumn, "opaque non-JSON"},
	} {
		if _, err := importValue(SourceColumn{Kind: tc.kind}, tc.value); err != nil {
			t.Fatal(err)
		}
	}
}

// SQLite's BOOLEAN affinity does not constrain storage classes. Unary plus must
// expose the original value, including values the driver's BOOL decoder hides.
func TestSQLiteBooleanStorageRemainsLossless(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TABLE flags(value BOOLEAN)"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		literal string
		valid   bool
	}{
		{"0", true}, {"1", true}, {"NULL", true},
		{"2", false}, {"1.5", false}, {"0.5", false}, {"'invalid'", false},
	} {
		t.Run(tc.literal, func(t *testing.T) {
			if _, err := db.Exec("DELETE FROM flags"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO flags VALUES(" + tc.literal + ")"); err != nil {
				t.Fatal(err)
			}
			var original any
			if err := db.QueryRow("SELECT +value FROM flags").Scan(&original); err != nil {
				t.Fatal(err)
			}
			_, err := importValue(SourceColumn{Kind: booleanColumn}, original)
			if (err == nil) != tc.valid {
				t.Fatalf("stored %s: validation error %v", tc.literal, err)
			}
		})
	}
}
