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

const requestIntegrationPluginIDMigration = "20261118070115_request_integration_plugin_id"

// The backfill records each request server's plugin from its installation.
// A server whose installation is gone gets a plugin only when the sole
// installed plugin offering its capability is the official router for it, so
// its API key never moves to a plugin that merely reuses the capability id.
func TestRequestIntegrationPluginIDBackfillPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("request_integration_plugin_id_%d", time.Now().UnixNano())
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	if _, err := db.ExecContext(t.Context(), `
CREATE TABLE plugin_installations (id integer PRIMARY KEY, plugin_id text NOT NULL);
CREATE TABLE plugin_capabilities (plugin_installation_id integer, capability_type text, capability_id text);
CREATE TABLE request_integrations (id text PRIMARY KEY, capability_id text, installation_id integer);
INSERT INTO plugin_installations VALUES
	(1, 'silo.requests.arr'),
	(2, 'example.requests.seerr'),
	(3, 'silo.requests.seerr'),
	(4, 'example.requests.ombi');
INSERT INTO plugin_capabilities VALUES
	(1, 'request_router.v1', 'arr'),
	(2, 'request_router.v1', 'seerr'),
	(3, 'request_router.v1', 'seerr'),
	(4, 'request_router.v1', 'ombi');
INSERT INTO request_integrations VALUES
	('bound', 'arr', 1),
	('official-orphan', 'arr', 91),
	('shared-orphan', 'seerr', 92),
	('third-party-orphan', 'ombi', 93),
	('no-installation', 'arr', NULL);`); err != nil {
		t.Fatal(err)
	}
	data, err := FS.ReadFile("sql/" + requestIntegrationPluginIDMigration + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fstest.MapFS{
		requestIntegrationPluginIDMigration + ".sql": &fstest.MapFile{Data: []byte(strings.ReplaceAll(string(data), "public.", schema+"."))},
	}, goose.WithTableName(schema+".goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"bound":           "silo.requests.arr",
		"official-orphan": "silo.requests.arr",
		// The official and a third-party plugin both offer seerr.
		"shared-orphan":      "",
		"third-party-orphan": "",
		"no-installation":    "",
	} {
		var got string
		if err := db.QueryRowContext(t.Context(), "SELECT plugin_id FROM request_integrations WHERE id = $1", id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: plugin_id = %q, want %q", id, got, want)
		}
	}
}
