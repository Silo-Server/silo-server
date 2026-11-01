package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jackc/pgx/v5"
)

// PGLoader restores dumps with pg_restore and reads databases with pgx.
type PGLoader struct {
	Tools Tools
}

func (l PGLoader) Empty(ctx context.Context, dbURL string) (bool, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var relations int
	err = conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'
		  AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = c.oid AND d.deptype = 'e')`).Scan(&relations)
	return relations == 0, err
}

// Load restores without owners or grants: the restoring role owns everything,
// which is the role Silo then connects as.
func (l PGLoader) Load(ctx context.Context, dbURL, dumpPath string) error {
	target, env, err := libpqTarget(dbURL, "")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, l.Tools.PgRestore, "--exit-on-error", "--no-owner", "--no-privileges", "--no-password", "--dbname="+target, dumpPath) //nolint:gosec // G204: the discovered pg_restore with arguments built here, no shell
	cmd.Env = append(os.Environ(), env...)
	stderr := &limitedBuffer{limit: stderrLimit}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (l PGLoader) Inspect(ctx context.Context, dbURL string) (Inspection, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return Inspection{}, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var in Inspection
	if in.GooseVersion, in.Applied, err = appliedMigrations(ctx, conn); err != nil {
		return Inspection{}, err
	}
	in.Tables, err = tableInventory(ctx, conn)
	return in, err
}

// PGControl is the swap's control connection to the maintenance database.
type PGControl struct {
	Conn *pgx.Conn
	// Like is the database whose encoding and libc locale a created
	// database copies, so the restored catalog sorts as the live one did.
	Like string
}

// ConnectControl opens the control connection to the maintenance database
// ("postgres") of the server databaseURL points at.
func ConnectControl(ctx context.Context, databaseURL string) (*PGControl, error) {
	live, err := databaseName(databaseURL)
	if err != nil {
		return nil, err
	}
	maintenance, err := WithDatabase(databaseURL, "postgres")
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, maintenance)
	if err != nil {
		return nil, fmt.Errorf("connecting to the maintenance database: %w", err)
	}
	return &PGControl{Conn: conn, Like: live}, nil
}

func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

func (c *PGControl) OID(ctx context.Context, name string) (uint32, bool, error) {
	var oid uint32
	err := c.Conn.QueryRow(ctx, `SELECT oid FROM pg_database WHERE datname = $1`, name).Scan(&oid)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return oid, err == nil, err
}

func (c *PGControl) NameOf(ctx context.Context, oid uint32) (string, bool, error) {
	var name string
	err := c.Conn.QueryRow(ctx, `SELECT datname FROM pg_database WHERE oid = $1`, oid).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

func (c *PGControl) OtherConnections(ctx context.Context, name string) (int, error) {
	var n int
	err := c.Conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name).Scan(&n)
	return n, err
}

// CheckPrivileges: renaming needs CREATEDB and ownership, and ALLOW_CONNECTIONS
// needs ownership; a superuser has both.
func (c *PGControl) CheckPrivileges(ctx context.Context, live string) error {
	var super, createdb, owner bool
	err := c.Conn.QueryRow(ctx, `
		SELECT r.rolsuper, r.rolcreatedb, pg_has_role(current_user, d.datdba, 'USAGE')
		FROM pg_roles r, pg_database d
		WHERE r.rolname = current_user AND d.datname = $1`, live).Scan(&super, &createdb, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("database %s does not exist", live)
	}
	if err != nil {
		return err
	}
	if super || (createdb && owner) {
		return nil
	}
	return fmt.Errorf("the database role needs CREATEDB and ownership of %s to swap it", live)
}

func (c *PGControl) Create(ctx context.Context, name string) error {
	var encoding, collate, ctype, provider string
	err := c.Conn.QueryRow(ctx, `SELECT pg_encoding_to_char(encoding), datcollate, datctype, datlocprovider::text FROM pg_database WHERE datname = $1`, c.Like).Scan(&encoding, &collate, &ctype, &provider)
	if err != nil {
		return fmt.Errorf("reading %s's locale: %w", c.Like, err)
	}
	sql := "CREATE DATABASE " + ident(name) + " TEMPLATE template0 ENCODING " + quoteLiteral(encoding)
	if provider == "c" {
		sql += " LOCALE_PROVIDER libc LC_COLLATE " + quoteLiteral(collate) + " LC_CTYPE " + quoteLiteral(ctype)
	}
	_, err = c.Conn.Exec(ctx, sql)
	return err
}

func (c *PGControl) Drop(ctx context.Context, name string) error {
	_, err := c.Conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident(name)+" WITH (FORCE)")
	return err
}

func (c *PGControl) Rename(ctx context.Context, from, to string) error {
	_, err := c.Conn.Exec(ctx, "ALTER DATABASE "+ident(from)+" RENAME TO "+ident(to))
	return err
}

func (c *PGControl) AllowConnections(ctx context.Context, name string, allow bool) error {
	value := "false"
	if allow {
		value = "true"
	}
	_, err := c.Conn.Exec(ctx, "ALTER DATABASE "+ident(name)+" WITH ALLOW_CONNECTIONS "+value)
	return err
}

func (c *PGControl) Terminate(ctx context.Context, name string) error {
	_, err := c.Conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
	return err
}

func (c *PGControl) Close(ctx context.Context) error { return c.Conn.Close(ctx) }

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
