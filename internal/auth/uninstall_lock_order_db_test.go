package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// lockOrderEnv is a sign-in provider (signInGuardEnv) with two accounts
// linked to it: an ordinary account with a password (it may unlink) and a
// break-glass admin.
type lockOrderEnv struct {
	*signInGuardEnv
	member, admin *models.User
	memberLink    int64
}

func newLockOrderEnv(t *testing.T) *lockOrderEnv {
	t.Helper()
	e := &lockOrderEnv{signInGuardEnv: newSignInGuardEnv(t)}
	ctx := t.Context()
	e.member = e.localAccount(t, "lock-member", models.RoleUser)
	link, err := insertIdentity(ctx, e.pool, e.member.ID, e.oidc, e.identity("lock-member-idp"), false, false)
	if err != nil {
		t.Fatal(err)
	}
	e.memberLink = link.ID
	e.admin = e.localAccount(t, "lock-admin", models.RoleAdmin)
	e.exec(t, `UPDATE users SET break_glass = true WHERE id = $1`, e.admin.ID)
	if _, err := insertIdentity(ctx, e.pool, e.admin.ID, e.oidc, e.identity("lock-admin-idp"), false, false); err != nil {
		t.Fatal(err)
	}
	return e
}

// begin opens a transaction the test drives by hand, replaying one path's
// lock order with the functions that path calls.
func (e *lockOrderEnv) begin(t *testing.T) (pgx.Tx, int) {
	t.Helper()
	tx, err := e.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var pid int
	if err := tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

// waitBlocked waits until a session other than those in skip runs a query
// containing fragment (any query for "") and is blocked by another session,
// and answers its pid. It reads pg_stat_activity, so it waits on the observable lock wait.
func (e *lockOrderEnv) waitBlocked(t *testing.T, fragment string, skip ...int) int {
	t.Helper()
	for {
		var pid int
		err := e.pool.QueryRow(t.Context(), `SELECT pid FROM pg_stat_activity
			WHERE cardinality(pg_blocking_pids(pid)) > 0 AND position($1 in query) > 0 AND NOT pid = ANY($2)
			LIMIT 1`, fragment, append([]int{0}, skip...)).Scan(&pid)
		if err == nil {
			return pid
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if t.Context().Err() != nil {
			t.Fatalf("no session blocked on %q", fragment)
		}
	}
}

// uninstall runs the plugin uninstall in the background.
func (e *lockOrderEnv) uninstall() <-chan error {
	done := make(chan error, 1)
	go func() { done <- e.installs.Delete(context.Background(), e.oidc) }()
	return done
}

// installed reports whether the provider's installation is still there.
func (e *lockOrderEnv) installed(t *testing.T) bool {
	t.Helper()
	var exists bool
	if err := e.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM plugin_installations WHERE id = $1)`, e.oidc).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// pauseUninstall holds the installation row with a key-share lock, so the
// uninstall's DELETE waits after it has taken the settings lock and the
// installation's identities; the returned function releases it.
func (e *lockOrderEnv) pauseUninstall(t *testing.T) func() {
	t.Helper()
	tx, _ := e.begin(t)
	if _, err := tx.Exec(t.Context(), `SELECT id FROM plugin_installations WHERE id = $1 FOR KEY SHARE`, e.oidc); err != nil {
		t.Fatal(err)
	}
	return func() { _ = tx.Rollback(context.Background()) }
}

func noDeadlock(t *testing.T, side string, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "deadlock") {
		t.Fatalf("%s aborted by Postgres: %v", side, err)
	}
}

// TestUninstallAddsNoLockCycleDB pins that the uninstall, which holds the
// server-settings lock while its cascade removes the installation's
// identities, adds no deadlock with the paths that take an identity and the
// settings lock in either order: unlinking an identity and deleting an
// account (identity, then settings lock) and an account update that ends
// sessions (settings lock, then identities). Each side goes first. Without
// the uninstall's no-wait identity lock, the identity-first paths deadlock
// with it.
func TestUninstallAddsNoLockCycleDB(t *testing.T) {
	// The identity-first paths, replayed in their order. Each takes an
	// identity row, then the settings lock.
	identityFirst := map[string]func(t *testing.T, e *lockOrderEnv, tx pgx.Tx){
		"unlink": func(t *testing.T, e *lockOrderEnv, tx pgx.Tx) {
			if _, err := lockUser(t.Context(), tx, e.member.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := identityForUser(t.Context(), tx, e.member.ID, e.memberLink); err != nil {
				t.Fatal(err)
			}
		},
		"delete a break-glass admin": func(t *testing.T, e *lockOrderEnv, tx pgx.Tx) {
			if _, err := lockUser(t.Context(), tx, e.admin.ID); err != nil {
				t.Fatal(err)
			}
			if err := lockIdentitiesBeforeSessions(t.Context(), tx, e.admin.ID, true); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, hold := range identityFirst {
		t.Run(name+" first", func(t *testing.T) {
			e := newLockOrderEnv(t)
			tx, _ := e.begin(t)
			hold(t, e, tx)
			// The uninstall does not wait for the identity while it holds the
			// settings lock: it gives up with the named answer.
			if err := <-e.uninstall(); !errors.Is(err, plugins.ErrInstallationBusy) {
				t.Fatalf("uninstall = %v, want ErrInstallationBusy", err)
			}
			if !e.installed(t) {
				t.Fatal("a busy uninstall removed the installation")
			}
			// The other request takes the settings lock and finishes.
			if err := lockServerSettings(t.Context(), tx); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := e.installs.Delete(t.Context(), e.oidc); err != nil {
				t.Fatalf("uninstall once the other request ended = %v", err)
			}
		})
	}

	t.Run("uninstall first, then unlink", uninstallFirstThenUnlink)

	t.Run("uninstall first, then delete a break-glass admin", uninstallFirstThenAccountDelete)

	t.Run("account update first", accountUpdateFirst)

	t.Run("uninstall first, then an account update", uninstallFirstThenAccountUpdate)
}

func uninstallFirstThenUnlink(t *testing.T) {
	e := newLockOrderEnv(t)
	release := e.pauseUninstall(t)
	done := e.uninstall()
	uninstallPID := e.waitBlocked(t, "DELETE FROM plugin_installations")
	unlinked := make(chan error, 1)
	go func() {
		unlinked <- NewIdentityService(e.pool).UnlinkOwn(context.Background(), e.member.ID, e.memberLink)
	}()
	e.waitBlocked(t, "", uninstallPID)
	release()
	uninstallErr, unlinkErr := <-done, <-unlinked
	noDeadlock(t, "uninstall", uninstallErr)
	noDeadlock(t, "unlink", unlinkErr)
	if uninstallErr != nil || !errors.Is(unlinkErr, ErrIdentityNotFound) {
		t.Fatalf("uninstall = %v, unlink = %v; want nil and ErrIdentityNotFound", uninstallErr, unlinkErr)
	}
}

func uninstallFirstThenAccountDelete(t *testing.T) {
	e := newLockOrderEnv(t)
	release := e.pauseUninstall(t)
	done := e.uninstall()
	uninstallPID := e.waitBlocked(t, "DELETE FROM plugin_installations")
	tx, _ := e.begin(t)
	deleted := make(chan error, 1)
	go func() {
		deleted <- func() error {
			if _, err := lockUser(context.Background(), tx, e.admin.ID); err != nil {
				return err
			}
			if err := lockIdentitiesBeforeSessions(context.Background(), tx, e.admin.ID, true); err != nil {
				return err
			}
			return lockServerSettings(context.Background(), tx)
		}()
	}()
	e.waitBlocked(t, "", uninstallPID)
	release()
	uninstallErr, deleteErr := <-done, <-deleted
	noDeadlock(t, "uninstall", uninstallErr)
	noDeadlock(t, "account delete", deleteErr)
	if uninstallErr != nil || deleteErr != nil {
		t.Fatalf("uninstall = %v, account delete = %v", uninstallErr, deleteErr)
	}
}

func accountUpdateFirst(t *testing.T) {
	e := newLockOrderEnv(t)
	tx, updatePID := e.begin(t)
	if _, err := lockUser(t.Context(), tx, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := lockServerSettings(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := lockIdentitiesBeforeSessions(t.Context(), tx, e.member.ID, false); err != nil {
		t.Fatal(err)
	}
	done := e.uninstall()
	e.waitBlocked(t, "pg_advisory_xact_lock", updatePID)
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("uninstall after the account update = %v", err)
	}
}

func uninstallFirstThenAccountUpdate(t *testing.T) {
	e := newLockOrderEnv(t)
	release := e.pauseUninstall(t)
	done := e.uninstall()
	uninstallPID := e.waitBlocked(t, "DELETE FROM plugin_installations")
	tx, _ := e.begin(t)
	updated := make(chan error, 1)
	go func() {
		updated <- func() error {
			if _, err := lockUser(context.Background(), tx, e.member.ID); err != nil {
				return err
			}
			if err := lockServerSettings(context.Background(), tx); err != nil {
				return err
			}
			return lockIdentitiesBeforeSessions(context.Background(), tx, e.member.ID, false)
		}()
	}()
	e.waitBlocked(t, "", uninstallPID)
	release()
	uninstallErr, updateErr := <-done, <-updated
	noDeadlock(t, "uninstall", uninstallErr)
	noDeadlock(t, "account update", updateErr)
	if uninstallErr != nil || updateErr != nil {
		t.Fatalf("uninstall = %v, account update = %v", uninstallErr, updateErr)
	}
}
