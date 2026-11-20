package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Every shard runs against its own copy of the migrated database. Packages and
// shards run in parallel, and on one shared database they saw each other's
// rows, advisory locks and planner statistics: a MAX(id)+1 seed collided with
// another package's insert, and a plan test flipped when another package
// changed the table it read. Every copy a run makes is named with the run's
// token so cleanup drops any copy a killed worker left behind, and runs from
// other checkouts on the same server are untouched.
const clonePrefix = "dbpins_"

// databaseURLEnvs name the variables a test reads its database from.
// cloneEnv repoints each one that names the template.
var databaseURLEnvs = []string{"SILO_TEST_DATABASE_URL", "SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL"}

// cloneEnv repoints every database variable that names the template at the
// clone.
func cloneEnv(environ []string, template, cloneURL string) []string {
	env := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, value, _ := strings.Cut(kv, "=")
		if slices.Contains(databaseURLEnvs, key) && sameDatabase(value, template) {
			kv = key + "=" + cloneURL
		}
		env = append(env, kv)
	}
	return env
}

// sameDatabase reports whether two URLs connect to one database on one
// server, as pgx resolves them: query parameters such as host, port, dbname
// and database override the rest of the URL. The loopback names count as one
// host: this assumes one test server answers on loopback at a port. Separate
// servers on 127.0.0.1 and ::1 at the same port would make a URL for the other
// server look like the template's, and its tests would run against the copy.
func sameDatabase(a, b string) bool {
	if a == b {
		return true
	}
	ca, errA := pgconn.ParseConfig(a)
	cb, errB := pgconn.ParseConfig(b)
	if errA != nil || errB != nil {
		return false
	}
	host := func(h string) string {
		switch h {
		case "localhost", "127.0.0.1", "::1":
			return "localhost"
		default:
			return h
		}
	}
	return host(ca.Host) == host(cb.Host) && ca.Port == cb.Port && ca.Database == cb.Database
}

var unsafeName = regexp.MustCompile(`[^a-z0-9]+`)

// newRunToken names one dbpins run's copies.
func newRunToken() (string, error) {
	token := make([]byte, 4)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}

// cloneName names the copy after the run and the package directory, so a
// database left behind says whose it was.
func cloneName(run, dir string) (string, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	parent, base := filepath.Base(filepath.Dir(dir)), filepath.Base(dir)
	pkg := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(parent+"_"+base), "_"), "_")
	// Postgres keeps 63 bytes of a name.
	prefix := clonePrefix + run + "_"
	if limit := 63 - len(prefix) - 1 - 2*len(suffix); len(pkg) > limit {
		pkg = pkg[len(pkg)-limit:]
	}
	return prefix + pkg + "_" + hex.EncodeToString(suffix), nil
}

// cloneDatabase copies the template database into a new one named for the
// run and the package in dir, and returns the new database's name and URL.
func cloneDatabase(ctx context.Context, template, run, dir string) (string, string, error) {
	if run == "" {
		return "", "", errors.New("no run token to name the database copy with")
	}
	tmplName, err := databaseName(template)
	if err != nil {
		return "", "", err
	}
	name, err := cloneName(run, dir)
	if err != nil {
		return "", "", err
	}
	conn, err := connectMaintenance(ctx, template)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{tmplName}.Sanitize()); err != nil {
		return "", "", fmt.Errorf("copy database %s for %s: %w (nothing else may be connected to it; pass -shared to run every package against it directly)", tmplName, dir, err)
	}
	cloneURL, err := withDatabase(template, name)
	if err != nil {
		return "", "", err
	}
	return name, cloneURL, nil
}

func dropDatabase(ctx context.Context, template, name string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := connectMaintenance(ctx, template)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop database %s: %w", name, err)
	}
	return nil
}

// dropRunClones drops every copy the run made that a wrapper did not.
func dropRunClones(template, run string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := connectMaintenance(ctx, template)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	rows, err := conn.Query(ctx, "SELECT datname FROM pg_database WHERE starts_with(datname, $1)", clonePrefix+run+"_")
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		errs = append(errs, dropDatabase(ctx, template, name))
	}
	return errors.Join(errs...)
}

// connectMaintenance connects to the server's postgres database: CREATE
// DATABASE refuses a template that has any session, including its own.
func connectMaintenance(ctx context.Context, template string) (*pgx.Conn, error) {
	maintenance, err := withDatabase(template, "postgres")
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, maintenance)
	if err != nil {
		return nil, fmt.Errorf("connect to the database server: %w", err)
	}
	return conn, nil
}

// databaseName is the database the URL connects to, as pgx resolves it.
func databaseName(dsn string) (string, error) {
	if _, err := parseDatabaseURL(dsn); err != nil {
		return "", err
	}
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return "", err
	}
	name := cfg.Database
	if name == "" || name == "postgres" {
		return "", fmt.Errorf("SILO_TEST_DATABASE_URL must name a disposable database, not %q", name)
	}
	return name, nil
}

// withDatabase points the URL at another database on the same server,
// dropping the dbname and database query parameters, either of which would
// override the path.
func withDatabase(dsn, name string) (string, error) {
	u, err := parseDatabaseURL(dsn)
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Del("dbname")
	query.Del("database")
	u.RawQuery = query.Encode()
	u.Path, u.RawPath = "/"+name, ""
	return u.String(), nil
}

func parseDatabaseURL(dsn string) (*url.URL, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errors.New("SILO_TEST_DATABASE_URL must be a postgres:// URL; pass -shared for another form")
	}
	return u, nil
}
