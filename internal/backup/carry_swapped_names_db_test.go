package backup

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
)

// TestCarrySwapsDestinationNamesDB: two destinations whose names were
// swapped since the backup are carried in one upsert, which collides on the
// immediate per-profile name constraint part way through unless the restored
// names are staged out of the way first.
func TestCarrySwapsDestinationNamesDB(t *testing.T) {
	live, liveURL := backuptest.Database(t)
	restored, restoredURL := backuptest.Database(t)
	ctx := t.Context()
	var owner int
	if err := live.QueryRow(ctx, `INSERT INTO users (role, username, created_at) VALUES ('user', 'owner', '2026-01-02 03:04:05.678901+00') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	// The same account in the restored data: its id and creation time (sameAccount).
	dbExec(t, restored, `INSERT INTO users (id, role, username, created_at) VALUES ($1, 'user', 'owner', '2026-01-02 03:04:05.678901+00')`, owner)
	destinations := func(db *pgxpool.Pool, first, second string) {
		t.Helper()
		testProfile(t, db, owner, "default", "Default", true, "")
		dbExec(t, db, `INSERT INTO notification_plugin_destinations (id, user_id, profile_id, plugin_id, capability_id, name, config_ciphertext)
			VALUES ('a', $1, 'default', 'example.ntfy', 'ntfy', $2, 'cipher'), ('b', $1, 'default', 'example.ntfy', 'ntfy', $3, 'cipher')`, owner, first, second)
		dbExec(t, db, `INSERT INTO notification_webhooks (id, user_id, profile_id, name, type, url_ciphertext, url_host)
			VALUES ('a', $1, 'default', $2, 'discord', 'cipher', 'example.invalid'), ('b', $1, 'default', $3, 'discord', 'cipher', 'example.invalid')`, owner, first, second)
	}
	destinations(restored, "Phone", "Tablet")
	destinations(live, "Tablet", "Phone")

	if _, err := (PGLoader{}).CarryCredentials(ctx, restoredURL, liveURL); err != nil {
		t.Fatalf("carry over swapped names: %v", err)
	}
	for _, table := range []string{"notification_plugin_destinations", "notification_webhooks"} {
		got, err := collectStrings(ctx, restored, `SELECT id || '=' || name FROM `+table+` ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, []string{"a=Tablet", "b=Phone"}) {
			t.Errorf("restored %s %v, want the live names", table, got)
		}
	}
}
