package migrations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Run the committed migration SQL in a temporary schema, rolled back with the
// fixture. No application tables or Goose version records are modified.
func adminMigrationFixture(t *testing.T) (pgx.Tx, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	schema := fmt.Sprintf("admin_migration_test_%d", time.Now().UnixNano())
	migrationExec(t, tx, "CREATE SCHEMA "+schema+"; SET LOCAL search_path TO "+schema+", public")
	return tx, schema
}

func migrationExec(t *testing.T, tx pgx.Tx, sql string) {
	t.Helper()
	if _, err := tx.Exec(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
}

func adminMigrationSQL(t *testing.T, name, schema string, down bool) string {
	t.Helper()
	data, err := os.ReadFile("sql/" + name + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	up, rollback, found := strings.Cut(string(data), "-- +goose Down")
	if !found {
		t.Fatal("migration has no down marker", name)
	}
	sql := up
	if down {
		sql = rollback
	}
	return strings.NewReplacer("public.", schema+".", "'public'", "'"+schema+"'").Replace(sql)
}

func requireMigrationSQLState(t *testing.T, tx pgx.Tx, sql, code string) {
	t.Helper()
	savepoint, err := tx.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = savepoint.Exec(t.Context(), sql)
	_ = savepoint.Rollback(t.Context())
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || pgErr.Code != code {
		t.Fatalf("expected SQLSTATE %s, got %v", code, err)
	}
}
