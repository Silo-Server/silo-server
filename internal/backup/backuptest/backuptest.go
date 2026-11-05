// Package backuptest gives a database test that runs pg_dump or pg_restore a
// database of its own.
//
// pg_dump reads every schema of the database it dumps, and other packages'
// tests create and drop schemas in the shared SILO_TEST_DATABASE_URL database
// while they run: a schema dropped between pg_dump's catalog read and its
// table locks fails the dump. A test that dumps or restores therefore runs on
// a private copy of a migrated template database. Tests that only read and
// write rows stay on the shared database.
package backuptest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/migrations"
)

// The template is migrated once per test binary and copied for each test.
var (
	templateOnce sync.Once
	templateName string
	errTemplate  error
)

// Database creates a migrated database for the test alone, on the server
// SILO_TEST_DATABASE_URL names, and returns a pool on it and its URL. The
// database is dropped, its connections terminated, when the test ends. It
// skips the test when SILO_TEST_DATABASE_URL is not set. A package that calls
// it runs its tests through Main.
func Database(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	templateOnce.Do(func() { templateName, errTemplate = createTemplate(dsn) })
	if errTemplate != nil {
		t.Fatalf("migrated template database: %v", errTemplate)
	}
	name, err := uniqueName("silo_backuptest_")
	if err != nil {
		t.Fatal(err)
	}
	if err := adminExec(t.Context(), dsn, "CREATE DATABASE "+ident(name)+" TEMPLATE "+ident(templateName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dropDatabase(dsn, name); err != nil {
			t.Errorf("dropping the test database: %v", err)
		}
	})
	dbURL, err := withDatabase(dsn, name)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, dbURL
}

// Main runs a package's tests, then drops the template database Database
// copies.
func Main(m *testing.M) {
	code := m.Run()
	if templateName != "" {
		if err := dropDatabase(os.Getenv("SILO_TEST_DATABASE_URL"), templateName); err != nil {
			fmt.Fprintf(os.Stderr, "backuptest: dropping the template database: %v\n", err)
		}
	}
	os.Exit(code)
}

// createTemplate creates a database and runs the repository's migrations into
// it as `silo --migrate-only` does. No connection stays open on it, so it can
// be copied.
func createTemplate(dsn string) (name string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if name, err = uniqueName("silo_backuptest_template_"); err != nil {
		return "", err
	}
	if err := adminExec(ctx, dsn, "CREATE DATABASE "+ident(name)); err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = dropDatabase(dsn, name)
		}
	}()
	dbURL, err := withDatabase(dsn, name)
	if err != nil {
		return "", err
	}
	pool, err := database.NewPool(ctx, config.DatabaseConfig{URL: dbURL, MaxConnections: 4})
	if err != nil {
		return "", err
	}
	defer database.ClosePool(pool)
	if err := database.MigrateOnly(ctx, pool, migrations.FS, "sql"); err != nil {
		return "", fmt.Errorf("migrating: %w", err)
	}
	return name, nil
}

// dropDatabase drops a database, terminating the connections still open on it.
func dropDatabase(dsn, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return adminExec(ctx, dsn, "DROP DATABASE IF EXISTS "+ident(name)+" WITH (FORCE)")
}

// adminExec runs a statement on a connection of its own to the shared
// database: CREATE and DROP DATABASE cannot run in a transaction.
func adminExec(ctx context.Context, dsn, sql string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

// withDatabase is dsn naming another database on the same server.
func withDatabase(dsn, name string) (string, error) {
	if !strings.Contains(dsn, "://") {
		return dsn + " dbname=" + name, nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parsing SILO_TEST_DATABASE_URL: %w", err)
	}
	u.Path = "/" + name
	u.RawPath = ""
	return u.String(), nil
}

func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

func uniqueName(prefix string) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}
