package auth

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// signInRowsFixture is an account signed in through a provider identity at a
// plugin installation: a login session opened through the identity, a device
// sign-in it approved and the device has not collected yet, and an
// Audiobookshelf session that gives a test a row to hold a revocation on
// after it has written the login sessions.
type signInRowsFixture struct {
	pool           *pgxpool.Pool
	userID         int
	installationID int64
	identityID     int64
	adminID        int
	sessionID      string
	requestID      string
	deviceCode     string
	deviceName     string
	absSessionID   int64
}

func newSignInRowsFixture(t *testing.T) *signInRowsFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	name := "signin-rows-" + uuid.NewString()
	f := &signInRowsFixture{pool: pool, sessionID: uuid.NewString(), requestID: uuid.NewString(),
		deviceCode: uuid.NewString(), deviceName: name}
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		_, _ = pool.Exec(ctx, `DELETE FROM device_login_requests WHERE device_name = $1`, f.deviceName)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []int{f.userID, f.adminID})
		_, _ = pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, f.installationID)
		pool.Close()
	})
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'user', true) RETURNING id`, name, name+"@example.test").Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'admin', true) RETURNING id`, name+"-admin", name+"-admin@example.test").Scan(&f.adminID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path)
		VALUES ($1, '1.0.0', $1) RETURNING id`, name).Scan(&f.installationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3) RETURNING id`, f.installationID, name, f.userID).Scan(&f.identityID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, identity_id, device_name, expires_at)
		VALUES ($1, $2, $3, 'Test client', NOW() + interval '1 hour')`, f.sessionID, f.userID, f.identityID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_login_requests (id, device_code_hash, browser_code_hash, user_code_hash, match_code,
			device_name, status, approved_by_user_id, approved_identity_id, approved_at, expires_at)
		VALUES ($1, $2, $3, $4, '00', $5, $6, $7, $8, NOW(), NOW() + interval '10 minutes')`,
		f.requestID, hashDeviceLoginSecret(f.deviceCode), uuid.NewString(), uuid.NewString(), f.deviceName,
		DeviceLoginStatusApproved, f.userID, f.identityID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO abs_sessions (user_id, token_hash, device_id)
		VALUES ($1, $2, 'test') RETURNING id`, f.userID, uuid.NewString()).Scan(&f.absSessionID); err != nil {
		t.Fatal(err)
	}
	return f
}

// mutate is an administrator's delete (nil input) or update of the account;
// revoke is what the caller's validation asks for.
func (f *signInRowsFixture) mutate(input *models.UpdateUserInput, revoke bool) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := NewUserRepository(f.pool).MutateAdminAccount(ctx, f.userID, -1, input,
			func(*models.User, pgx.Tx) (bool, error) { return revoke, nil })
		return err
	}
}

// devices is a device sign-in service over the fixture's database.
func (f *signInRowsFixture) devices() *DeviceLoginService {
	return NewDeviceLoginService(f.pool, NewUserRepository(f.pool), NewJWTService("signin-rows-test-secret", time.Hour, 24*time.Hour),
		NewSessionRepository(f.pool), nil, nil)
}

// uninstall begins the plugin uninstall's delete on its own connection,
// leaving its transaction open, and returns the connection's backend pid.
func (f *signInRowsFixture) uninstall(ctx context.Context, t *testing.T) (pgx.Tx, int, <-chan error) {
	t.Helper()
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Release)
	var pid int
	if err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	done := make(chan error, 1)
	go func() {
		_, err := tx.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, f.installationID)
		done <- err
	}()
	return tx, pid, done
}

// hold locks one row in a transaction of its own and returns the transaction
// and its backend pid.
func (f *signInRowsFixture) hold(ctx context.Context, t *testing.T, query string, args ...any) (pgx.Tx, int) {
	t.Helper()
	holder, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.WithoutCancel(ctx)) })
	var pid int
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
	return holder, pid
}

// blockedBy waits until a backend waits on a lock blockerPID holds, and
// returns its pid. want, when non-zero, names the backend to wait for.
func (f *signInRowsFixture) blockedBy(ctx context.Context, t *testing.T, blockerPID, want int) int {
	t.Helper()
	for {
		var pid int
		if err := f.pool.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity
			WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid))
				AND ($2 = 0 OR pid = $2) LIMIT 1), 0)`, blockerPID, want).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for a backend blocked by %d: %v", blockerPID, ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func receive(ctx context.Context, t *testing.T, what string, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-ctx.Done():
		t.Fatalf("%s did not finish: %v", what, ctx.Err())
	}
}

