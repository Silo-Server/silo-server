package auth

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// keySessions stores n live sessions opened by deviceKeyID for userID, in the
// reverse of their id order, so a statement that updates them in the order
// it scans them takes the higher id first. It answers the ids in id order.
func (e *keyEnv) keySessions(t *testing.T, userID int, deviceKeyID string, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	slices.Sort(ids)
	for i := n - 1; i >= 0; i-- {
		if err := e.svc.sessions.Create(t.Context(), models.AuthSession{ID: ids[i], UserID: userID, DeviceName: "phone",
			ExpiresAt: time.Now().Add(time.Hour), DeviceKeyID: &deviceKeyID, OpenedVia: models.SessionOpenedAppKey}); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

// preapprove stores a device sign-in session approved in advance.
func (e *keyEnv) preapprove(t *testing.T, userID int, session string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO device_login_requests (id, device_code_hash, browser_code_hash, user_code_hash,
			match_code, device_name, status, approved_by_user_id, expires_at, preapproved_at, preapproved_session_id, binding_hash)
		VALUES ($1, $4, $4, $4, '000000', 'TV', 'approved', $2, NOW() + interval '10 minutes', NOW(), $3, 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA')`,
		id, userID, session, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	return id
}

// runStaged holds row (a session row), starts first and lets it take what it
// takes before that row and wait, then starts second. When contend, second
// waits too, and the row is released; otherwise (the two share no row)
// second must finish while first still waits. Both must finish within the
// deadline, so neither order deadlocks.
func (e *keyEnv) runStaged(t *testing.T, row string, first, second func(context.Context) error, contend bool) {
	t.Helper()
	ctx := t.Context()
	hold, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	if _, err := hold.Exec(ctx, `SELECT 1 FROM auth_sessions WHERE id = $1 FOR UPDATE`, row); err != nil {
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

// inAccountTx runs end in a transaction holding userID's account row, as
// the account's own paths that end sessions do.
func (e *keyEnv) inAccountTx(userID int, end func(context.Context, pgx.Tx) error) func(context.Context) error {
	return func(ctx context.Context) error {
		return pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			if _, err := lockUser(ctx, tx, userID); err != nil {
				return err
			}
			return end(ctx, tx)
		})
	}
}

// staged is one pair of paths that end sessions, run in both orders.
type staged struct {
	name string
	// set up returns the row to hold and the two sides.
	setup func(t *testing.T, label string) (row string, a, b func(context.Context) error)
}

// The paths that end sessions, staged against each other in both orders on
// the rows they share: none deadlocks. Every path that ends several sessions
// locks them in id order first, and ending a session writes only its row.
func TestSessionEndersStagedDB(t *testing.T) {
	env := newKeyEnv(t, "endstaged")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	sweep := func(ctx context.Context) error { return switchOff(ctx, env.pool) }
	keyed := func(t *testing.T, label string) (*models.User, string, []string) {
		user := env.localAccount(t, label, models.RoleUser)
		_, _, enrolled := env.enroll(t, user)
		return user, enrolled.DeviceID, env.keySessions(t, user.ID, enrolled.DeviceID, 2)
	}
	deleteAccount := func(userID int) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := NewUserRepository(env.pool).MutateAdminAccount(ctx, userID, -1, nil,
				func(*models.User, pgx.Tx) (bool, error) { return true, nil })
			return err
		}
	}
	pairs := []staged{
		{"logout and the sweep", func(t *testing.T, label string) (string, func(context.Context) error, func(context.Context) error) {
			user, _, sessions := keyed(t, label)
			env.preapprove(t, user.ID, sessions[0])
			return sessions[0], func(ctx context.Context) error { return env.svc.Logout(ctx, sessions[0]) }, sweep
		}},
		{"deleting the account and a logout", func(t *testing.T, label string) (string, func(context.Context) error, func(context.Context) error) {
			user, _, sessions := keyed(t, label)
			env.preapprove(t, user.ID, sessions[0])
			return sessions[0], deleteAccount(user.ID), func(ctx context.Context) error {
				// The account may be gone by then, its session with it.
				if err := env.svc.Logout(ctx, sessions[0]); err != nil && !IsSessionNotFound(err) {
					return err
				}
				return nil
			}
		}},
		{"deleting the account and the sweep", func(t *testing.T, label string) (string, func(context.Context) error, func(context.Context) error) {
			user, _, sessions := keyed(t, label)
			env.preapprove(t, user.ID, sessions[0])
			return sessions[0], deleteAccount(user.ID), sweep
		}},
		{"ending all of an account's sessions and the sweep", func(t *testing.T, label string) (string, func(context.Context) error, func(context.Context) error) {
			user, _, sessions := keyed(t, label)
			return sessions[0], env.inAccountTx(user.ID, func(ctx context.Context, tx pgx.Tx) error {
				return revokeSignInsInTransaction(ctx, tx, user.ID, nil)
			}), sweep
		}},
		{"the administrator ending all of an account's sessions and the sweep", func(t *testing.T, label string) (string, func(context.Context) error, func(context.Context) error) {
			user, _, sessions := keyed(t, label)
			admin := env.localAccount(t, label+"-admin", models.RoleAdmin)
			return sessions[0], func(ctx context.Context) error {
				_, err := env.svc.sessions.RevokeAsAdmin(ctx, admin.ID, user.ID, nil)
				return err
			}, sweep
		}},
		{"revoking one device key and the sweep", func(t *testing.T, label string) (string, func(context.Context) error, func(context.Context) error) {
			user, key, sessions := keyed(t, label)
			return sessions[0], func(ctx context.Context) error {
				return NewAppKeyRepository(env.pool).RevokeDeviceKey(ctx, user.ID, key)
			}, sweep
		}},
		{"revoking an account's keys and the sweep", func(t *testing.T, label string) (string, func(context.Context) error, func(context.Context) error) {
			user, _, sessions := keyed(t, label)
			return sessions[0], env.inAccountTx(user.ID, func(ctx context.Context, tx pgx.Tx) error {
				return revokeAccountKeysTx(ctx, tx, user.ID)
			}), sweep
		}},
		{"unlinking an identity and the sweep", func(t *testing.T, _ string) (string, func(context.Context) error, func(context.Context) error) {
			// The provider account's keys are bound to its identity.
			_, enrolled := env.enrollFromProvider(t)
			sessions := env.keySessions(t, env.user.ID, enrolled.DeviceID, 2)
			return sessions[0], env.inAccountTx(env.user.ID, func(ctx context.Context, tx pgx.Tx) error {
				return revokeIdentityKeysTx(ctx, tx, env.user.ID, env.identityID)
			}), sweep
		}},
	}
	for i, p := range pairs {
		for _, aFirst := range []bool{true, false} {
			order := map[bool]string{true: "first", false: "second"}[aFirst]
			t.Run(fmt.Sprintf("%s, the former %s", p.name, order), func(t *testing.T) {
				env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
				row, a, b := p.setup(t, fmt.Sprintf("endstaged-%d-%s", i, order))
				if aFirst {
					env.runStaged(t, row, a, b, true)
				} else {
					env.runStaged(t, row, b, a, true)
				}
			})
		}
	}
}

// Two revokes of one session, with a sign-in approved in advance from it
// committing between them: neither waits on the request, and the request is
// no longer honored once the session has ended.
func TestTwoRevokesAroundAPreapprovalDB(t *testing.T) {
	env := newKeyEnv(t, "tworevokes")
	ctx := t.Context()
	user := env.localAccount(t, "tworevokes-user", models.RoleUser)
	_, _, enrolled := env.enroll(t, user)
	session := enrolled.Pair.SessionID
	hold, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	// No-key-update: the revokes wait on it, while the pre-approval's
	// foreign key check (a key-share lock) does not.
	if _, err := hold.Exec(ctx, `SELECT 1 FROM auth_sessions WHERE id = $1 FOR NO KEY UPDATE`, session); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	done := make(chan error, 2)
	revoke := func() {
		if err := env.svc.Logout(deadline, session); err != nil && !IsSessionNotFound(err) {
			done <- err
			return
		}
		done <- nil
	}
	go revoke()
	waitForLockWaiters(t, env.pool, 1)
	request := env.preapprove(t, user.ID, session)
	go revoke()
	waitForLockWaiters(t, env.pool, 2)
	if err := hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("a revoke failed: %v", err)
		}
	}
	var live bool
	if err := env.pool.QueryRow(ctx, `SELECT `+preapprovalLiveSQL+` FROM device_login_requests r WHERE r.id = $1`, request).Scan(&live); err != nil || live {
		t.Fatalf("the request's approver live = %v (%v), want ended", live, err)
	}
}

