package bridgeimport

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/migrations"
)

func TestImportReceiptAccountDeletionDB(t *testing.T) {
	dsn := os.Getenv("SILO_IMPORT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_IMPORT_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context()) //nolint:errcheck
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context()) //nolint:errcheck
	// The actual receipt migration runs in an isolated transactional schema.
	// No application or other test tables are touched, even on failure.
	schema := "bridge_receipt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := tx.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), "SET LOCAL search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), "CREATE TABLE users(id integer PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	migration, err := migrations.FS.ReadFile("sql/20260905204450_add_userdb_import_receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err := tx.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), "INSERT INTO users VALUES(7)"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO userdb_import_receipts(user_id,installation_id,source_sha256,schema_version,mapping_version,progress_generation,verification) VALUES(7,$1,$2,22,1,$3,'{}')`, uuid.NewString(), strings.Repeat("a", 64), uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), "DELETE FROM users WHERE id=7"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM userdb_import_receipts").Scan(&count); err != nil || count != 0 {
		t.Fatalf("receipt count=%d err=%v", count, err)
	}
}
