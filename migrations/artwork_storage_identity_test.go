package migrations

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// TestArtworkStorageIdentityMigrationPostgres covers the shapes a database can
// be in before the identity row existed. Released builds recorded only the S3
// fingerprint; the backend row came from unreleased builds; fresh databases
// have neither.
func TestArtworkStorageIdentityMigrationPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })

	const file = "20260915210000_artwork_storage_identity.sql"
	source, err := FS.ReadFile("sql/" + file)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		rows map[string]string
		want string
	}{
		{"released s3 install", map[string]string{"s3.public_storage_identity": "https://s3.example|artwork|silo"}, "s3|https://s3.example|artwork|silo"},
		{"unreleased s3 backend row", map[string]string{"artwork.storage_backend_active": "s3", "s3.public_storage_identity": "https://s3.example|artwork|"}, "s3|https://s3.example|artwork|"},
		{"unreleased local backend row", map[string]string{"artwork.storage_backend_active": "local", "s3.public_storage_identity": "local|/var/lib/silo/artwork", "s3.public_storage_sweep_checkpoint": "{}"}, "local|/var/lib/silo/artwork"},
		{"backend row without fingerprint", map[string]string{"artwork.storage_backend_active": "s3"}, ""},
		{"fresh database", map[string]string{}, ""},
		{"already migrated", map[string]string{"artwork.storage_identity": "local|/srv/art", "s3.public_storage_identity": "https://s3.example|artwork|"}, "local|/srv/art"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := fmt.Sprintf("artwork_identity_test_%d_%d", i, time.Now().UnixNano())
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("CREATE SCHEMA " + schema)
			t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
			exec("CREATE TABLE " + schema + ".server_settings (key text PRIMARY KEY, value text NOT NULL)")
			for key, value := range tc.rows {
				exec("INSERT INTO "+schema+".server_settings VALUES ($1, $2)", key, value)
			}
			sql := strings.ReplaceAll(string(source), "server_settings", schema+".server_settings")
			fixture := fstest.MapFS{file: &fstest.MapFile{Data: []byte(sql)}}
			provider, err := goose.NewProvider(goose.DialectPostgres, db, fixture, goose.WithTableName(schema+".goose_db_version"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Up(t.Context()); err != nil {
				t.Fatal(err)
			}
			var got string
			err = db.QueryRowContext(t.Context(), "SELECT value FROM "+schema+".server_settings WHERE key = 'artwork.storage_identity'").Scan(&got)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("unexpected identity %q", got)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("identity = %q, %v; want %q", got, err, tc.want)
			}
			var legacy int
			if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+schema+".server_settings WHERE key IN ('artwork.storage_backend_active','s3.public_storage_identity','s3.public_storage_sweep_checkpoint')").Scan(&legacy); err != nil || legacy != 0 {
				t.Fatalf("legacy rows remaining = %d: %v", legacy, err)
			}
		})
	}
}
