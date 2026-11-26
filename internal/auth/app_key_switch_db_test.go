package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/models"
)

// switchOff turns auth.app_key_sign_in off as the settings write does: the
// settings lock in exclusive mode, the row, and the sweep, in one
// transaction.
func switchOff(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := lockServerSettings(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'false')
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthAppKeySignInSettingKey); err != nil {
			return err
		}
		_, err := EndAppKeySessionsTx(ctx, tx)
		return err
	})
}

// waitForLockWaiters waits until n backends of this database wait on a
// lock: the observable state a race test holds before releasing a blocker.
func waitForLockWaiters(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d backends waiting on a lock, want %d", waiting, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Turning the switch off ends every session a key opened, the person's own
// devices' and a granted app's, and no other session; renewal of a key
// session the sweep missed is refused while the switch is off, in demo mode
// too; and turning it back on lets the same device key sign in again.
func TestAppKeySwitchOffEndsKeySessionsDB(t *testing.T) {
	env := newKeyEnv(t, "keyswitch")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey, demomode.SettingKey))
	ctx := t.Context()
	user := env.localAccount(t, "keyswitch-user", models.RoleUser)
	person, device, enrolled := env.enroll(t, user)
	app := newTestKey(t)
	granted, err := env.redeem(t, app, env.grantEnvelope(t, person, user.ID, app, nil))
	if err != nil {
		t.Fatalf("grant redeem: %v", err)
	}
	password := env.loginSession(t, user, models.SessionOpenedPassword, nil)
	deviceSignIn := env.loginSession(t, user, models.SessionOpenedDeviceLogin, nil)

	if err := switchOff(ctx, env.pool); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"own device key": enrolled.Pair.SessionID, "granted app": granted.Pair.SessionID} {
		if env.sessionLive(t, id, user.ID) {
			t.Errorf("%s session live after the switch went off", name)
		}
	}
	for name, id := range map[string]string{"password": password.SessionID, "device sign-in": deviceSignIn.SessionID} {
		if !env.sessionLive(t, id, user.ID) {
			t.Errorf("%s session ended by the switch", name)
		}
	}

	// Every key path reads the switch under the settings lock.
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrAppKeySignInDisabled) {
		t.Fatalf("sign-in with the switch off = %v", err)
	}

	// A key session left live (a database write that skipped the sweep)
	// does not renew while the switch is off, nor in demo mode.
	env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
	again, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device}))
	if err != nil || again.DeviceID != enrolled.DeviceID {
		t.Fatalf("the same device key after the switch came back on = %+v, %v", again, err)
	}
	for _, off := range []struct{ key, value string }{
		{config.AuthAppKeySignInSettingKey, "false"},
		{demomode.SettingKey, demomode.OnValue},
	} {
		env.setSetting(t, off.key, off.value)
		if _, err := env.svc.Refresh(ctx, again.Pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
			t.Errorf("renewing a key session with %s=%s = %v", off.key, off.value, err)
		}
		if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrAppKeySignInDisabled) {
			t.Errorf("sign-in with %s=%s = %v", off.key, off.value, err)
		}
		env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
		env.setSetting(t, demomode.SettingKey, "")
	}
	if _, err := env.svc.Refresh(ctx, again.Pair.RefreshToken); err != nil {
		t.Fatalf("renewing a key session with the switch on = %v", err)
	}
}

// A key sign-in and a switch-off serialize on the settings lock: either the
// switch-off commits first and the sign-in is refused, opening no session,
// or the sign-in commits first and the sweep ends its session. Nothing a key
// opened survives the switch going off.
func TestKeySignInRacesSwitchOffDB(t *testing.T) {
	env := newKeyEnv(t, "keyrace")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	ctx := t.Context()
	user := env.localAccount(t, "keyrace-user", models.RoleUser)
	_, device, enrolled := env.enroll(t, user)

	// blocker holds a row lock the sign-in needs, so it waits there.
	blocker := func(t *testing.T, query string, arg any) pgx.Tx {
		t.Helper()
		tx, err := env.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		if _, err := tx.Exec(ctx, query, arg); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	signIn := func(req KeyRequest) chan error {
		done := make(chan error, 1)
		go func() {
			_, err := env.signIn.SignIn(ctx, req)
			done <- err
		}()
		return done
	}
	keySessions := func(t *testing.T) (live int) {
		t.Helper()
		if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions
			WHERE device_key_id = $1 AND revoked_at IS NULL`, enrolled.DeviceID).Scan(&live); err != nil {
			t.Fatal(err)
		}
		return live
	}

	t.Run("switch-off first", func(t *testing.T) {
		env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
		req := env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})
		// The sign-in waits on the account row, before the settings lock.
		hold := blocker(t, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, user.ID)
		done := signIn(req)
		waitForLockWaiters(t, env.pool, 1)
		if err := switchOff(ctx, env.pool); err != nil {
			t.Fatal(err)
		}
		if err := hold.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, ErrAppKeySignInDisabled) {
			t.Fatalf("sign-in after the switch-off committed = %v", err)
		}
		if live := keySessions(t); live != 0 {
			t.Fatalf("%d key sessions live", live)
		}
	})

	t.Run("sign-in first", func(t *testing.T) {
		env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
		req := env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})
		// The sign-in holds the account and the settings lock (shared) and
		// waits on its key row; the switch-off then waits on the settings
		// lock.
		hold := blocker(t, `SELECT 1 FROM auth_device_keys WHERE id = $1 FOR UPDATE`, enrolled.DeviceID)
		done := signIn(req)
		waitForLockWaiters(t, env.pool, 1)
		off := make(chan error, 1)
		go func() { off <- switchOff(ctx, env.pool) }()
		waitForLockWaiters(t, env.pool, 2)
		if err := hold.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("sign-in that held the lock first = %v", err)
		}
		if err := <-off; err != nil {
			t.Fatal(err)
		}
		if live := keySessions(t); live != 0 {
			t.Fatalf("%d key sessions survived the switch-off", live)
		}
	})
}
