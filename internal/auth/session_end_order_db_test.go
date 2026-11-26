package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// preapprovedKeySession is a key session that approved a TV's sign-in in
// advance: ending it writes that request through the
// auth_sessions_withdraw_preapprovals trigger.
type preapprovedKeySession struct {
	user    *models.User
	admin   *models.User
	session string
	refresh string
}

func (e *keyEnv) preapprovedKeySession(t *testing.T, label string) preapprovedKeySession {
	t.Helper()
	user := e.localAccount(t, label, models.RoleUser)
	_, _, enrolled := e.enroll(t, user)
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO device_login_requests (id, device_code_hash, browser_code_hash, user_code_hash,
			match_code, device_name, status, approved_by_user_id, expires_at, preapproved_at, preapproved_session_id, binding_hash)
		VALUES ($1, $4, $4, $4, '000000', 'TV', 'approved', $2, NOW() + interval '10 minutes', NOW(), $3, 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA')`,
		uuid.NewString(), user.ID, enrolled.Pair.SessionID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	return preapprovedKeySession{user: user, admin: e.localAccount(t, label+"-admin", models.RoleAdmin),
		session: enrolled.Pair.SessionID, refresh: enrolled.Pair.RefreshToken}
}

// runStaged holds the session row, starts first and lets it take what it
// takes before that row and wait, then starts second. When contend, second
// waits too, and the row is released; otherwise (the two share no row)
// second must finish while first still waits. Both must finish within the
// deadline, so neither order deadlocks.
func (e *keyEnv) runStaged(t *testing.T, session string, first, second func(context.Context) error, contend bool) {
	t.Helper()
	ctx := t.Context()
	hold, err := e.pool.Begin(ctx)
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
	go func() { done <- first(deadline) }()
	waitForLockWaiters(t, e.pool, 1)
	go func() { done <- second(deadline) }()
	remaining := 2
	if contend {
		waitForLockWaiters(t, e.pool, 2)
	} else {
		if err := <-done; err != nil {
			t.Fatalf("the side sharing no row failed: %v", err)
		}
		remaining = 1
	}
	if err := hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for range remaining {
		if err := <-done; err != nil {
			t.Fatalf("one side failed (a deadlock aborts one): %v", err)
		}
	}
}

// Every path that ends a single session takes the device sign-ins it approved
// in advance before the session row, as the switch-off sweep and ending all
// of an account's sessions do, so none of them deadlocks with the other over
// a key session holding a pre-approval, in either order.
func TestSessionEndersShareOneOrderDB(t *testing.T) {
	env := newKeyEnv(t, "endorder")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	sweep := func(_ preapprovedKeySession) func(context.Context) error {
		return func(ctx context.Context) error { return switchOff(ctx, env.pool) }
	}
	revokers := []struct {
		name string
		end  func(s preapprovedKeySession) func(context.Context) error
	}{
		{"logout", func(s preapprovedKeySession) func(context.Context) error {
			return func(ctx context.Context) error { return env.svc.Logout(ctx, s.session) }
		}},
		{"ending one session from the list", func(s preapprovedKeySession) func(context.Context) error {
			return func(ctx context.Context) error { return env.svc.RevokeSession(ctx, s.session, s.user.ID) }
		}},
		{"the administrator's single revoke", func(s preapprovedKeySession) func(context.Context) error {
			return func(ctx context.Context) error {
				_, err := env.svc.sessions.RevokeAsAdmin(ctx, s.admin.ID, s.user.ID, &s.session)
				return err
			}
		}},
	}
	revokeAll := func(s preapprovedKeySession) func(context.Context) error {
		return func(ctx context.Context) error {
			return pgx.BeginFunc(ctx, env.pool, func(tx pgx.Tx) error {
				if _, err := lockUser(ctx, tx, s.user.ID); err != nil {
					return err
				}
				return revokeSignInsInTransaction(ctx, tx, s.user.ID, nil)
			})
		}
	}
	type pair struct {
		name          string
		first, second func(preapprovedKeySession) func(context.Context) error
		switchOff     bool
	}
	var pairs []pair
	for _, r := range revokers {
		pairs = append(pairs,
			pair{name: r.name + ", then the sweep", first: r.end, second: sweep},
			pair{name: "the sweep, then " + r.name, first: sweep, second: r.end})
	}
	logout := revokers[0].end
	// A renewal with the switch off ends its own session (stored off by
	// hand, so no sweep ran).
	renew := func(s preapprovedKeySession) func(context.Context) error {
		return func(ctx context.Context) error {
			if _, err := env.svc.Refresh(ctx, s.refresh); err != nil && !errors.Is(err, ErrSessionRevoked) {
				return err
			}
			return nil
		}
	}
	pairs = append(pairs,
		pair{name: "a renewal, then its own session's logout", first: renew, second: logout, switchOff: true},
		pair{name: "a logout, then its own session's renewal", first: logout, second: renew, switchOff: true},
		pair{name: "a logout, then ending all of the account's sessions", first: logout, second: revokeAll},
		pair{name: "ending all of the account's sessions, then a logout", first: revokeAll, second: logout})
	for i, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
			s := env.preapprovedKeySession(t, fmt.Sprintf("endorder-%d", i))
			if p.switchOff {
				env.setSetting(t, config.AuthAppKeySignInSettingKey, "false")
			}
			env.runStaged(t, s.session, p.first(s), p.second(s), true)
			var live, approved int
			if err := env.pool.QueryRow(t.Context(), `SELECT
					(SELECT COUNT(*) FROM auth_sessions WHERE id = $1 AND revoked_at IS NULL),
					(SELECT COUNT(*) FROM device_login_requests WHERE preapproved_session_id = $1 AND status = 'approved')`,
				s.session).Scan(&live, &approved); err != nil {
				t.Fatal(err)
			}
			if live != 0 || approved != 0 {
				t.Fatalf("after both: %d live sessions, %d approvals still standing", live, approved)
			}
		})
	}
}

// Ending a session viewing as another account shares no row with the sweep:
// such a session cannot approve a device sign-in in advance and no key opened
// it. While the sweep holds a key session's pre-approval and waits on that
// session's row, ending an impersonation finishes, in either order.
func TestEndImpersonationBesideTheSweepDB(t *testing.T) {
	env := newKeyEnv(t, "endimpersonation")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	for i, sweepFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("sweep first %v", sweepFirst), func(t *testing.T) {
			env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
			s := env.preapprovedKeySession(t, fmt.Sprintf("endimp-%d", i))
			viewing := uuid.NewString()
			if _, err := env.pool.Exec(t.Context(), `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, impersonator_user_id, impersonation_started_at, opened_via)
				VALUES ($1, $2, 'view', NOW() + interval '1 hour', $3, NOW(), 'impersonation')`, viewing, s.user.ID, s.admin.ID); err != nil {
				t.Fatal(err)
			}
			end := func(ctx context.Context) error { return env.svc.EndImpersonation(ctx, viewing, s.admin.ID) }
			sweep := func(ctx context.Context) error { return switchOff(ctx, env.pool) }
			if sweepFirst {
				// The sweep holds the pre-approval and waits on the key
				// session's row.
				env.runStaged(t, s.session, sweep, end, false)
			} else {
				// Ending the impersonation waits on its own session's row.
				env.runStaged(t, viewing, end, sweep, false)
			}
		})
	}
}
