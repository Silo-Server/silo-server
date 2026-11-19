package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Every package's tests run against their own copy of the migrated database.
// go test runs the listed packages in parallel, and on one shared database
// they saw each other's rows, advisory locks and planner statistics: a
// MAX(id)+1 seed collided with another package's insert, and a plan test
// flipped when another package changed the table it read.
//
// dbpins passes itself to go test as -exec. go test then starts each test
// binary through it, with the template's URL in cloneTemplateEnv; the wrapper
// copies the template into a new database, points the binary at the copy, and
// drops the copy when the binary exits. Every copy a run makes is named with
// the run's token in cloneRunEnv, so the run drops any copy a killed wrapper
// left behind, and runs from other checkouts on the same server are untouched.
const (
	cloneTemplateEnv = "DBPINS_CLONE_TEMPLATE"
	cloneRunEnv      = "DBPINS_CLONE_RUN"
	clonePrefix      = "dbpins_"
)

// databaseURLEnvs name the variables a test reads its database from. The
// wrapper repoints each one that names the template.
var databaseURLEnvs = []string{"SILO_TEST_DATABASE_URL", "SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL"}

// setUpClones returns the -exec value that runs each test binary through
// this command and the run's token. It copies the template once first, so a
// database that cannot be copied fails here rather than once per package.
func setUpClones(template string) (execPath, run string, err error) {
	self, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	if execPath, err = execArg(self); err != nil {
		return "", "", err
	}
	if run, err = newRunToken(); err != nil {
		return "", "", err
	}
	ctx := context.Background()
	name, _, err := cloneDatabase(ctx, template, run, "preflight")
	if err != nil {
		return "", "", err
	}
	if err := dropDatabase(ctx, template, name); err != nil {
		return "", "", err
	}
	return execPath, run, nil
}

// runCloned is the -exec wrapper: argv is the test binary and its flags.
func runCloned(template string, argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "dbpins: no test binary to run")
		return 2
	}
	ctx := context.Background()
	dir, _ := os.Getwd()
	clone, cloneURL, err := cloneDatabase(ctx, template, os.Getenv(cloneRunEnv), dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbpins: %v\n", err)
		return 1
	}
	defer func() {
		if err := dropDatabase(context.Background(), template, clone); err != nil {
			fmt.Fprintf(os.Stderr, "dbpins: %v\n", err)
		}
	}()

	// go test signals only the process it started, which is this wrapper: a
	// timeout sends SIGQUIT for the test binary's goroutine dump. Handled
	// signals reset to their default in the child, so the binary still dies on
	// each one it is sent.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(signals)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // go test hands -exec the test binary it built and its flags; no shell is invoked
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = cloneEnv(os.Environ(), template, cloneURL)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "dbpins: %v\n", err)
		return 1
	}
	go func() {
		for sig := range signals {
			_ = cmd.Process.Signal(sig)
		}
	}()
	err = cmd.Wait()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr) && exitErr.ExitCode() >= 0:
		return exitErr.ExitCode()
	default:
		fmt.Fprintf(os.Stderr, "dbpins: %v\n", err)
		return 1
	}
}

// cloneEnv drops the wrapper's own variables and repoints every database
// variable that names the template at the clone.
func cloneEnv(environ []string, template, cloneURL string) []string {
	env := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, value, _ := strings.Cut(kv, "=")
		switch {
		case key == cloneTemplateEnv || key == cloneRunEnv:
			continue
		case slices.Contains(databaseURLEnvs, key) && sameDatabase(value, template):
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

// execArg quotes path for go test's -exec, which splits its value on spaces
// and honors quotes.
func execArg(path string) (string, error) {
	if !strings.ContainsAny(path, " \t\n'\"") {
		return path, nil
	}
	if !strings.Contains(path, "'") {
		return "'" + path + "'", nil
	}
	if !strings.Contains(path, `"`) {
		return `"` + path + `"`, nil
	}
	return "", fmt.Errorf("cannot pass %q to go test -exec; build dbpins to a path without quotes", path)
}
