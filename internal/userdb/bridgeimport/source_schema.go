package bridgeimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// validateSourceSchema checks actual columns, rather than treating user_version
// or the read-only preflight's column count as proof of representability.
func validateSourceSchema(ctx context.Context, tx *sql.Tx) error {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 22 {
		return errors.New("source schema version is unsupported")
	}
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return errors.New("cannot inspect source schema")
	}
	defer func() { _ = rows.Close() }()
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return errors.New("cannot inspect source schema")
		}
		if _, ok := sourceColumns[name]; !ok {
			return errors.New("unmapped source table")
		}
		found[name] = true
	}
	err = rows.Err()
	// Close before the per-table queries below; the deferred Close is then a no-op.
	_ = rows.Close()
	if err != nil {
		return errors.New("cannot inspect source schema")
	}
	if len(found) != len(sourceColumns) {
		return errors.New("missing source table")
	}
	for _, mapping := range Manifest() {
		if err := validateSourceColumns(ctx, tx, mapping.Source); err != nil {
			return err
		}
		if mapping.Target == "" && mapping.Source != sourceCollectionAudiences {
			var count int64
			// The identifier comes only from the fixed manifest, never a discovered name.
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM "`+mapping.Source+`"`).Scan(&count); err != nil {
				return errors.New("cannot inspect legacy source rows")
			}
			if count != 0 {
				return fmt.Errorf("nonempty %s has no supported import", mapping.Source)
			}
		}
	}
	return nil
}

// validateSourceColumns checks that table has exactly the columns
// sourceColumns lists for it, with matching type, nullability, and key role.
func validateSourceColumns(ctx context.Context, tx *sql.Tx, table string) error {
	expected := map[string]SourceColumn{}
	for _, column := range sourceColumns[table] {
		expected[column.Name] = column
	}
	columns, err := tx.QueryContext(ctx, "SELECT name, type, [notnull], pk, hidden FROM pragma_table_xinfo(?) ORDER BY cid", table)
	if err != nil {
		return errors.New("cannot inspect source columns")
	}
	defer func() { _ = columns.Close() }()
	i := 0
	for columns.Next() {
		var name, declared string
		var required bool
		var pk, hidden int
		if err := columns.Scan(&name, &declared, &required, &pk, &hidden); err != nil {
			return errors.New("cannot inspect source columns")
		}
		column, known := expected[name]
		if hidden != 0 || !known || strings.ToUpper(declared) != column.SQLiteType || required != column.NotNull || pk != column.PrimaryKey {
			return fmt.Errorf("unsupported source columns in %s", table)
		}
		i++
	}
	if err := columns.Err(); err != nil || i != len(expected) {
		return fmt.Errorf("incomplete source columns in %s", table)
	}
	return nil
}
