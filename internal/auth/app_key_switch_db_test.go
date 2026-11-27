package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
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
	waitForLockWaitersWhile(t, pool, n, nil)
}

// waitForLockWaitersWhile is waitForLockWaiters for parties that must all
// wait: a party that answers on returned before n backends wait fails the
// test at once with its answer, rather than when the wait times out.
func waitForLockWaitersWhile(t *testing.T, pool *pgxpool.Pool, n int, returned <-chan error) {
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
		select {
		case err := <-returned:
			t.Fatalf("a party returned before %d backends waited on a lock: %v", n, err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Turning the switch off ends every session a key opened, the person's own
// devices' and a granted app's, and no other session; key sign-in is refused
// while it is off and in demo mode; and turning it back on lets the same
// device key sign in again.
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

	// Back on: the same device key signs in again and its session renews.
	// In demo mode key sign-in is refused as with the setting off. (A key
	// session the sweep missed is ended at its next refresh:
	// TestKeySessionSweepMissedEndsAtRefreshDB drives that through the
	// routes.)
	env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
	again, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device}))
	if err != nil || again.DeviceID != enrolled.DeviceID {
		t.Fatalf("the same device key after the switch came back on = %+v, %v", again, err)
	}
	if _, err := env.svc.Refresh(ctx, again.Pair.RefreshToken); err != nil {
		t.Fatalf("renewing a key session with the switch on = %v", err)
	}
	env.setSetting(t, demomode.SettingKey, demomode.OnValue)
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrAppKeySignInDisabled) {
		t.Errorf("sign-in in demo mode = %v", err)
	}
	env.setSetting(t, demomode.SettingKey, "")
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

// The switch-off sweep and ending all of an account's sessions
// (revokeSignInsInTransaction) take a key session's pre-approved device
// sign-in before the session, so neither order deadlocks when a key session
// holds one. A blocker on the session row lets each side take what it takes
// first and wait; then both must finish within the deadline.
func TestSwitchOffSweepAndAccountRevokeDoNotDeadlockDB(t *testing.T) {
	env := newKeyEnv(t, "keydeadlock")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	ctx := t.Context()
	sweep := func(_ *models.User) func(context.Context) error {
		return func(ctx context.Context) error { return switchOff(ctx, env.pool) }
	}
	revokeAll := func(user *models.User) func(context.Context) error {
		return func(ctx context.Context) error {
			return pgx.BeginFunc(ctx, env.pool, func(tx pgx.Tx) error {
				if _, err := lockUser(ctx, tx, user.ID); err != nil {
					return err
				}
				return revokeSignInsInTransaction(ctx, tx, user.ID, nil)
			})
		}
	}
	for i, order := range []struct {
		name          string
		first, second func(*models.User) func(context.Context) error
	}{
		{"the sweep first", sweep, revokeAll},
		{"ending all of the account's sessions first", revokeAll, sweep},
	} {
		t.Run(order.name, func(t *testing.T) {
			env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
			user := env.localAccount(t, fmt.Sprintf("keydeadlock-%d", i), models.RoleUser)
			_, _, enrolled := env.enroll(t, user)
			session := enrolled.Pair.SessionID
			if _, err := env.pool.Exec(ctx, `INSERT INTO device_login_requests (id, device_code_hash, browser_code_hash, user_code_hash,
					match_code, device_name, status, approved_by_user_id, expires_at, preapproved_at, preapproved_session_id, binding_hash)
				VALUES ($1, $4, $4, $4, '000000', 'TV', 'approved', $2, NOW() + interval '10 minutes', NOW(), $3, 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA')`,
				uuid.NewString(), user.ID, session, uuid.NewString()); err != nil {
				t.Fatal(err)
			}
			hold, err := env.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
			if _, err := hold.Exec(ctx, `SELECT 1 FROM auth_sessions WHERE id = $1 FOR UPDATE`, session); err != nil {
				t.Fatal(err)
			}
			deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			done := make(chan error, 2)
			go func() { done <- order.first(user)(deadline) }()
			waitForLockWaiters(t, env.pool, 1)
			go func() { done <- order.second(user)(deadline) }()
			waitForLockWaiters(t, env.pool, 2)
			if err := hold.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := <-done; err != nil {
					t.Fatalf("one side failed (a deadlock aborts one): %v", err)
				}
			}
			var live int
			if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE id = $1 AND revoked_at IS NULL`, session).Scan(&live); err != nil || live != 0 {
				t.Fatalf("the key session is still live (%d, %v)", live, err)
			}
		})
	}
}

// A switch-off that commits just after a key session's renewal read the
// switch as on still ends the session, in its own sweep, and the renewal
// that slipped through outlives it no further: its new tokens name a revoked
// session, so the access token is refused on first use and the next refresh
// is refused. The switch-off runs in the gap through the renewal's hook.
func TestSwitchOffAfterRenewalReadDB(t *testing.T) {
	env := newKeyEnv(t, "keyrenewrace")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	ctx := t.Context()
	env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
	user := env.localAccount(t, "keyrenewrace-user", models.RoleUser)
	_, _, enrolled := env.enroll(t, user)
	afterRenewalSwitchRead = func() {
		afterRenewalSwitchRead = nil
		if err := switchOff(ctx, env.pool); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterRenewalSwitchRead = nil })
	// The renewal read "on" before the switch-off, so it goes on and mints
	// tokens; extending the swept session finds nothing to extend.
	pair, err := env.svc.Refresh(ctx, enrolled.Pair.RefreshToken)
	if err != nil {
		t.Fatalf("renewal that read the switch on = %v", err)
	}
	if env.sessionLive(t, pair.SessionID, user.ID) {
		t.Fatal("the session the switch-off swept is live")
	}
	if _, err := env.svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("the slipped-through refresh token = %v, want refused", err)
	}
}
