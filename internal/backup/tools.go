package backup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrToolMissing reports that pg_dump or pg_restore is not installed.
	ErrToolMissing = errors.New("pg_dump and pg_restore are not installed on this server")
	// ErrToolTooOld reports a pg_dump older than the database server. pg_dump
	// refuses to dump a newer server, and an older one could miss objects.
	ErrToolTooOld = errors.New("pg_dump is older than the database server")
)

// Tools are the PostgreSQL client programs a backup and a restore run.
type Tools struct {
	PgDump    string
	PgRestore string
	// Major is pg_dump's major version; Version its full version string.
	Major   int
	Version string
}

// pgLibDirs is where Debian's PostgreSQL packages (PGDG and the distribution)
// install each major version's programs.
const pgLibDirs = "/usr/lib/postgresql"

var versionPattern = regexp.MustCompile(`\(PostgreSQL\)\s+(\d+)(?:\.(\d+))?`)

// FindTools picks the newest pg_dump installed, with the pg_restore beside it:
// a versioned Debian directory first, then PATH. Several majors can be
// installed side by side and the PATH wrapper may not pick the newest.
func FindTools(ctx context.Context) (Tools, error) {
	var candidates []string
	if dirs, err := filepath.Glob(filepath.Join(pgLibDirs, "*", "bin")); err == nil {
		candidates = append(candidates, dirs...)
	}
	if path, err := exec.LookPath("pg_dump"); err == nil {
		candidates = append(candidates, filepath.Dir(path))
	}
	var best Tools
	for _, dir := range candidates {
		dump, restore := filepath.Join(dir, "pg_dump"), filepath.Join(dir, "pg_restore")
		if !executable(dump) || !executable(restore) {
			continue
		}
		major, version, err := toolVersion(ctx, dump)
		if err != nil {
			continue
		}
		if major > best.Major {
			best = Tools{PgDump: dump, PgRestore: restore, Major: major, Version: version}
		}
	}
	if best.PgDump == "" {
		return Tools{}, ErrToolMissing
	}
	return best, nil
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func toolVersion(ctx context.Context, path string) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return 0, "", err
	}
	return parseToolVersion(string(out))
}

func parseToolVersion(out string) (int, string, error) {
	match := versionPattern.FindStringSubmatch(out)
	if match == nil {
		return 0, "", fmt.Errorf("unrecognized version output %q", strings.TrimSpace(out))
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, "", err
	}
	return major, strings.TrimSpace(out), nil
}

// CheckToolsFor refuses a pg_dump older than the server's major version.
func CheckToolsFor(tools Tools, serverMajor int) error {
	if tools.PgDump == "" {
		return ErrToolMissing
	}
	if tools.Major < serverMajor {
		return fmt.Errorf("%w: pg_dump %d, server %d; install postgresql-client-%d", ErrToolTooOld, tools.Major, serverMajor, serverMajor)
	}
	return nil
}

// pgxOnlyParams are connection-string parameters pgx understands and libpq
// rejects as unknown.
var pgxOnlyParams = []string{"pool_", "statement_cache_capacity", "description_cache_capacity", "default_query_exec_mode", "min_read_buffer_size"}

// secretParams are the connection parameters that carry a secret: libpqTarget
// keeps them off the command line, where ps shows them, and HasSecret
// refuses them in a command's flags.
var secretParams = []string{paramPassword, paramSSLPassword}

const (
	paramPassword    = "password"
	paramSSLPassword = "sslpassword"
)

// libpqServiceName is the section of the private service file that passes
// pg_dump and pg_restore an sslpassword (libpqConn).
const libpqServiceName = "silo-backup"

// errInlineSSL refuses pgx's sslinline, which puts key material in the
// connection string; libpq cannot read it, and it would reach the command
// line.
var errInlineSSL = errors.New("the database URL uses sslinline, which pg_dump and pg_restore cannot read; give the certificate and key as files")