// count runs a query that counts rows and returns the count.
func (f *signInRowsFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.WithoutCancel(t.Context()), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// raceUninstall holds the account change on the row holdQuery locks, at a
// point after it has locked rows the uninstall needs, starts the uninstall,
// waits until the uninstall waits on the change, then lets the change go on.
// Both must finish. Before the fix the change held a login session or device
// sign-in request the uninstall waited for, then waited for an identity or
// request the uninstall held: deadlock detected, 500.
func (f *signInRowsFixture) raceUninstall(t *testing.T, change func(context.Context) error, holdQuery string, holdArgs ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	holder, holderPID := f.hold(ctx, t, holdQuery, holdArgs...)
	changed := make(chan error, 1)
	go func() { changed <- change(ctx) }()
	changePID := f.blockedBy(ctx, t, holderPID, 0)
	uninstallTx, uninstallPID, uninstalled := f.uninstall(ctx, t)
	f.blockedBy(ctx, t, changePID, uninstallPID)
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	receive(ctx, t, "account change", changed)
	receive(ctx, t, "uninstall", uninstalled)
	if err := uninstallTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM plugin_installations WHERE id = $1`, f.installationID) +
		f.count(t, `SELECT count(*) FROM plugin_auth_identities WHERE id = $1`, f.identityID); n != 0 {
		t.Fatalf("%d of the installation and identity rows remain", n)
	}
}

const holdABSSession = `SELECT id FROM abs_sessions WHERE id = $1 FOR UPDATE`

// revoked counts the fixture's rows a sign-out everywhere ends, after the
// uninstall unlinked them: the login session revoked and the device sign-in
// approval withdrawn.
func (f *signInRowsFixture) revoked(t *testing.T) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM auth_sessions WHERE id = $1 AND revoked_at IS NOT NULL AND identity_id IS NULL`, f.sessionID) +
		f.count(t, `SELECT count(*) FROM device_login_requests WHERE id = $1 AND status = $2 AND approved_identity_id IS NULL`,
			f.requestID, DeviceLoginStatusDenied)
}

