package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestRestoredRateLimitsUseTheirWritePathDB: restored rate limits are
// written in the restore's transaction with the rate-limit configuration's
// checks: valid limits are saved, and limits the configuration refuses
// fail the item, writing nothing.
func TestRestoredRateLimitsUseTheirWritePathDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "silo_ratelimit_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})
	url, err := backup.WithDatabase(dsn, name)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `CREATE TABLE server_settings (key text PRIMARY KEY, value text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	h := NewRateLimitHandler(catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), nil), nil, nil, nil)
	apply := func(values map[string]string) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		after, err := h.ApplyRateLimitSettingsInTx(ctx, tx, values, nil)
		if err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		after(ctx)
		return nil
	}
	if err := apply(map[string]string{"ratelimit.enabled": "true", "ratelimit.ip.requests_per_minute": "30"}); err != nil {
		t.Fatal(err)
	}
	var minute string
	if err := pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = 'ratelimit.ip.requests_per_minute'`).Scan(&minute); err != nil || minute != "30" {
		t.Fatalf("restored limit = %q (%v), want 30", minute, err)
	}
	if err := apply(map[string]string{"ratelimit.backend": "carrier-pigeon"}); err == nil {
		t.Fatal("a backend the configuration refuses was written")
	}
}