// libpqTarget turns a DATABASE_URL into what pg_dump and pg_restore accept:
// pgx-only parameters removed, an optional database name substituted, and
// the secretParams moved out into secrets. When an sslpassword is among
// them, conn names libpqServiceName, which carries it.
func libpqTarget(databaseURL, dbname string) (conn string, secrets map[string]string, err error) {
	if dbname != "" {
		if databaseURL, err = WithDatabase(databaseURL, dbname); err != nil {
			return "", nil, err
		}
	}
	secrets = map[string]string{}
	if !strings.Contains(databaseURL, "://") {
		pairs, err := parseKeyValueDSN(databaseURL)
		if err != nil {
			return "", nil, err
		}
		var kept []dsnPair
		for _, p := range pairs {
			switch {
			case p.key == "sslinline":
				return "", nil, errInlineSSL
			case slices.Contains(secretParams, p.key):
				secrets[p.key] = p.value
			case pgxOnlyParam(p.key):
				// libpq rejects it.
			default:
				kept = append(kept, p)
			}
		}
		if _, ok := secrets[paramSSLPassword]; ok {
			kept = append(kept, dsnPair{key: "service", value: libpqServiceName})
		}
		return joinKeyValueDSN(kept), secrets, nil
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", nil, fmt.Errorf("parsing the database URL: %w", err)
	}
	if u.User != nil {
		if password, ok := u.User.Password(); ok {
			secrets[paramPassword] = password
			u.User = url.User(u.User.Username())
		}
	}
	query := u.Query()
	if query.Has("sslinline") {
		return "", nil, errInlineSSL
	}
	for key := range query {
		switch {
		case slices.Contains(secretParams, key):
			secrets[key] = query.Get(key)
			query.Del(key)
		case pgxOnlyParam(key):
			query.Del(key)
		}
	}
	if _, ok := secrets[paramSSLPassword]; ok {
		query.Set("service", libpqServiceName)
	}
	u.RawQuery = query.Encode()
	return u.String(), secrets, nil
}

// libpqConn is a connection for pg_dump and pg_restore: Conn, for
// --dbname, holds no secret. Env passes the password as PGPASSWORD and an
// sslpassword, which libpq reads from no environment variable, through a
// service file only this user can read, which Close removes.
type libpqConn struct {
	Conn string
	Env  []string
	dir  string
}

func newLibpqConn(databaseURL string) (*libpqConn, error) {
	conn, secrets, err := libpqTarget(databaseURL, "")
	if err != nil {
		return nil, err
	}
	c := &libpqConn{Conn: conn}
	if password, ok := secrets[paramPassword]; ok {
		c.Env = append(c.Env, "PGPASSWORD="+password)
	}
	sslPassword, ok := secrets[paramSSLPassword]
	if !ok {
		return c, nil
	}
	// A service file line ends at a newline; a value cannot hold one.
	if strings.ContainsAny(sslPassword, "\r\n") {
		return nil, errors.New("the database URL's sslpassword holds a line break, which a service file cannot carry")
	}
	if c.dir, err = os.MkdirTemp("", "silo-pg-service-"); err != nil {
		return nil, err
	}
	file := filepath.Join(c.dir, "pg_service.conf")
	if err := os.WriteFile(file, []byte("["+libpqServiceName+"]\nsslpassword="+sslPassword+"\n"), 0o600); err != nil {
		c.Close()
		return nil, err
	}
	c.Env = append(c.Env, "PGSERVICEFILE="+file)
	return c, nil
}

// Close removes the service file, once the command using it has exited.
func (c *libpqConn) Close() {
	if c.dir != "" {
		_ = os.RemoveAll(c.dir)
	}
}

// HasSecret reports whether a database URL or key=value DSN carries a
// secret (secretParams, or key material inline). One that does not parse
// counts as carrying one.
func HasSecret(databaseURL string) bool {
	if !strings.Contains(databaseURL, "://") {
		pairs, err := parseKeyValueDSN(databaseURL)
		if err != nil {
			return true
		}
		return slices.ContainsFunc(pairs, func(p dsnPair) bool { return slices.Contains(secretParams, p.key) || p.key == "sslinline" })
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return true
	}
	_, inUser := u.User.Password()
	query := u.Query()
	return inUser || query.Has("sslinline") || slices.ContainsFunc(secretParams, query.Has)
}

func joinKeyValueDSN(pairs []dsnPair) string {
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.key + "=" + quoteDSNValue(p.value)
	}
	return strings.Join(parts, " ")
}

