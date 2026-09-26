package bridgeimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func importTable(ctx context.Context, source *sql.Tx, target pgx.Tx, userID int, mapping Mapping) (TableVerification, error) {
	columns := sourceColumns[mapping.Source]
	sourceSelect := make([]string, len(columns))
	var order []SourceColumn
	for i, column := range columns {
		name := pgx.Identifier{column.Name}.Sanitize()
		sourceSelect[i] = name
		if column.Kind == booleanColumn {
			// Unary plus removes declared-type decoding without converting the
			// SQLite storage value. CAST would truncate reals and coerce text.
			sourceSelect[i] = "+" + name
		}
		if column.PrimaryKey > 0 {
			order = append(order, column)
		}
	}
	slices.SortFunc(order, func(a, b SourceColumn) int { return a.PrimaryKey - b.PrimaryKey })
	var ordering []string
	for _, column := range order {
		ordering = append(ordering, pgx.Identifier{column.Name}.Sanitize())
	}
	rows, err := source.QueryContext(ctx, "SELECT "+strings.Join(sourceSelect, ",")+" FROM "+pgx.Identifier{mapping.Source}.Sanitize()+" ORDER BY "+strings.Join(ordering, ","))
	if err != nil {
		return TableVerification{}, importFailure(ctx, "cannot read source table")
	}
	defer rows.Close() //nolint:errcheck
	selected := []SourceColumn{}
	for _, column := range columns {
		if !remappedColumn(mapping.Source, column.Name) {
			selected = append(selected, column)
		}
	}
	targetColumns := []string{"user_id"}
	for _, column := range selected {
		targetColumns = append(targetColumns, column.Name)
	}
	digest := sha256.New()
	var count int64
	batch := make([][]any, 0, 256)
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := target.CopyFrom(ctx, pgx.Identifier{mapping.Target}, targetColumns, pgx.CopyFromRows(batch)); err != nil {
			return importFailure(ctx, "target cannot represent source table "+mapping.Source)
		}
		batch = batch[:0]
		batchBytes = 0
		return nil
	}
	for rows.Next() {
		raw := make([]any, len(columns))
		scan := make([]any, len(columns))
		for i := range raw {
			scan[i] = &raw[i]
		}
		if err := rows.Scan(scan...); err != nil {
			return TableVerification{}, importFailure(ctx, "cannot read source values")
		}
		values := []any{userID}
		rowBytes := 0
		for i, column := range columns {
			value, err := importValue(column, raw[i])
			if err != nil {
				return TableVerification{}, fmt.Errorf("unsupported value in %s.%s", mapping.Source, column.Name)
			}
			if text, ok := value.(string); ok {
				rowBytes += len(text)
			} else {
				rowBytes += 32
			}
			if rowBytes > maxImportedValueBytes {
				return TableVerification{}, errors.New("source row exceeds import limit")
			}
			if !remappedColumn(mapping.Source, column.Name) {
				values = append(values, value)
			}
		}
		if err := hashProjectedRow(ctx, target, digest, mapping.Source, selected, values[1:]); err != nil {
			return TableVerification{}, err
		}
		if mapping.Source == sourceCollectionRevisions || mapping.Source == sourceOrderRevision {
			if err := rebaseWitness(ctx, target, userID, mapping, values[1:]); err != nil {
				return TableVerification{}, err
			}
		} else {
			batch = append(batch, values)
			batchBytes += rowBytes
			if len(batch) == cap(batch) || batchBytes >= 4<<20 {
				if err := flush(); err != nil {
					return TableVerification{}, err
				}
			}
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return TableVerification{}, importFailure(ctx, "source read failed")
	}
	if err := flush(); err != nil {
		return TableVerification{}, err
	}
	expected := TableVerification{Rows: count, SHA256: hex.EncodeToString(digest.Sum(nil))}
	actual, err := verifyTargetTable(ctx, target, userID, mapping, selected, order)
	if err != nil {
		return TableVerification{}, err
	}
	if actual != expected {
		return TableVerification{}, fmt.Errorf("semantic verification failed in %s", mapping.Source)
	}
	return expected, nil
}