// Administrators changing an account's sign-ins while another uninstalls the
// plugin behind its provider: both finish. The uninstall deletes the
// identities, then clears device_login_requests.approved_identity_id, then
// auth_sessions.identity_id.
func TestAccountSignInChangesDuringPluginUninstallDB(t *testing.T) {
	t.Run("account delete", func(t *testing.T) {
		f := newSignInRowsFixture(t)
		f.raceUninstall(t, f.mutate(nil, false), holdABSSession, f.absSessionID)
		if n := f.count(t, `SELECT count(*) FROM users WHERE id = $1`, f.userID) +
			f.count(t, `SELECT count(*) FROM auth_sessions WHERE id = $1`, f.sessionID); n != 0 {
			t.Fatalf("%d of the account and session rows remain", n)
		}
	})
	t.Run("account disable", func(t *testing.T) {
		f := newSignInRowsFixture(t)
		disabled := false
		f.raceUninstall(t, f.mutate(&models.UpdateUserInput{Enabled: &disabled}, true), holdABSSession, f.absSessionID)
		if f.count(t, `SELECT count(*) FROM users WHERE id = $1 AND NOT enabled`, f.userID) != 1 || f.revoked(t) != 2 {
			t.Fatal("want the account disabled, its session revoked and its approval withdrawn, unlinked")
		}
	})
	t.Run("administrator signs the account out everywhere", func(t *testing.T) {
		f := newSignInRowsFixture(t)
		f.raceUninstall(t, func(ctx context.Context) error {
			_, err := NewSessionRepository(f.pool).RevokeAsAdmin(ctx, f.adminID, f.userID, nil)
			return err
		}, holdABSSession, f.absSessionID)
		if f.revoked(t) != 2 {
			t.Fatal("want the session revoked and the approval withdrawn, unlinked")
		}
	})
	t.Run("device sign-in approved from a provider session", func(t *testing.T) {
		f := newSignInRowsFixture(t)
		devices := f.devices()
		start, err := devices.Start(t.Context(), DeviceLoginStartInput{DeviceName: f.deviceName, ClientPurpose: DeviceLoginPurposeLogin})
		if err != nil {
			t.Fatal(err)
		}
		claims := &Claims{UserID: f.userID, SessionID: f.sessionID, TokenType: TokenTypeAccess}
		// Held at the pending request, after the approval locked its session.
		f.raceUninstall(t, func(ctx context.Context) error {
			return devices.Approve(WithClaims(ctx, claims), DeviceLoginLookupInput{UserCode: start.UserCode}, f.userID)
		}, `SELECT id FROM device_login_requests WHERE device_name = $1 AND status = $2 FOR UPDATE`,
			f.deviceName, DeviceLoginStatusPending)
		if f.count(t, `SELECT count(*) FROM device_login_requests WHERE device_name = $1 AND id <> $2 AND status = $3 AND approved_by_user_id = $4`,
			f.deviceName, f.requestID, DeviceLoginStatusApproved, f.userID) != 1 {
			t.Fatal("the device sign-in was not approved")
		}
	})
	t.Run("device collects a sign-in approved through the provider", func(t *testing.T) {
		f := newSignInRowsFixture(t)
		// Hold the collect after it locked the request, as its session
		// insert starts: a trigger waits on an advisory lock the test holds.
		key := int64(uuid.New().ID())
		fn := "test_hold_session_insert_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		if _, err := f.pool.Exec(t.Context(), `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN PERFORM pg_advisory_xact_lock(`+strconv.FormatInt(key, 10)+`); RETURN NEW; END $$;
			CREATE TRIGGER `+fn+` BEFORE INSERT ON auth_sessions FOR EACH ROW
				WHEN (NEW.device_name = '`+f.deviceName+`') EXECUTE FUNCTION `+fn+`()`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = f.pool.Exec(context.WithoutCancel(t.Context()), `DROP TRIGGER IF EXISTS `+fn+` ON auth_sessions; DROP FUNCTION IF EXISTS `+fn+`()`)
		})
		devices := f.devices()
		f.raceUninstall(t, func(ctx context.Context) error {
			_, err := devices.Poll(ctx, f.deviceCode, "")
			return err
		}, `SELECT pg_advisory_xact_lock($1)`, key)
		if f.count(t, `SELECT count(*) FROM device_login_requests WHERE id = $1 AND status = $2`, f.requestID, DeviceLoginStatusConsumed) != 1 {
			t.Fatal("the device did not collect its sign-in")
		}
	})
}

// The expired-session cleanup deletes sessions before the device sign-in
// requests that name them, the reverse of a plugin uninstall, so it must
// never wait on a request another transaction holds: it leaves that session
// for its next run.
func TestExpiredSessionCleanupSkipsHeldRequestDB(t *testing.T) {
	f := newSignInRowsFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx, `UPDATE auth_sessions SET expires_at = NOW() - interval '1 minute' WHERE id = $1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE device_login_requests SET status = $2, auth_session_id = $3, consumed_at = NOW(),
		binding_checked = TRUE WHERE id = $1`, f.requestID, DeviceLoginStatusConsumed, f.sessionID); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionRepository(f.pool)
	holder, _ := f.hold(ctx, t, `SELECT id FROM device_login_requests WHERE id = $1 FOR UPDATE`, f.requestID)
	waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	_, err := sessions.DeleteExpired(waitCtx)
	waitCancel()
	if err != nil {
		t.Fatalf("cleanup with the request held: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM auth_sessions WHERE id = $1`, f.sessionID) != 1 {
		t.Fatal("cleanup deleted the session whose request another transaction holds")
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.DeleteExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM auth_sessions WHERE id = $1`, f.sessionID) != 0 {
		t.Fatal("cleanup kept the expired session once the request was free")
	}
}