// A collect racing the revoke of the session that approved it in advance:
// a revoke committed before the collect reads the approver leaves the
// device no session; run together, either outcome is allowed and neither
// deadlocks.
func TestCollectRacingTheApproversRevokeDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	ctx := t.Context()
	approve := func(label string) (session, code, secret string) {
		t.Helper()
		approver := preapprovalApprover(t, svc, userID, nil, nil)
		secret, binding := preapprovalSecret(t)
		created, err := svc.Preapprove(approver, DevicePreapprovalInput{DeviceName: name(label), Binding: binding}, userID)
		if err != nil {
			t.Fatal(err)
		}
		return ClaimsFromContext(approver).SessionID, created.DeviceCode, secret
	}

	// The collect waits on the account row while the revoke commits.
	session, code, secret := approve("revoked first")
	hold, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	if _, err := hold.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	type polled struct {
		poll *DeviceLoginPollResult
		err  error
	}
	collected := make(chan polled, 1)
	go func() {
		poll, err := svc.Poll(deadline, code, secret)
		collected <- polled{poll, err}
	}()
	waitForLockWaiters(t, pool, 1)
	if err := svc.sessions.Revoke(deadline, session); err != nil {
		t.Fatal(err)
	}
	if err := hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-collected; got.err != nil || got.poll.TokenPair != nil || got.poll.Status != DeviceLoginStatusDenied {
		t.Fatalf("collect after the revoke committed = %+v, %v; want denied with no session", got.poll, got.err)
	}

	// Together: either outcome, no deadlock.
	session, code, secret = approve("together")
	results := make(chan error, 2)
	go func() {
		_, err := svc.Poll(deadline, code, secret)
		results <- err
	}()
	go func() { results <- svc.sessions.Revoke(deadline, session) }()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("collect or revoke failed: %v", err)
		}
	}
}

