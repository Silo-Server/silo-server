package migrations

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestAccessTokenExpiryMigrationPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	connectionConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*connectionConfig)
	t.Cleanup(func() { _ = db.Close() })
	const file = "20260917000328_raise_default_access_token_expiry.sql"
	data, err := FS.ReadFile("sql/" + file)
	if err != nil {
		t.Fatal(err)
	}
	for _, refresh := range []string{
		"30d", "1d", "24h", "48h", "1440m", "86400s", "+24h", "23h60m", "23.5h30m", "24.h",
		"23h59m59.999999999s", "23h59m59999999999ns", "86400000000us", "86400000000µs", "86400000000μs", "86400000ms",
		"12h", "1h", "", "missing", "invalid",
	} {
		for _, value := range []string{"1h", "8h", "24h", "12h", "48h", "60m", "", "missing"} {
			t.Run(refresh+"/"+value, func(t *testing.T) {
				schema := fmt.Sprintf("auth_expiry_%d", time.Now().UnixNano())
				exec := func(query string, args ...any) {
					t.Helper()
					if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
						t.Fatal(err)
					}
				}
				exec("CREATE SCHEMA " + schema)
				t.Cleanup(func() {
					if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
						t.Error(err)
					}
				})
				exec("CREATE TABLE " + schema + ".server_settings (key text PRIMARY KEY, value text NOT NULL)")
				exec("INSERT INTO " + schema + ".server_settings VALUES ('unrelated', '8h')")
				if refresh != "missing" {
					exec("INSERT INTO "+schema+".server_settings VALUES ('auth.refresh_token_expiry', $1)", refresh)
				}
				if value != "missing" {
					exec("INSERT INTO "+schema+".server_settings VALUES ('auth.access_token_expiry', $1)", value)
				}
				fixture := fstest.MapFS{file: &fstest.MapFile{Data: []byte(strings.ReplaceAll(string(data), "public.", schema+"."))}}
				provider, err := goose.NewProvider(goose.DialectPostgres, db, fixture, goose.WithTableName(schema+".goose_db_version"))
				if err != nil {
					t.Fatal(err)
				}
				// Model the old runtime fallback independently of the new default.
				previousAccess := value
				if previousAccess == "" || previousAccess == "missing" {
					previousAccess = "8h"
				}
				previousRefresh := refresh
				if previousRefresh == "missing" {
					previousRefresh = ""
				}
				validBefore := config.ValidateAdminSettings(map[string]string{
					"auth.access_token_expiry":  previousAccess,
					"auth.refresh_token_expiry": previousRefresh,
				}) == nil
				want := value
				refreshDuration, parseErr := time.ParseDuration(refresh)
				canUpgrade := refresh == "30d" || refresh == "1d" || refresh == "" || refresh == "missing" || (parseErr == nil && refreshDuration >= 24*time.Hour)
				if canUpgrade && (value == "1h" || value == "8h") {
					want = "24h"
				} else if !canUpgrade && (value == "" || value == "missing") {
					want = "8h"
				}
				check := func() {
					t.Helper()
					var got string
					if err := db.QueryRowContext(t.Context(), "SELECT COALESCE((SELECT value FROM "+schema+".server_settings WHERE key = 'auth.access_token_expiry'), 'missing')").Scan(&got); err != nil || got != want {
						t.Fatalf("access expiry = %q, want %q: %v", got, want, err)
					}
					if validBefore {
						storedAccess := got
						if storedAccess == "missing" {
							storedAccess = ""
						}
						if err := config.ValidateAdminSettings(map[string]string{
							"auth.access_token_expiry":  storedAccess,
							"auth.refresh_token_expiry": previousRefresh,
						}); err != nil {
							t.Fatalf("migration invalidated previously valid admin settings: %v", err)
						}
					}
					var gotRefresh, unrelated string
					if err := db.QueryRowContext(t.Context(), "SELECT COALESCE((SELECT value FROM "+schema+".server_settings WHERE key = 'auth.refresh_token_expiry'), 'missing'), (SELECT value FROM "+schema+".server_settings WHERE key = 'unrelated')").Scan(&gotRefresh, &unrelated); err != nil || gotRefresh != refresh || unrelated != "8h" {
						t.Fatalf("other settings changed: refresh=%q unrelated=%q: %v", gotRefresh, unrelated, err)
					}
				}
				for range 2 {
					if _, err := provider.Up(t.Context()); err != nil {
						t.Fatal(err)
					}
					check()
				}
				if _, err := provider.Down(t.Context()); err != nil {
					t.Fatal(err)
				}
				check()
			})
		}
	}
}
