package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
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

// switchWriteFixture is an account with sessions of every kind, and the
// settings the app key switch reads, restored when the test ends.
type switchWriteFixture struct {
	pool           *pgxpool.Pool
	suffix         string
	userID         int
	ownKey, appKey string
}

var switchSettingKeys = []string{config.AuthAppKeySignInSettingKey, demomode.SettingKey, config.BackupDestinationSettingKey}

func newSwitchWriteFixture(t *testing.T) *switchWriteFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := &switchWriteFixture{pool: pool, suffix: strconv.FormatInt(time.Now().UnixNano(), 10)}
	saved := map[string]*string{}
	for _, key := range switchSettingKeys {
		var value *string
		_ = pool.QueryRow(t.Context(), `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
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
	f.userID = f.account(t, "keyswitch", true)
	f.ownKey, f.appKey = f.deviceKey(t, f.userID, "device", "session"), f.deviceKey(t, f.userID, "app", "grant")
	return f
}

// account seeds an enabled account, with a password or with none, removed
// when t ends.
func (f *switchWriteFixture) account(t *testing.T, label string, password bool) int {
	t.Helper()
	var hash *string
	if password {
		hash = new("x")
	}
	var id int
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, enabled, local_password_login_enabled)
		VALUES ($1, $2, $3, 'user', true, $4) RETURNING id`, label+"-"+f.suffix, label+"-"+f.suffix+"@example.invalid", hash, password).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return id
}

func (f *switchWriteFixture) deviceKey(t *testing.T, userID int, kind, via string) string {
	t.Helper()
	public := make([]byte, 65)
	if _, err := rand.Read(public); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, enrolled_via)
		VALUES ($1, $2, $3, $4, $5, 'Phone', $6)`, id, userID, public, hex.EncodeToString(public[:16]), kind, via); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *switchWriteFixture) session(t *testing.T, via string, key *string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
		VALUES ($1, $2, 'test', NOW() + interval '1 hour', $3, $4)`, id, f.userID, via, key); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *switchWriteFixture) live(t *testing.T, id string) bool {
	t.Helper()
	var revoked bool
	if err := f.pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM auth_sessions WHERE id = $1`, id).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	return !revoked
}

func (f *switchWriteFixture) settingStored(t *testing.T) bool {
	t.Helper()
	var stored bool
	if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM server_settings WHERE key = $1)`, config.AuthAppKeySignInSettingKey).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