// A collect judges its approver's session by the clock when it reads it,
// not when its transaction began: the collect waits on the account row, the
// approving session expires meanwhile (its expires_at set to the current
// clock and committed), and once the row is free the collect is refused with
// no session. Read against the transaction's start (NOW()) the approver
// would still be live, and an expired session's approval would sign the
// device in.
func TestCollectReadsApproverAtStatementClockDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	ctx := t.Context()
	approver := preapprovalApprover(t, svc, userID, nil, nil)
	secret, binding := preapprovalSecret(t)
	created, err := svc.Preapprove(approver, DevicePreapprovalInput{DeviceName: name("expiring approver"), Binding: binding}, userID)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	if _, err := hold.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	type polled struct {
		poll *DeviceLoginPollResult
		err  error
	}
	collected := make(chan polled, 1)
	go func() {
		poll, err := svc.Poll(deadline, created.DeviceCode, secret)
		collected <- polled{poll, err}
	}()
	waitForLockWaiters(t, pool, 1)
	if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET expires_at = clock_timestamp() WHERE id = $1`,
		ClaimsFromContext(approver).SessionID); err != nil {
		t.Fatal(err)
	}
	if err := hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-collected; got.err != nil || got.poll.TokenPair != nil || got.poll.Status != DeviceLoginStatusDenied {
		t.Fatalf("collect after the approver expired = %+v, %v; want denied with no session", got.poll, got.err)
	}
}

// Promotion to administrator withdraws every device sign-in the account
// approved that no device has collected, in advance or at the moment (a
// direct write of the request rows, unlike the side writes
// TestNoUnlistedSideWritesDB guards): collecting one afterwards is refused
// with no session. A collect racing the promotion queues on the account row,
// which both take before the request row: with the promotion first the
// collect is refused with no session; with the collect first it signs the
// device in, before the account was an administrator's.
func TestPromotionWithdrawsApprovalsDB(t *testing.T) {
	svc, pool, _, name := deviceLoginTestService(t)
	ctx := t.Context()
	users := NewUserRepository(pool)
	promote := func(ctx context.Context, id int) error {
		_, err := users.MutateAdminAccount(ctx, id, -1, &models.UpdateUserInput{Role: new(models.RoleAdmin)},
			func(*models.User, pgx.Tx) (bool, error) { return false, nil })
		return err
	}
	account := func(label string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
			VALUES ($1, $2, 'x', 'user', true) RETURNING id`, name(label), name(label)+"@example.invalid").Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
		return id
	}
	approvals := []struct {
		name    string
		approve func(t *testing.T, id int, label string) (code, secret string)
	}{
		{"in advance", func(t *testing.T, id int, label string) (string, string) {
			approver := preapprovalApprover(t, svc, id, nil, nil)
			secret, binding := preapprovalSecret(t)
			created, err := svc.Preapprove(approver, DevicePreapprovalInput{DeviceName: name(label), Binding: binding}, id)
			if err != nil {
				t.Fatal(err)
			}
			return created.DeviceCode, secret
		}},
		{"at the moment", func(t *testing.T, id int, label string) (string, string) {
			start, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name(label)})
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}, id); err != nil {
				t.Fatal(err)
			}
			return start.DeviceCode, ""
		}},
	}
	for _, kind := range approvals {
		t.Run(kind.name, func(t *testing.T) {
			// Approve, promote, collect.
			promoted := account(kind.name + " promoted")
			code, secret := kind.approve(t, promoted, kind.name+" before promotion")
			if err := promote(ctx, promoted); err != nil {
				t.Fatal(err)
			}
			if poll, err := svc.Poll(ctx, code, secret); err != nil || poll.TokenPair != nil || poll.Status != DeviceLoginStatusDenied {
				t.Fatalf("collect after promotion = %+v, %v; want denied with no session", poll, err)
			}
			for _, promoteFirst := range []bool{true, false} {
				t.Run(map[bool]string{true: "promotion first", false: "collect first"}[promoteFirst], func(t *testing.T) {
					order := map[bool]string{true: " race-promote", false: " race-collect"}[promoteFirst]
					id := account(kind.name + order)
					code, secret := kind.approve(t, id, kind.name+order+" racing")
					hold, err := pool.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
					if _, err := hold.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, id); err != nil {
						t.Fatal(err)
					}
					deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					type polled struct {
						poll *DeviceLoginPollResult
						err  error
					}
					promoted, collected := make(chan error, 1), make(chan polled, 1)
					returned := make(chan error, 2)
					startPromote := func() {
						go func() {
							err := promote(deadline, id)
							promoted <- err
							returned <- fmt.Errorf("promotion: %v", err)
						}()
					}
					startCollect := func() {
						go func() {
							poll, err := svc.Poll(deadline, code, secret)
							collected <- polled{poll, err}
							returned <- fmt.Errorf("collect: %+v, %v", poll, err)
						}()
					}
					first, second := startCollect, startPromote
					if promoteFirst {
						first, second = startPromote, startCollect
					}
					first()
					waitForLockWaitersWhile(t, pool, 1, returned)
					second()
					waitForLockWaitersWhile(t, pool, 2, returned)
					if err := hold.Rollback(ctx); err != nil {
						t.Fatal(err)
					}
					if err := <-promoted; err != nil {
						t.Fatalf("promotion: %v", err)
					}
					got := <-collected
					if got.err != nil {
						t.Fatalf("collect: %v", got.err)
					}
					if promoteFirst && (got.poll.TokenPair != nil || got.poll.Status != DeviceLoginStatusDenied) {
						t.Fatalf("collect after the promotion committed = %+v; want denied with no session", got.poll)
					}
					if !promoteFirst && got.poll.TokenPair == nil {
						t.Fatalf("collect before the promotion = %+v; want the device signed in", got.poll)
					}
				})
			}
		})
	}
}