func pgxOnlyParam(key string) bool {
	for _, prefix := range pgxOnlyParams {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

type dsnPair struct{ key, value string }

// parseKeyValueDSN splits a libpq key=value connection string the way libpq
// reads it: whitespace around = is allowed, and a value is either a run of
// non-space characters or single-quoted, with \ escaping the next character
// in both forms.
func parseKeyValueDSN(dsn string) ([]dsnPair, error) {
	malformed := errors.New("the database URL is not a valid key=value connection string")
	var pairs []dsnPair
	s := dsn
	for {
		s = strings.TrimLeft(s, dsnSpace)
		if s == "" {
			return pairs, nil
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, malformed
		}
		key := strings.TrimRight(s[:eq], dsnSpace)
		if key == "" || strings.ContainsAny(key, dsnSpace) {
			return nil, malformed
		}
		s = strings.TrimLeft(s[eq+1:], dsnSpace)
		quoted := strings.HasPrefix(s, "'")
		if quoted {
			s = s[1:]
		}
		var value strings.Builder
		i, closed := 0, !quoted
		for ; i < len(s); i++ {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				i++
				value.WriteByte(s[i])
				continue
			}
			if quoted && c == '\'' {
				closed = true
				i++
				break
			}
			if !quoted && strings.IndexByte(dsnSpace, c) >= 0 {
				break
			}
			value.WriteByte(c)
		}
		if !closed {
			return nil, malformed
		}
		s = s[i:]
		pairs = append(pairs, dsnPair{key: key, value: value.String()})
	}
}

const dsnSpace = " \t\n\r\f\v"

// dbnameParam is the connection parameter naming the database.
const dbnameParam = "dbname"

// serviceParams name a connection service, whose file can name the
// database: a URL using one does not say which database it reaches, so the
// restore cannot name another or know the live one.
var serviceParams = []string{"service", "servicefile"}

func errServiceParam(key string) error {
	return fmt.Errorf("the database URL uses a connection service (%s=); give the host, port, user, and database in the URL instead, so the restore can tell which database it names", key)
}

// WithDatabase returns databaseURL pointing at another database on the same
// server. The database is set once, replacing every place it can be named:
// the URL path and a dbname query parameter, which libpq and pgx let
// override the path, or each dbname of a key=value DSN. A URL using a
// connection service is refused (serviceParams). Other parameters, host
// and port overrides included, are kept: they pick the server, which stays
// the same.
func WithDatabase(databaseURL, dbname string) (string, error) {
	if !strings.Contains(databaseURL, "://") {
		pairs, err := keyValuePairs(databaseURL)
		if err != nil {
			return "", err
		}
		pairs = slices.DeleteFunc(pairs, func(p dsnPair) bool { return p.key == dbnameParam })
		return joinKeyValueDSN(append(pairs, dsnPair{key: dbnameParam, value: dbname})), nil
	}
	u, query, err := parseDatabaseURL(databaseURL)
	if err != nil {
		return "", err
	}
	query.Del(dbnameParam)
	u.RawQuery = query.Encode()
	u.Path = "/" + dbname
	u.RawPath = ""
	return u.String(), nil
}

func quoteDSNValue(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}

// databaseName is the database a URL connects to, read as libpq and pgx
// read it: a dbname query parameter over the URL path, and the last dbname
// of a key=value DSN.
func databaseName(databaseURL string) (string, error) {
	name, err := effectiveDatabase(databaseURL)
	if err == nil && name == "" {
		err = errors.New("the database URL names no database")
	}
	return name, err
}

func effectiveDatabase(databaseURL string) (string, error) {
	if !strings.Contains(databaseURL, "://") {
		pairs, err := keyValuePairs(databaseURL)
		if err != nil {
			return "", err
		}
		name := ""
		for _, p := range pairs {
			if p.key == dbnameParam {
				name = p.value
			}
		}
		return name, nil
	}
	u, query, err := parseDatabaseURL(databaseURL)
	if err != nil {
		return "", err
	}
	switch names := query[dbnameParam]; len(names) {
	case 0:
		return strings.TrimPrefix(u.Path, "/"), nil
	case 1:
		return names[0], nil
	}
	return "", errors.New("the database URL names its database more than once")
}

// keyValuePairs parses a key=value DSN, refusing a connection service.
func keyValuePairs(dsn string) ([]dsnPair, error) {
	pairs, err := parseKeyValueDSN(dsn)
	if err != nil {
		return nil, err
	}
	for _, p := range pairs {
		if slices.Contains(serviceParams, p.key) {
			return nil, errServiceParam(p.key)
		}
	}
	return pairs, nil
}

// parseDatabaseURL parses a database URL and its query, refusing a
// connection service.
func parseDatabaseURL(databaseURL string) (*url.URL, url.Values, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing the database URL: %w", err)
	}
	query := u.Query()
	for _, key := range serviceParams {
		if query.Has(key) {
			return nil, nil, errServiceParam(key)
		}
	}
	return u, query, nil
}

// CheckRestoreTools refuses a pg_restore older than the pg_dump that wrote
// the backup: an older pg_restore cannot read a newer archive.
func CheckRestoreTools(tools Tools, m *Manifest) error {
	if tools.PgRestore == "" {
		return ErrToolMissing
	}
	// An unrecorded version leaves the decision to pg_restore.
	if dumped, _, err := parseToolVersion(m.PgDumpVersion); err == nil && tools.Major < dumped {
		return fmt.Errorf("%w: the backup was written by pg_dump %d and pg_restore here is %d; install postgresql-client-%d", ErrToolTooOld, dumped, tools.Major, dumped)
	}
	return nil
}
