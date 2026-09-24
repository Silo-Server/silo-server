package nodeconfig

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestWatcherStartsWithUnreadableStagedStorageTransition(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// The session-local table shadows the deployment's server_settings table.
	if _, err := pool.Exec(t.Context(), `CREATE TEMP TABLE server_settings (key text PRIMARY KEY, value text NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	cipher, err := secret.New([]byte(strings.Repeat("s", secret.MinMasterKeyLen)))
	if err != nil {
		t.Fatal(err)
	}
	jwtSecret, err := cipher.Encrypt("active-secret", secret.SettingsAAD("auth.jwt_secret"))
	if err != nil {
		t.Fatal(err)
	}
	const corruptStage = "enc:v1:invalid-transition-envelope"
	for key, value := range map[string]string{
		config.StorageTransitionTargetKey: corruptStage,
		"auth.jwt_secret":                 jwtSecret,
		"server.log_level":                "debug",
	} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, key, value); err != nil {
			t.Fatal(err)
		}
	}

	watcher := NewWatcher(pool, cipher, &cache.NoopEventBus{}, BootstrapOverrides{})
	if err := watcher.Start(t.Context()); err != nil {
		t.Fatalf("config watcher startup failed before blocked transition recovery: %v", err)
	}
	if got := watcher.Config().Auth.JWTSecret; got != "active-secret" {
		t.Fatalf("JWT secret = %q, want decrypted active setting", got)
	}
	if got := watcher.Config().Server.LogLevel; got != "debug" {
		t.Fatalf("log level = %q, want debug", got)
	}
	var retained string
	if err := pool.QueryRow(t.Context(), `SELECT value FROM server_settings WHERE key = $1`, config.StorageTransitionTargetKey).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != corruptStage {
		t.Fatal("watcher changed the unreadable transition stage")
	}

	if _, err := pool.Exec(t.Context(), `INSERT INTO server_settings (key, value) VALUES ('s3.public_secret_key', $1)`, corruptStage); err != nil {
		t.Fatal(err)
	}
	if err := watcher.ForceReload(t.Context()); err == nil {
		t.Fatal("unreadable active storage credentials must still fail the reload")
	}
}
