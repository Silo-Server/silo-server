package migrations

import (
	"strings"
	"testing"
)

func TestDeviceLoginCancellationStatusPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE users (id integer PRIMARY KEY);
CREATE TABLE user_profiles (user_id integer REFERENCES users(id), id text, PRIMARY KEY (user_id, id));
CREATE TABLE auth_sessions (id text PRIMARY KEY);`)
	// The original status constraint and the purpose columns added later.
	for _, name := range []string{
		"073_device_login_requests",
		"20260709233718_add_remote_playback_handoff_to_device_login",
	} {
		data, err := FS.ReadFile("sql/" + name + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(data), "-- +goose Down")
		migrationExec(t, tx, strings.ReplaceAll(up, "public.", schema+"."))
	}
	migrationExec(t, tx, `
INSERT INTO device_login_requests(id,device_code_hash,browser_code_hash,user_code_hash,match_code,device_name,status,expires_at)
SELECT gen_random_uuid(),'d-'||status,'b-'||status,'u-'||status,'42','TV',status,now()+interval '10 minutes'
FROM unnest(ARRAY['pending','approved','denied','consumed']) AS status;`)
	snapshot := func() string {
		t.Helper()
		var rows string
		if err := tx.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(r) ORDER BY device_code_hash)::text FROM device_login_requests r`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := snapshot()
	requireMigrationSQLState(t, tx, `UPDATE device_login_requests SET status='cancelled' WHERE device_code_hash='d-pending'`, "23514")

	const repair = "20261002100747_allow_cancelled_device_login_requests"
	migrationExec(t, tx, adminMigrationSQL(t, repair, schema, false))
	if got := snapshot(); got != before {
		t.Fatalf("migration changed existing requests: before=%s after=%s", before, got)
	}
	migrationExec(t, tx, `UPDATE device_login_requests SET status='cancelled' WHERE device_code_hash='d-pending'`)
	requireMigrationSQLState(t, tx, `UPDATE device_login_requests SET status='invalid' WHERE device_code_hash='d-denied'`, "23514")

	// Versions from this one on write cancelled, so rollback keeps accepting it
	// and leaves withdrawn requests as they are.
	before = snapshot()
	migrationExec(t, tx, adminMigrationSQL(t, repair, schema, true))
	if got := snapshot(); got != before {
		t.Fatalf("rollback changed existing requests: before=%s after=%s", before, got)
	}
	migrationExec(t, tx, `UPDATE device_login_requests SET status='cancelled' WHERE device_code_hash='d-approved'`)
	migrationExec(t, tx, adminMigrationSQL(t, repair, schema, false))
}