func remappedColumn(table, column string) bool {
	return (column == "id" && (table == "user_setting_values" || table == "user_setting_migration_rejects")) || (table == "watch_progress" && column == "synced_seq") || (table == sourceOrderRevision && column == sourceSingleton)
}
func rebaseWitness(ctx context.Context, tx pgx.Tx, userID int, mapping Mapping, values []any) error {
	revisionIndex := len(values) - 1
	revision, ok := values[revisionIndex].(int64)
	if !ok || revision < 1 || revision == math.MaxInt64 {
		return errors.New("invalid source collection witness")
	}
	var current int64
	if mapping.Source == sourceCollectionRevisions {
		err := tx.QueryRow(ctx, "SELECT revision FROM user_collection_revisions WHERE user_id=$1 AND collection_id=$2", userID, values[0]).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return importFailure(ctx, "cannot inspect collection witness")
		}
		if current == math.MaxInt64 {
			return errors.New("collection witness exhausted")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_collection_revisions(user_id,collection_id,revision) VALUES($1,$2,$3) ON CONFLICT(user_id,collection_id) DO UPDATE SET revision=EXCLUDED.revision`, userID, values[0], max(current, revision)+1); err != nil {
			return importFailure(ctx, "cannot rebase collection witness")
		}
	} else {
		err := tx.QueryRow(ctx, "SELECT revision FROM user_collection_order_revisions WHERE user_id=$1", userID).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return importFailure(ctx, "cannot inspect collection order witness")
		}
		if current == math.MaxInt64 {
			return errors.New("collection order witness exhausted")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_collection_order_revisions(user_id,revision) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET revision=EXCLUDED.revision`, userID, max(current, revision)+1); err != nil {
			return importFailure(ctx, "cannot rebase collection order witness")
		}
	}
	return nil
}
func digestColumn(table string, column SourceColumn) bool {
	return column.Name != "revision" || (table != sourceCollectionRevisions && table != sourceOrderRevision)
}
func hashProjectedRow(ctx context.Context, tx pgx.Tx, digest hash.Hash, table string, columns []SourceColumn, values []any) error {
	var expressions []string
	var args []any
	for i, column := range columns {
		if !digestColumn(table, column) {
			continue
		}
		cast := "text"
		switch column.Kind {
		case integerColumn:
			cast = "bigint"
		case booleanColumn:
			cast = "boolean"
		case realColumn:
			cast = "double precision"
		case instantColumn:
			cast = "timestamptz"
		case jsonColumn:
			cast = "jsonb"
		}
		args = append(args, values[i])
		expressions = append(expressions, "$"+strconv.Itoa(len(args))+"::"+cast)
	}
	var encoded string
	if err := tx.QueryRow(ctx, "SELECT jsonb_build_array("+strings.Join(expressions, ",")+")::text", args...).Scan(&encoded); err != nil {
		return importFailure(ctx, "source value cannot be represented in PostgreSQL")
	}
	_, _ = digest.Write([]byte(encoded + "\n"))
	return nil
}
func verifyTargetTable(ctx context.Context, tx pgx.Tx, userID int, mapping Mapping, columns, order []SourceColumn) (TableVerification, error) {
	var expressions, ordering []string
	for _, column := range columns {
		if digestColumn(mapping.Source, column) {
			expressions = append(expressions, pgx.Identifier{column.Name}.Sanitize())
		}
	}
	for _, column := range order {
		if column.Name == sourceSingleton {
			ordering = append(ordering, "user_id")
			continue
		}
		expression := pgx.Identifier{column.Name}.Sanitize()
		if column.SQLiteType == sourceText {
			expression += ` COLLATE "C"`
		}
		ordering = append(ordering, expression)
	}
	rows, err := tx.Query(ctx, "SELECT jsonb_build_array("+strings.Join(expressions, ",")+")::text FROM "+pgx.Identifier{mapping.Target}.Sanitize()+" WHERE user_id=$1 ORDER BY "+strings.Join(ordering, ","), userID)
	if err != nil {
		return TableVerification{}, importFailure(ctx, "cannot verify target table")
	}
	defer rows.Close()
	digest := sha256.New()
	var count int64
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return TableVerification{}, importFailure(ctx, "cannot verify target values")
		}
		_, _ = digest.Write([]byte(value + "\n"))
		count++
	}
	if err := rows.Err(); err != nil {
		return TableVerification{}, importFailure(ctx, "target verification failed")
	}
	return TableVerification{Rows: count, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}
