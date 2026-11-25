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

// deleteUninstallFixture is an account signed in through a provider
// identity at a plugin installation: one login session names the identity,
// and one Audiobookshelf session gives a test a row to hold the account
// delete on after it has revoked the login sessions.
type deleteUninstallFixture struct {
	pool           *pgxpool.Pool
	userID         int
	installationID int64
	identityID     int64
	sessionID      string
	absSessionID   int64
}

func newDeleteUninstallFixture(t *testing.T) *deleteUninstallFixture {
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
	f := &deleteUninstallFixture{pool: pool, sessionID: uuid.NewString()}
	name := "delete-uninstall-" + uuid.NewString()
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
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
	if err := pool.QueryRow(ctx, `INSERT INTO abs_sessions (user_id, token_hash, device_id)
		VALUES ($1, $2, 'test') RETURNING id`, f.userID, uuid.NewString()).Scan(&f.absSessionID); err != nil {
		t.Fatal(err)
	}
	return f
}

// deleteAccount deletes the account as an administrator does, in the
// background.
func (f *deleteUninstallFixture) deleteAccount(ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := NewUserRepository(f.pool).MutateAdminAccount(ctx, f.userID, -1, nil,
			func(*models.User, pgx.Tx) (bool, error) { return false, nil })
		done <- err
	}()
	return done
}

// uninstall begins the plugin uninstall's delete on its own connection,
// leaving its transaction open, and returns the connection's backend pid.
func (f *deleteUninstallFixture) uninstall(ctx context.Context, t *testing.T) (pgx.Tx, int, <-chan error) {
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

// blockedBy waits until a backend waits on a lock blockerPID holds, and
// returns its pid. want, when non-zero, names the backend to wait for.
func (f *deleteUninstallFixture) blockedBy(ctx context.Context, t *testing.T, blockerPID, want int) int {
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

// assertGone checks that the account, its identity, its login session and
// the installation are all deleted, as each action alone would leave them.
func (f *deleteUninstallFixture) assertGone(t *testing.T) {
	t.Helper()
	var left int
	if err := f.pool.QueryRow(context.WithoutCancel(t.Context()), `SELECT
		(SELECT count(*) FROM users WHERE id = $1) +
		(SELECT count(*) FROM plugin_auth_identities WHERE id = $2) +
		(SELECT count(*) FROM auth_sessions WHERE id = $3) +
		(SELECT count(*) FROM plugin_installations WHERE id = $4)`,
		f.userID, f.identityID, f.sessionID, f.installationID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d of the account, identity, session and installation rows remain", left)
	}
}

// An administrator deleting an account while another uninstalls the plugin
// behind its sign-in provider: both finish, in either order. The uninstall
// deletes the identities and then clears auth_sessions.identity_id; an account
// delete that revoked the sessions before locking the identities deadlocked
// with it and answered 500.
func TestAccountDeleteDuringPluginUninstallDB(t *testing.T) {
	t.Run("delete first", func(t *testing.T) {
		f := newDeleteUninstallFixture(t)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		// Hold the delete after it has revoked the login sessions, at the
		// Audiobookshelf session it revokes next.
		holder, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
		var holderPID int
		if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
			t.Fatal(err)
		}
		if _, err := holder.Exec(ctx, `SELECT id FROM abs_sessions WHERE id = $1 FOR UPDATE`, f.absSessionID); err != nil {
			t.Fatal(err)
		}
		deleted := f.deleteAccount(ctx)
		deletePID := f.blockedBy(ctx, t, holderPID, 0)
		uninstallTx, uninstallPID, uninstalled := f.uninstall(ctx, t)
		f.blockedBy(ctx, t, deletePID, uninstallPID)
		if err := holder.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		receive(ctx, t, "account delete", deleted)
		receive(ctx, t, "uninstall", uninstalled)
		if err := uninstallTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		f.assertGone(t)
	})
	t.Run("uninstall first", func(t *testing.T) {
		f := newDeleteUninstallFixture(t)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		uninstallTx, uninstallPID, uninstalled := f.uninstall(ctx, t)
		receive(ctx, t, "uninstall", uninstalled)
		deleted := f.deleteAccount(ctx)
		f.blockedBy(ctx, t, uninstallPID, 0)
		if err := uninstallTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		receive(ctx, t, "account delete", deleted)
		f.assertGone(t)
	})
}
