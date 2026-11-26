package auth

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// sideWriteTables are the tables whose writes must not reach another table,
// or be reached from one, unseen: a trigger, a rule or a foreign-key action
// writing a table as a consequence of a write takes that table's rows in an
// order no Go code chose, which is how ending a session once deadlocked with
// paths that took the same rows the other way round. A direct write of these
// tables by Go code (promotion withdrawing approvals made in advance, a person
// withdrawing one) is not a side write and is not listed here.
var sideWriteTables = []string{"auth_sessions", "device_login_requests", "auth_device_keys", "auth_person_keys", "auth_sign_in_links"}

// allowedSideWrites names each trigger and foreign-key action that writes one
// of sideWriteTables, or writes another table on a write to one, with why it
// is safe.
var allowedSideWrites = map[string]string{
	// Trigger on device_login_requests writing auth_sessions.
	"device_login_requests_bind_remote_playback": "collecting a remote-playback request binds the session the same " +
		"transaction just created and named on the request: no other transaction can hold that session row",

	// Actions when a session is deleted.
	"device_login_requests_auth_session_id_fkey": "deleting a session nulls the request that produced it: session row, " +
		"then request row, the order every path takes",
	"device_login_requests_preapproved_session_id_fkey": "deleting a session nulls the requests it approved in advance: " +
		"session row, then request row, the order every path takes. The hourly expired-session cleanup and an " +
		"account-wide revoke of the same account can still meet over such a pair; the cleanup skips locked " +
		"sessions and its job runs again",
	// Actions when a key is deleted.
	"auth_device_keys_person_key_fkey": "only deleting an account deletes person keys, under the account lock and " +
		"after its device keys",
	"auth_sessions_device_key_fkey": "only deleting an account deletes device keys, under the account lock and after " +
		"locking the account's sessions in id order",
	// Actions when an account is deleted.
	"auth_sessions_user_id_fkey": "only deleting an account deletes it, under the account lock and after locking " +
		"every session of the account, and those viewing as it, in id order",
	"auth_sessions_impersonator_user_id_fkey": "only deleting an account deletes it, after locking the sessions " +
		"viewing as it in id order with the account's own",
	"device_login_requests_approved_by_user_id_fkey": "only deleting an account deletes it, under the account lock, " +
		"which approving and collecting a device sign-in also take first, and after its sessions",
	"auth_person_keys_user_id_fkey": "deleting an account deletes its keys itself, under the account lock, before " +
		"the account row",
	"auth_device_keys_user_id_fkey": "deleting an account deletes its keys itself, under the account lock, before " +
		"the account row",
	// Actions when a provider identity is deleted.
	"auth_sessions_identity_id_fkey": "an identity is deleted under its own lock (unlink) or its installation's " +
		"(uninstall), and every path that writes several of an account's sessions takes the account's identities " +
		"first (lockIdentitiesBeforeSessions); the switch-off sweep takes every installation first",
	"device_login_requests_approved_identity_id_fkey": "collecting a request approved through an identity locks the " +
		"identity before the request row (lockPreapprovalIdentity), the order a deletion takes them",
	// Action when a profile is deleted.
	"device_login_requests_approved_profile_fkey": "deleting a profile takes the account and its identities before " +
		"the profile row, whose deletion clears the requests approved for it",
}

// sqlWrite finds the tables a function body writes.
var sqlWrite = regexp.MustCompile(`(?i)\b(?:insert\s+into|update|delete\s+from)\s+(?:only\s+)?(?:"?public"?\.)?"?([a-z_][a-z0-9_]*)`)

// sqlCall finds the functions a function body calls.
var sqlCall = regexp.MustCompile(`(?i)(?:"?public"?\.)?\b([a-z_][a-z0-9_]*)\s*\(`)

// No trigger, rule or foreign-key action writes one of sideWriteTables as a
// consequence of a write to another table, or another table as a consequence
// of a write to one of them, unless allowedSideWrites names it with its
// reason. It reads the migrated schema, so a migration that adds one fails
// here until the list says why it is safe. A trigger function's own writes
// are read, and so are the writes of each function it calls in this
// database's own schema, one level down; a called function that itself calls
// another of the schema's functions fails as one it cannot classify. It
// cannot see SQL built at run time (EXECUTE of a string) in a function body.
// A listed table that does not exist yet is skipped and reported, unless a
// migration creates it, which fails.
func TestNoUnlistedSideWritesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tables := presentGuardedTables(t, pool)
	seen := map[string]bool{}
	check := func(name, what string) {
		seen[name] = true
		if _, ok := allowedSideWrites[name]; !ok {
			t.Errorf("%s %s; list it in allowedSideWrites with why it is safe, or remove it", name, what)
		}
	}
	checkTriggers(t, pool, tables, check)
	checkForeignKeyActions(t, pool, tables, check)
	var rules []string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(array_agg(tablename || '.' || rulename), '{}') FROM pg_rules
		WHERE schemaname = 'public' AND tablename = ANY($1)`, tables).Scan(&rules); err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		t.Errorf("rule %s rewrites writes to a guarded table; express it in Go or as a listed trigger", rule)
	}
	for _, name := range slices.Sorted(maps.Keys(allowedSideWrites)) {
		if !seen[name] {
			t.Errorf("allowedSideWrites lists %s, which no longer writes another table; remove it", name)
		}
	}
}

// presentGuardedTables answers the guarded tables the schema has. A missing
// one is reported and skipped, or fails when a migration creates it.
func presentGuardedTables(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	var present []string
	if err := pool.QueryRow(t.Context(), `SELECT COALESCE(array_agg(c.relname::text), '{}') FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname = ANY($1)`, sideWriteTables).Scan(&present); err != nil {
		t.Fatal(err)
	}
	migrations, err := filepath.Glob(filepath.Join("..", "..", "migrations", "sql", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range sideWriteTables {
		if slices.Contains(present, table) {
			continue
		}
		creates := regexp.MustCompile(`(?i)create\s+table\s+(?:if\s+not\s+exists\s+)?(?:"?public"?\.)?"?` + table + `\b`)
		for _, path := range migrations {
			if body, err := os.ReadFile(path); err == nil && creates.Match(body) {
				t.Errorf("%s creates %s, but the migrated test database has no such table", filepath.Base(path), table)
			}
		}
		t.Logf("%s does not exist in this schema yet; skipped", table)
	}
	return present
}

