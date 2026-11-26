package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/serversettings"
)

// Turning the app key switch off, through the batch and the single-key
// settings writes and by turning demo mode on, ends every session a key
// opened (a device key's and a granted app's) in the write's transaction,
// and no other session. A write that fails after the sweep ends nothing.
func TestAppKeySwitchWriteEndsKeySessionsDB(t *testing.T) {
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
	keys := []string{config.AuthAppKeySignInSettingKey, demomode.SettingKey, config.BackupDestinationSettingKey}
	saved := map[string]*string{}
	for _, key := range keys {
		var value *string
		_ = pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
		saved[key] = value
	}
	t.Cleanup(func() {
		for key, value := range saved {
			if value == nil {
				_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, key)
			} else {
				_, _ = pool.Exec(context.Background(), `UPDATE server_settings SET value = $2 WHERE key = $1`, key, *value)
			}
		}
	})
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, keys); err != nil {
			t.Fatal(err)
		}
	}

	suffix := fmt.Sprint(time.Now().UnixNano())
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'user', true) RETURNING id`, "keyswitch-"+suffix, "keyswitch-"+suffix+"@example.invalid").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	deviceKey := func(t *testing.T, kind, via string) string {
		t.Helper()
		public := make([]byte, 65)
		if _, err := rand.Read(public); err != nil {
			t.Fatal(err)
		}
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, enrolled_via)
			VALUES ($1, $2, $3, $4, $5, 'Phone', $6)`, id, userID, public, hex.EncodeToString(public[:16]), kind, via); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ownKey, appKey := deviceKey(t, "device", "session"), deviceKey(t, "app", "grant")
	session := func(t *testing.T, via string, key *string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
			VALUES ($1, $2, 'test', NOW() + interval '1 hour', $3, $4)`, id, userID, via, key); err != nil {
			t.Fatal(err)
		}
		return id
	}
	live := func(t *testing.T, id string) bool {
		t.Helper()
		var revoked bool
		if err := pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM auth_sessions WHERE id = $1`, id).Scan(&revoked); err != nil {
			t.Fatal(err)
		}
		return !revoked
	}

	h := &AdminHandler{SettingsRepo: serversettings.NewServerSettingsRepo(pool), pool: pool}
	if existing, err := h.AccountsAtRisk(ctx, auth.ChangeAppKeysOff); err != nil || len(existing) > 0 {
		t.Skipf("accounts that sign in only with keys already in the database (%v): every switch-off would be refused", err)
	}
	// keysOnly makes an account with a key and no password or provider, so
	// turning app keys off is refused, and returns a function removing it.
	keysOnly := func(t *testing.T) func() {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, role, enabled, local_password_login_enabled)
			VALUES ($1, $2, 'user', true, false) RETURNING id`, "keysonly-"+suffix, "keysonly-"+suffix+"@example.invalid").Scan(&id); err != nil {
			t.Fatal(err)
		}
		public := make([]byte, 65)
		if _, err := rand.Read(public); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, enrolled_via)
			VALUES ($1, $2, $3, $4, 'device', 'Phone', 'app_code')`, uuid.NewString(), id, public, hex.EncodeToString(public[:16])); err != nil {
			t.Fatal(err)
		}
		return func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) }
	}
	for _, tc := range []struct {
		name    string
		write   func() error
		failed  bool
		refused bool
	}{
		{name: "batch refused with a keys-only account", refused: true, write: func() error {
			return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", false)
		}},
		{name: "single key refused with a keys-only account", refused: true, write: func() error {
			return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", true)
		}},
		{name: "batch", write: func() error { return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", false) }},
		{name: "single key", write: func() error { return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", true) }},
		{name: "demo mode on", write: func() error { return saveAdminSetting(t, h, demomode.SettingKey, demomode.OnValue, true) }},
		{name: "write refused after the sweep", failed: true, write: func() error {
			// The backup check runs after the sweep and refuses S3 without a
			// bucket: the transaction rolls the sweep back.
			_, err := h.UpdateAdminSettings(ctx, map[string]string{
				config.AuthAppKeySignInSettingKey: "false", config.BackupDestinationSettingKey: config.BackupDestinationS3,
			}, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			own, app := session(t, "app_key", &ownKey), session(t, "app_key", &appKey)
			password, deviceSignIn := session(t, "password", nil), session(t, "device_login", nil)
			if tc.refused {
				remove := keysOnly(t)
				err := tc.write()
				remove()
				requireAdminError(t, err, 409, "last_sign_in_method")
				if !strings.Contains(err.Error(), "keysonly-"+suffix) {
					t.Fatalf("refusal %v does not name the account", err)
				}
				var stored int
				if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM server_settings WHERE key = $1`, config.AuthAppKeySignInSettingKey).Scan(&stored); err != nil || stored != 0 {
					t.Fatalf("refused write stored the setting (%d, %v)", stored, err)
				}
				if !live(t, own) || !live(t, app) {
					t.Fatal("a refused write ended key sessions")
				}
				return
			}
			err := tc.write()
			if tc.failed {
				requireAdminError(t, err, 400, "")
				if !live(t, own) || !live(t, app) {
					t.Fatal("a refused write ended key sessions")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if live(t, own) || live(t, app) {
				t.Errorf("key sessions live after the switch went off: own %v, granted app %v", live(t, own), live(t, app))
			}
			if !live(t, password) || !live(t, deviceSignIn) {
				t.Errorf("other sessions ended: password %v, device sign-in %v", live(t, password), live(t, deviceSignIn))
			}
		})
	}
}
