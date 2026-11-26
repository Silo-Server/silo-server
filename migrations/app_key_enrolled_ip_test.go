package migrations

import "testing"

// Both key tables gain a nullable inet address: a row from before it has
// none, an address that is not one is refused, and rolling back drops the
// column from both.
func TestAppKeyEnrolledIPMigrationUpAndDownPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `CREATE TABLE auth_person_keys (id integer PRIMARY KEY); INSERT INTO auth_person_keys VALUES (1);
		CREATE TABLE auth_device_keys (id integer PRIMARY KEY); INSERT INTO auth_device_keys VALUES (1);`)
	const migration = "20261126061310_record_app_key_enrolled_ip"
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
	for _, table := range []string{"auth_person_keys", "auth_device_keys"} {
		migrationExec(t, tx, `INSERT INTO `+table+` (id, enrolled_ip_address) VALUES (2, '2001:db8::1')`)
		requireMigrationSQLState(t, tx, `INSERT INTO `+table+` (id, enrolled_ip_address) VALUES (3, 'not an address')`, "22P02")
		var unknown bool
		if err := tx.QueryRow(t.Context(), `SELECT enrolled_ip_address IS NULL FROM `+table+` WHERE id = 1`).Scan(&unknown); err != nil || !unknown {
			t.Fatalf("%s: a row from before the column has an address (%v)", table, err)
		}
	}
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	var left int
	if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND column_name = 'enrolled_ip_address'`, schema).Scan(&left); err != nil || left != 0 {
		t.Fatalf("after rollback %d enrolled_ip_address columns remain (%v)", left, err)
	}
}