// checkTriggers reads every trigger on a guarded table, and every trigger
// whose function writes a guarded table, with the functions it calls one
// level down.
func checkTriggers(t *testing.T, pool *pgxpool.Pool, tables []string, check func(name, what string)) {
	t.Helper()
	ctx := t.Context()
	rows, err := pool.Query(ctx, `SELECT t.tgname, t.tgrelid::regclass::text, pg_get_functiondef(t.tgfoid)
		FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE NOT t.tgisinternal AND n.nspname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	type trigger struct{ name, table, body string }
	var triggers []trigger
	for rows.Next() {
		var tr trigger
		if err := rows.Scan(&tr.name, &tr.table, &tr.body); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, tr)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, tr := range triggers {
		guarded := slices.Contains(tables, tr.table)
		written, unclassified := triggerWrites(ctx, t, pool, tr.body)
		for _, w := range written {
			switch {
			case w == tr.table:
			case guarded:
				check(tr.name, "is a trigger on "+tr.table+" that writes "+w)
			case slices.Contains(tables, w):
				check(tr.name, "is a trigger on "+tr.table+" that writes the guarded table "+w)
			}
		}
		if guarded {
			for _, fn := range unclassified {
				t.Errorf("trigger %s on %s calls %s, which calls further functions of the schema; this test cannot classify it", tr.name, tr.table, fn)
			}
		}
	}
}

// triggerWrites answers the tables body writes, and those the schema's own
// functions it calls write; and the called functions that call further
// functions of the schema, which it does not follow.
func triggerWrites(ctx context.Context, t *testing.T, pool *pgxpool.Pool, body string) (written, unclassified []string) {
	t.Helper()
	add := func(b string) {
		for _, m := range sqlWrite.FindAllStringSubmatch(b, -1) {
			if w := strings.ToLower(m[1]); !slices.Contains(written, w) {
				written = append(written, w)
			}
		}
	}
	add(body)
	for _, name := range schemaFunctionsCalled(ctx, t, pool, body) {
		var def string
		if err := pool.QueryRow(ctx, `SELECT pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = 'public' AND p.proname = $1 AND p.prokind IN ('f', 'p') LIMIT 1`, name).Scan(&def); err != nil {
			t.Fatal(err)
		}
		add(def)
		if len(schemaFunctionsCalled(ctx, t, pool, def)) > 0 {
			unclassified = append(unclassified, name)
		}
	}
	return written, unclassified
}

// schemaFunctionsCalled answers the functions of the public schema body
// calls, other than the function body defines. Aggregates (an extension's
// among them) write nothing and are left out.
func schemaFunctionsCalled(ctx context.Context, t *testing.T, pool *pgxpool.Pool, body string) []string {
	t.Helper()
	var names []string
	for _, m := range sqlCall.FindAllStringSubmatch(body, -1) {
		if n := strings.ToLower(m[1]); !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	defines := regexp.MustCompile(`(?i)create\s+or\s+replace\s+function\s+(?:"?public"?\.)?"?([a-z_][a-z0-9_]*)`).FindStringSubmatch(body)
	var called []string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(array_agg(DISTINCT p.proname::text), '{}') FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public' AND p.prokind IN ('f', 'p') AND p.proname = ANY($1)`, names).Scan(&called); err != nil {
		t.Fatal(err)
	}
	if len(defines) > 1 {
		called = slices.DeleteFunc(called, func(n string) bool { return n == strings.ToLower(defines[1]) })
	}
	return called
}

// checkForeignKeyActions reads every foreign key with an action other than
// NO ACTION or RESTRICT whose child or parent is a guarded table.
func checkForeignKeyActions(t *testing.T, pool *pgxpool.Pool, tables []string, check func(name, what string)) {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT conname, conrelid::regclass::text, confrelid::regclass::text, confdeltype::text, confupdtype::text
		FROM pg_constraint WHERE contype = 'f'
			AND (conrelid::regclass::text = ANY($1) OR confrelid::regclass::text = ANY($1))
			AND (confdeltype NOT IN ('a', 'r') OR confupdtype NOT IN ('a', 'r'))`, tables)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, child, parent, del, upd string
		if err := rows.Scan(&name, &child, &parent, &del, &upd); err != nil {
			t.Fatal(err)
		}
		check(name, "is a foreign key action on "+child+" when "+parent+" rows are deleted ("+del+") or updated ("+upd+")")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
