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

// libpqTarget turns a DATABASE_URL into what pg_dump and pg_restore accept:
// pgx-only parameters removed, an optional database name substituted, and the
// password moved out of the command line (where ps shows it) into PGPASSWORD.
func libpqTarget(databaseURL, dbname string) (conn string, env []string, err error) {
	if !strings.Contains(databaseURL, "://") {
		return libpqKeyValueTarget(databaseURL, dbname)
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", nil, fmt.Errorf("parsing the database URL: %w", err)
	}
	if u.User != nil {
		if password, ok := u.User.Password(); ok {
			env = append(env, "PGPASSWORD="+password)
			u.User = url.User(u.User.Username())
		}
	}
	query := u.Query()
	if query.Has("password") {
		env = append(env, "PGPASSWORD="+query.Get("password"))
		query.Del("password")
	}
	for key := range query {
		if pgxOnlyParam(key) {
			query.Del(key)
		}
	}
	u.RawQuery = query.Encode()
	if dbname != "" {
		u.Path = "/" + dbname
		u.RawPath = ""
	}
	return u.String(), env, nil
}

// libpqKeyValueTarget is libpqTarget for a key=value DSN: it is rewritten
// without the password and the pgx-only parameters, with dbname replaced
// when one is given.
func libpqKeyValueTarget(dsn, dbname string) (conn string, env []string, err error) {
	pairs, err := parseKeyValueDSN(dsn)
	if err != nil {
		return "", nil, err
	}
	parts := make([]string, 0, len(pairs)+1)
	for _, p := range pairs {
		switch {
		case p.key == "password":
			env = append(env, "PGPASSWORD="+p.value)
		case pgxOnlyParam(p.key), p.key == "dbname" && dbname != "":
			// Dropped: libpq rejects the first, and the second is replaced.
		default:
			parts = append(parts, p.key+"="+quoteDSNValue(p.value))
		}
	}
	if dbname != "" {
		parts = append(parts, "dbname="+quoteDSNValue(dbname))
	}
	return strings.Join(parts, " "), env, nil
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

// WithDatabase returns databaseURL pointing at another database on the same
// server, for pgx connections.
func WithDatabase(databaseURL, dbname string) (string, error) {
	if !strings.Contains(databaseURL, "://") {
		return databaseURL + " dbname=" + quoteDSNValue(dbname), nil
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parsing the database URL: %w", err)
	}
	u.Path = "/" + dbname
	u.RawPath = ""
	return u.String(), nil
}

func quoteDSNValue(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}

// databaseName is the database a URL names.
func databaseName(databaseURL string) (string, error) {
	if !strings.Contains(databaseURL, "://") {
		for _, field := range strings.Fields(databaseURL) {
			if name, ok := strings.CutPrefix(field, "dbname="); ok {
				return strings.Trim(name, `'`), nil
			}
		}
		return "", errors.New("the database URL names no database")
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parsing the database URL: %w", err)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return "", errors.New("the database URL names no database")
	}
	return name, nil
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
