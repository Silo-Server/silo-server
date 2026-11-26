package migrations

import (
	"testing"
)

// The migration that drops the session pre-approval trigger rewrites no row:
// a request the trigger already withdrew stays withdrawn and one still
// approved stays approved; afterwards revoking a session writes no request.
// Its down puts the trigger back working: revoking a session withdraws the
// requests it approved in advance.
func TestDropSessionPreapprovalTriggerUpAndDownPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `CREATE TABLE auth_sessions (id text PRIMARY KEY, revoked_at timestamptz);
		CREATE TABLE device_login_requests (id integer PRIMARY KEY, status text NOT NULL, denied_at timestamptz,
			updated_at timestamptz, preapproved_at timestamptz, preapproved_session_id text);
		INSERT INTO auth_sessions VALUES ('withdrawn', NOW()), ('live', NULL);
		INSERT INTO device_login_requests VALUES (1, 'denied', NOW(), NOW(), NOW(), 'withdrawn'), (2, 'approved', NULL, NOW(), NOW(), 'live')`)
	const migration = "20261126204848_drop_session_preapproval_withdrawal_trigger"
	statuses := func() string {
		t.Helper()
		var s string
		if err := tx.QueryRow(t.Context(), `SELECT string_agg(id || ':' || status, ',' ORDER BY id) FROM device_login_requests`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
	if got := statuses(); got != "1:denied,2:approved" {
		t.Fatalf("after the migration: %s; no row may be rewritten", got)
	}
	migrationExec(t, tx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE id = 'live'`)
	if got := statuses(); got != "1:denied,2:approved" {
		t.Fatalf("revoking a session wrote a request: %s", got)
	}

	migrationExec(t, tx, `UPDATE auth_sessions SET revoked_at = NULL WHERE id = 'live'`)
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	if got := statuses(); got != "1:denied,2:approved" {
		t.Fatalf("after the rollback: %s; no row may be rewritten", got)
	}
	migrationExec(t, tx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE id = 'live'`)
	if got := statuses(); got != "1:denied,2:denied" {
		t.Fatalf("with the trigger back, revoking a session left its request: %s", got)
	}
}