// Turning the app key switch off, through the batch and the single-key
// settings writes and by turning demo mode on, ends every session a key
// opened (a device key's and a granted app's) in the write's transaction,
// and no other session. It is refused, 409 last_sign_in_method naming the
// account, while an account signs in only with its keys; a refused write,
// or one that fails after the sweep, ends nothing.
func TestAppKeySwitchWriteEndsKeySessionsDB(t *testing.T) {
	f := newSwitchWriteFixture(t)
	ctx := t.Context()
	h := &AdminHandler{SettingsRepo: serversettings.NewServerSettingsRepo(f.pool), pool: f.pool}
	if existing, err := h.AccountsAtRisk(ctx, auth.ChangeAppKeysOff); err != nil || len(existing) > 0 {
		t.Skipf("accounts that sign in only with keys already in the database (%v): every switch-off would be refused", err)
	}
	for i, tc := range []struct {
		name string
		// keysOnly adds an account that signs in only with its keys.
		keysOnly bool
		write    func() error
		// status is the refusal's, or 0 for a write that succeeds.
		status int
	}{
		{name: "batch refused with a keys-only account", keysOnly: true, status: 409,
			write: func() error { return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", false) }},
		{name: "single key refused with a keys-only account", keysOnly: true, status: 409,
			write: func() error { return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", true) }},
		{name: "batch", write: func() error { return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", false) }},
		{name: "single key", write: func() error { return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", true) }},
		{name: "demo mode on", write: func() error { return saveAdminSetting(t, h, demomode.SettingKey, demomode.OnValue, true) }},
		// The backup check runs after the sweep and refuses S3 without a
		// bucket: the transaction rolls the sweep back.
		{name: "write refused after the sweep", status: 400, write: func() error {
			_, err := h.UpdateAdminSettings(ctx, map[string]string{
				config.AuthAppKeySignInSettingKey: "false", config.BackupDestinationSettingKey: config.BackupDestinationS3,
			}, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, switchSettingKeys); err != nil {
				t.Fatal(err)
			}
			own, app := f.session(t, "app_key", &f.ownKey), f.session(t, "app_key", &f.appKey)
			password, deviceSignIn := f.session(t, "password", nil), f.session(t, "device_login", nil)
			if tc.keysOnly {
				f.deviceKey(t, f.account(t, "keysonly"+strconv.Itoa(i), false), "device", "app_code")
			}
			err := tc.write()
			if tc.status != 0 {
				requireAdminError(t, err, tc.status, "")
				if tc.keysOnly && (!strings.Contains(err.Error(), "last_sign_in_method: 1 account sign") ||
					!strings.Contains(err.Error(), "keysonly"+strconv.Itoa(i)+"-"+f.suffix)) {
					t.Fatalf("refusal = %v, want last_sign_in_method naming one account", err)
				}
				if !f.live(t, own) || !f.live(t, app) || f.settingStored(t) {
					t.Fatal("a refused write ended key sessions or stored the setting")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.live(t, own) || f.live(t, app) {
				t.Errorf("key sessions live after the switch went off: own %v, granted app %v", f.live(t, own), f.live(t, app))
			}
			if !f.live(t, password) || !f.live(t, deviceSignIn) {
				t.Errorf("other sessions ended: password %v, device sign-in %v", f.live(t, password), f.live(t, deviceSignIn))
			}
		})
	}
}

// A batch that turns app keys off together with local passwords is judged
// on the local password setting the batch leaves, not the stored one. With
// passwords stored off, an account with a password and a key has no way in
// now, and turning passwords on in the same write gives it its password
// back: the write succeeds. A passwordless key holder judged on the stored
// "off" would look keyless (its key needs local passwords) and slip through;
// on the batch's "on" its keys are its only way in, so the write is refused
// naming it. Turning both off is refused naming it too.
func TestAppKeysOffJudgedOnTheBatchLocalSettingDB(t *testing.T) {
	f := newSwitchWriteFixture(t)
	ctx := t.Context()
	h := &AdminHandler{SettingsRepo: serversettings.NewServerSettingsRepo(f.pool), pool: f.pool}
	if existing, err := h.AccountsAtRisk(ctx, auth.ChangeAppKeysOff); err != nil || len(existing) > 0 {
		t.Skipf("accounts that sign in only with keys already in the database (%v)", err)
	}
	var savedLocal *string
	_ = f.pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey).Scan(&savedLocal)
	t.Cleanup(func() {
		if savedLocal == nil {
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey)
		} else {
			_, _ = f.pool.Exec(context.Background(), `UPDATE server_settings SET value = $2 WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey, *savedLocal)
		}
	})
	// A break-glass admin, so turning local passwords off passes its own
	// check.
	glass := f.account(t, "glass", true)
	if _, err := f.pool.Exec(ctx, `UPDATE users SET role = 'admin', break_glass = true WHERE id = $1`, glass); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		name, storedLocal, batchLocal string
		password                      bool
		refused                       bool
	}{
		{name: "passwords on, keys off, a password and a key", storedLocal: "false", batchLocal: "true", password: true},
		{name: "passwords on, keys off, keys only", storedLocal: "false", batchLocal: "true", refused: true},
		{name: "passwords off, keys off, keys only", storedLocal: "true", batchLocal: "false", refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, switchSettingKeys); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthLocalPasswordLoginSettingKey, tc.storedLocal); err != nil {
				t.Fatal(err)
			}
			label := "holder" + strconv.Itoa(i)
			f.deviceKey(t, f.account(t, label, tc.password), "device", "session")
			_, err := h.UpdateAdminSettings(ctx, map[string]string{
				config.AuthLocalPasswordLoginSettingKey: tc.batchLocal, config.AuthAppKeySignInSettingKey: "false",
			}, nil)
			if !tc.refused {
				if err != nil {
					t.Fatalf("batch = %v, want it saved", err)
				}
				return
			}
			requireAdminError(t, err, 409, "last_sign_in_method")
			if !strings.Contains(err.Error(), label+"-"+f.suffix) {
				t.Fatalf("refusal %v does not name the account", err)
			}
		})
	}
}
