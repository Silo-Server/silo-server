package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// The S3 keys are saved and cleared too, so a case that relies on S3 being
// unconfigured does not depend on what the database already holds.
var switchSettingKeys = []string{config.AuthAppKeySignInSettingKey, demomode.SettingKey, config.BackupDestinationSettingKey,
	config.BackupS3BucketSettingKey, config.BackupS3EndpointSettingKey}

// storedSettingValue reads a stored setting for a test to restore: nil when
// there is no row. Any other read error fails the test, so a cleanup never
// deletes a row it could not read.
func storedSettingValue(t *testing.T, pool *pgxpool.Pool, key string) *string {
	t.Helper()
	var value string
	err := pool.QueryRow(t.Context(), `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading setting %s: %v", key, err)
	}
	return &value
}

func newSwitchWriteFixture(t *testing.T) *switchWriteFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Room for a staged test's blocker, parties and poll on a 4-CPU runner.
	poolConfig.MaxConns = 16
	// A name of this test's own, so a staged wait counts only this test's
	// backends, never another test's waiting on a shared advisory lock.
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "silo-test-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := &switchWriteFixture{pool: pool, suffix: strconv.FormatInt(time.Now().UnixNano(), 10)}
	saved := map[string]*string{}
	for _, key := range switchSettingKeys {
		saved[key] = storedSettingValue(t, pool, key)
	}
	t.Cleanup(func() {
		for key, value := range saved {
			if value == nil {
				_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, key)
			} else {
				_, _ = pool.Exec(context.Background(), `INSERT INTO server_settings (key, value) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, *value)
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
	existing, err := h.AccountsAtRisk(ctx, auth.ChangeAppKeysOff)
	if err != nil {
		t.Fatalf("reading the accounts at risk: %v", err)
	}
	if len(existing) > 0 {
		t.Skipf("%d accounts that sign in only with keys already in the database: every switch-off would be refused", len(existing))
	}
	for i, tc := range []struct {
		name string
		// keysOnly adds an account that signs in only with its keys.
		keysOnly bool
		write    func() error
		// status is the refusal's, or 0 for a write that succeeds.
		status int
		// stored is the switch key a successful write stores and its value;
		// the other switch key stays unset (each case starts with neither).
		stored [2]string
	}{
		{name: "batch refused with a keys-only account", keysOnly: true, status: 409,
			write: func() error { return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", false) }},
		{name: "single key refused with a keys-only account", keysOnly: true, status: 409,
			write: func() error { return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", true) }},
		{name: "batch", stored: [2]string{config.AuthAppKeySignInSettingKey, "false"},
			write: func() error { return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", false) }},
		{name: "single key", stored: [2]string{config.AuthAppKeySignInSettingKey, "false"},
			write: func() error { return saveAdminSetting(t, h, config.AuthAppKeySignInSettingKey, "false", true) }},
		{name: "demo mode on", stored: [2]string{demomode.SettingKey, demomode.OnValue},
			write: func() error { return saveAdminSetting(t, h, demomode.SettingKey, demomode.OnValue, true) }},
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
			requireSwitchStored(t, f.pool, tc.stored[0], tc.stored[1])
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
	existing, err := h.AccountsAtRisk(ctx, auth.ChangeAppKeysOff)
	if err != nil {
		t.Fatalf("reading the accounts at risk: %v", err)
	}
	if len(existing) > 0 {
		t.Skipf("%d accounts that sign in only with keys already in the database", len(existing))
	}
	savedLocal := storedSettingValue(t, f.pool, config.AuthLocalPasswordLoginSettingKey)
	t.Cleanup(func() {
		if savedLocal == nil {
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey)
		} else {
			_, _ = f.pool.Exec(context.Background(), `INSERT INTO server_settings (key, value) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthLocalPasswordLoginSettingKey, *savedLocal)
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

// A stale settings write answers 412 before anything else is judged, whether
// or not it names a switch key (which runs it in a transaction taken before
// the write) and whether or not its values are valid: the precondition is
// checked first, as it always was. Each invalid value is shown invalid with
// no precondition, and each valid one is saved with a current one, as a
// batch and as a single setting.
func TestStaleSettingsWriteAnswersPreconditionFirstDB(t *testing.T) {
	f := newSwitchWriteFixture(t)
	ctx := t.Context()
	h := &AdminHandler{SettingsRepo: serversettings.NewServerSettingsRepo(f.pool), pool: f.pool}
	stale := func(AdminSettingsSnapshot) error {
		return &APIError{Status: http.StatusPreconditionFailed, Code: "precondition_failed", Message: "stale"}
	}
	fresh := func(AdminSettingsSnapshot) error { return nil }
	status := func(err error) int {
		if apiErr, ok := errors.AsType[*APIError](err); ok {
			return apiErr.Status
		}
		return 0
	}
	for _, tc := range []struct {
		name, key, value string
		valid            bool
	}{
		{"a switch key, valid", config.AuthAppKeySignInSettingKey, "true", true},
		{"a switch key, invalid", config.AuthAppKeySignInSettingKey, "maybe", false},
		{"no switch key, valid", config.AuthLocalPasswordLoginSettingKey, "true", true},
		{"no switch key, invalid", config.AuthLocalPasswordLoginSettingKey, "maybe", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.valid {
				if _, err := h.UpdateAdminSettings(ctx, map[string]string{tc.key: tc.value}, nil); status(err) != http.StatusBadRequest {
					t.Fatalf("without a precondition: %v, want a 400 validation error", err)
				}
			}
			if _, err := h.UpdateAdminSettings(ctx, map[string]string{tc.key: tc.value}, stale); status(err) != http.StatusPreconditionFailed {
				t.Fatalf("stale batch: %v, want 412", err)
			}
			if _, err := h.UpdateAdminSetting(ctx, tc.key, tc.value, stale); status(err) != http.StatusPreconditionFailed {
				t.Fatalf("stale single setting: %v, want 412", err)
			}
			if tc.valid {
				// The same write with a current precondition succeeds.
				if _, err := h.UpdateAdminSettings(ctx, map[string]string{tc.key: tc.value}, fresh); err != nil {
					t.Fatalf("fresh batch: %v, want it saved", err)
				}
				if _, err := h.UpdateAdminSetting(ctx, tc.key, tc.value, fresh); err != nil {
					t.Fatalf("fresh single setting: %v, want it saved", err)
				}
			}
		})
	}
}

// requireSwitchStored checks that a successful switch-off stored key with
// value and left the other switch key unset, as the case began.
func requireSwitchStored(t *testing.T, pool *pgxpool.Pool, key, value string) {
	t.Helper()
	for _, k := range []string{config.AuthAppKeySignInSettingKey, demomode.SettingKey} {
		got := storedSettingValue(t, pool, k)
		switch {
		case k == key && (got == nil || *got != value):
			t.Errorf("%s stored %v, want %q", k, got, value)
		case k != key && got != nil:
			t.Errorf("%s stored %q, want it left unset", k, *got)
		}
	}
}

// A guarded write of a switch key takes no second connection while it holds
// its transaction: on a pool of one connection it completes, fresh or stale,
// as a batch and as a single setting. Regression guard: the precondition
// read went through the pool after the write's transaction had taken the
// only connection, so the write waited on itself until its deadline.
func TestGuardedSwitchWriteOnOneConnectionDB(t *testing.T) {
	f := newSwitchWriteFixture(t)
	poolConfig, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = 1
	one, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(one.Close)
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM server_settings WHERE key = ANY($1)`, switchSettingKeys); err != nil {
		t.Fatal(err)
	}
	h := &AdminHandler{SettingsRepo: serversettings.NewServerSettingsRepo(one), pool: one}
	fresh := func(AdminSettingsSnapshot) error { return nil }
	stale := func(AdminSettingsSnapshot) error {
		return &APIError{Status: http.StatusPreconditionFailed, Code: "precondition_failed", Message: "stale"}
	}
	for _, tc := range []struct {
		name  string
		guard func(AdminSettingsSnapshot) error
		want  int
	}{{"fresh", fresh, 0}, {"stale", stale, http.StatusPreconditionFailed}} {
		for _, single := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: " batch", true: " single setting"}[single], func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				var err error
				if single {
					_, err = h.UpdateAdminSetting(ctx, config.AuthAppKeySignInSettingKey, "true", tc.guard)
				} else {
					_, err = h.UpdateAdminSettings(ctx, map[string]string{config.AuthAppKeySignInSettingKey: "true"}, tc.guard)
				}
				switch {
				case tc.want == 0 && err != nil:
					t.Fatalf("fresh write on one connection: %v", err)
				case tc.want != 0:
					if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != tc.want {
						t.Fatalf("stale write on one connection: %v, want %d", err, tc.want)
					}
				}
			})
		}
	}
}
