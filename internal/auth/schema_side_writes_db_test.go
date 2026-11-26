package auth

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// sideWriteTables are the tables whose writes must not reach another table
// unseen: a trigger or a foreign-key action writing another table as a
// consequence takes that table's rows in an order no Go code chose, which is
// how ending a session once deadlocked with paths that took the same rows
// the other way round.
var sideWriteTables = []string{"auth_sessions", "device_login_requests", "auth_device_keys", "auth_person_keys", "auth_sign_in_links"}

// allowedSideWrites names each trigger and foreign-key action that does
// write another table on a write to sideWriteTables, with why it is safe.
var allowedSideWrites = map[string]string{
	// Trigger on device_login_requests.
	"device_login_requests_bind_remote_playback": "collecting a remote-playback request binds the session the same " +
		"transaction just created and named on the request: no other transaction can hold that session row",
	// ON DELETE SET NULL on device_login_requests.
	"device_login_requests_auth_session_id_fkey": "deleting a session nulls the request that produced it: session row, " +
		"then request row, the order every path takes",
	"device_login_requests_preapproved_session_id_fkey": "deleting a session nulls the requests it approved in advance: " +
		"session row, then request row, the order every path takes. The hourly expired-session cleanup and an " +
		"account-wide revoke of the same account can still meet over such a pair; the cleanup skips locked " +
		"sessions and its job runs again",
	// ON DELETE SET NULL on auth_device_keys.
	"auth_device_keys_person_key_fkey": "only deleting an account deletes person keys, under the account lock and " +
		"after its device keys",
	// ON DELETE CASCADE on auth_sessions.
	"auth_sessions_device_key_fkey": "only deleting an account deletes device keys, under the account lock and after " +
		"locking the account's sessions in id order",
}

// sqlWrite finds the tables a trigger function writes.
var sqlWrite = regexp.MustCompile(`(?i)\b(?:insert\s+into|update|delete\s+from)\s+(?:only\s+)?(?:"?public"?\.)?"?([a-z_][a-z0-9_]*)`)

// No trigger and no foreign-key action writes another table as a
// consequence of a write to sideWriteTables, unless allowedSideWrites names
// it with its reason. It reads the migrated schema, so a migration that adds
// one fails here until the list says why it is safe.
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
	seen := map[string]bool{}
	check := func(name, what string) {
		seen[name] = true
		if _, ok := allowedSideWrites[name]; !ok {
			t.Errorf("%s %s; list it in allowedSideWrites with why it is safe, or remove it", name, what)
		}
	}

	rows, err := pool.Query(ctx, `SELECT t.tgname, t.tgrelid::regclass::text, pg_get_functiondef(t.tgfoid)
		FROM pg_trigger t WHERE NOT t.tgisinternal AND t.tgrelid::regclass::text = ANY($1)`, sideWriteTables)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, table, body string
		if err := rows.Scan(&name, &table, &body); err != nil {
			t.Fatal(err)
		}
		for _, m := range sqlWrite.FindAllStringSubmatch(body, -1) {
			if written := strings.ToLower(m[1]); written != table {
				check(name, "is a trigger on "+table+" that writes "+written)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	rows, err = pool.Query(ctx, `SELECT conname, conrelid::regclass::text, confrelid::regclass::text, confdeltype::text, confupdtype::text
		FROM pg_constraint WHERE contype = 'f' AND confrelid::regclass::text = ANY($1)
			AND (confdeltype NOT IN ('a', 'r') OR confupdtype NOT IN ('a', 'r'))`, sideWriteTables)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, writes, on, del, upd string
		if err := rows.Scan(&name, &writes, &on, &del, &upd); err != nil {
			t.Fatal(err)
		}
		check(name, "is a foreign key action on "+writes+" when "+on+" rows are deleted ("+del+") or updated ("+upd+")")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for _, name := range slices.Sorted(maps.Keys(allowedSideWrites)) {
		if !seen[name] {
			t.Errorf("allowedSideWrites lists %s, which no longer writes another table; remove it", name)
		}
	}
}
