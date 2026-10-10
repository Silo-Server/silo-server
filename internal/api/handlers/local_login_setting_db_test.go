package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestLocalLoginSettingUsesMutationConnectionDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprint(time.Now().UnixNano())
	var id int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username,email,password_hash,role,enabled,break_glass) VALUES ($1,$2,'x','admin',true,true) RETURNING id`, "setting-pool-"+suffix, "setting-pool-"+suffix+"@example.invalid").Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	key := config.AuthLocalPasswordLoginSettingKey
	var saved *string
	var value string
	err = pool.QueryRow(t.Context(), `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	if err == nil {
		saved = new(value)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if saved == nil {
			_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, key)
		} else {
			_, _ = pool.Exec(context.Background(), `UPDATE server_settings SET value = $2 WHERE key = $1`, key, *saved)
		}
	})
	cipher, err := secret.New([]byte("synthetic-setting-test-key-01234567890123456789"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		encrypted bool
		batch     bool
	}{
		{name: "raw single"},
		{name: "raw batch", batch: true},
		{name: "encrypted single", encrypted: true},
		{name: "encrypted batch", encrypted: true, batch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), `INSERT INTO server_settings(key,value) VALUES ($1,'true') ON CONFLICT(key) DO UPDATE SET value = 'true'`, key); err != nil {
				t.Fatal(err)
			}
			var store ServerSettingsStore = catalog.NewServerSettingsRepo(pool)
			if tc.encrypted {
				store = catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher)
			}
			h := &AdminHandler{pool: pool, SettingsRepo: store}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if tc.batch {
				_, err = h.UpdateAdminSettings(ctx, map[string]string{key: "false"}, nil)
			} else {
				_, err = h.UpdateAdminSetting(ctx, key, "false", nil)
			}
			if err != nil {
				t.Fatalf("local-login setting update with one connection: %v", err)
			}
			stored, err := store.Get(ctx, key)
			if err != nil || stored != "false" {
				t.Fatalf("stored local-login setting = %q, error = %v", stored, err)
			}
		})
	}
}
