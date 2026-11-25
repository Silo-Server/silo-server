package auth

import (
	"context"
	"os"
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
	sessionID      string
	requestID      string
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
	f := &signInRowsFixture{pool: pool, sessionID: uuid.NewString(), requestID: uuid.NewString()}
	name := "signin-rows-" + uuid.NewString()
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		_, _ = pool.Exec(ctx, `DELETE FROM device_login_requests WHERE id = $1`, f.requestID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, f.installationID)
		pool.Close()
	})
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'user', true) RETURNING id`, name, name+"@example.test").Scan(&f.userID); err != nil {
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
		VALUES ($1, $2, $3, $4, '00', 'Test device', $5, $6, $7, NOW(), NOW() + interval '10 minutes')`,
		f.requestID, uuid.NewString(), uuid.NewString(), uuid.NewString(), DeviceLoginStatusApproved, f.userID, f.identityID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO abs_sessions (user_id, token_hash, device_id)
		VALUES ($1, $2, 'test') RETURNING id`, f.userID, uuid.NewString()).Scan(&f.absSessionID); err != nil {
		t.Fatal(err)
	}
	return f
}

// mutate runs an administrator's delete (nil input) or update of the account
// in the background; revoke is what the caller's validation asks for.
func (f *signInRowsFixture) mutate(ctx context.Context, input *models.UpdateUserInput, revoke bool) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := NewUserRepository(f.pool).MutateAdminAccount(ctx, f.userID, -1, input,
			func(*models.User, pgx.Tx) (bool, error) { return revoke, nil })
		done <- err
	}()
	return done
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

// raceUninstall holds the account change at the Audiobookshelf session it
// revokes after the login sessions, starts the uninstall and waits until
// the uninstall waits on the account change, then lets the change go on.
// Both must finish. Before the fix the change held the login session the
// uninstall waited for, then waited for an identity or device sign-in
// request the uninstall held: deadlock detected, 500.
func (f *signInRowsFixture) raceUninstall(t *testing.T, input *models.UpdateUserInput, revoke bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	holder, holderPID := f.hold(ctx, t, `SELECT id FROM abs_sessions WHERE id = $1 FOR UPDATE`, f.absSessionID)
	changed := f.mutate(ctx, input, revoke)
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

// Administrators changing an account's sign-ins while another uninstalls the
// plugin behind its provider: both finish. The uninstall deletes the
// identities, then clears device_login_requests.approved_identity_id, then
// auth_sessions.identity_id.
func TestAccountSignInChangesDuringPluginUninstallDB(t *testing.T) {
	t.Run("account delete", func(t *testing.T) {
		f := newSignInRowsFixture(t)
		f.raceUninstall(t, nil, false)
		if n := f.count(t, `SELECT count(*) FROM users WHERE id = $1`, f.userID) +
			f.count(t, `SELECT count(*) FROM auth_sessions WHERE id = $1`, f.sessionID); n != 0 {
			t.Fatalf("%d of the account and session rows remain", n)
		}
	})
	t.Run("account disable", func(t *testing.T) {
		f := newSignInRowsFixture(t)
		disabled := false
		f.raceUninstall(t, &models.UpdateUserInput{Enabled: &disabled}, true)
		if n := f.count(t, `SELECT count(*) FROM users WHERE id = $1 AND NOT enabled`, f.userID) +
			f.count(t, `SELECT count(*) FROM auth_sessions WHERE id = $1 AND revoked_at IS NOT NULL AND identity_id IS NULL`, f.sessionID) +
			f.count(t, `SELECT count(*) FROM device_login_requests WHERE id = $1 AND status = $2 AND approved_identity_id IS NULL`,
				f.requestID, DeviceLoginStatusDenied); n != 3 {
			t.Fatalf("%d of 3: account disabled, session revoked and unlinked, approval withdrawn and unlinked", n)
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
