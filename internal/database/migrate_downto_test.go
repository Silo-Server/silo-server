package database

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/migrations"
)

// TestMigrateDownToRestoresLegacyDisplayPrefs is the rollback rehearsal.
//
// The displayprefs move deletes rows from user_settings that the previous
// binary reads, so a binary-only rollback silently loses every Jellyfin
// client's saved view preferences. This proves the documented recovery —
// --migrate-down-to — actually restores them, and that it reaches the Go
// migrations the standalone goose CLI cannot see.
func TestMigrateDownToRestoresLegacyDisplayPrefs(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	var userID int
	if err := pool.QueryRow(ctx, `
INSERT INTO users (username, email, password_hash, role)
VALUES ('downflag','df@example.com','x','user')
ON CONFLICT (username) DO UPDATE SET email=EXCLUDED.email RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	const key = "jellycompat:displayprefs:usersettings:emby"
	const blob = `{"SortBy":"SortName"}`
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_settings (user_id,key,value) VALUES ($1,$2,$3)
		 ON CONFLICT (user_id,key) DO UPDATE SET value=EXCLUDED.value`, userID, key, blob); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	// Apply the move by re-running it (the migration already ran before the seed).
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("re-up: %v", err)
	}

	// Roll back to just before the displayprefs table migration.
	if err := MigrateDownTo(ctx, pool, migrations.FS, "sql", 20260727212045); err != nil {
		t.Fatalf("MigrateDownTo: %v", err)
	}

	var restored string
	if err := pool.QueryRow(ctx,
		`SELECT value FROM user_settings WHERE user_id=$1 AND key=$2`, userID, key).Scan(&restored); err != nil {
		t.Fatalf("legacy row not restored after down: %v", err)
	}
	if restored != blob {
		t.Errorf("restored=%q want %q", restored, blob)
	}
	t.Logf("down-to restored the legacy row the old binary reads: %v", restored == blob)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
}
