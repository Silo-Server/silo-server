package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// storeSessions stores n sessions through insert so that their physical
// order (by ctid) is the reverse of their id order: a statement that writes
// them in the order it scans the table, or an index on a key they share
// (which lists equal keys in physical order), reaches the lowest id last,
// whatever free space other tests left in the table. It inserts the highest
// id first and, if the rows did not land in that order, deletes them and
// tries again with new ids. It answers the ids in id order.
func (e *keyEnv) storeSessions(t *testing.T, n int, insert func(t *testing.T, id string)) []string {
	t.Helper()
	for range 20 {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = uuid.NewString()
		}
		slices.Sort(ids)
		for i := n - 1; i >= 0; i-- {
			insert(t, ids[i])
		}
		var physical []string
		if err := e.pool.QueryRow(t.Context(), `SELECT array_agg(id ORDER BY ctid) FROM auth_sessions WHERE id = ANY($1)`, ids).Scan(&physical); err != nil {
			t.Fatal(err)
		}
		reversed := slices.Clone(ids)
		slices.Reverse(reversed)
		if slices.Equal(physical, reversed) {
			return ids
		}
		if _, err := e.pool.Exec(t.Context(), `DELETE FROM auth_sessions WHERE id = ANY($1)`, ids); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("could not store the sessions in the reverse of their id order")
	return nil
}

// requireLocksInIDOrder holds the lowest of sessions from a helper
// connection, starts end, waits until end is blocked by that helper, and
// then takes each other session FOR UPDATE NOWAIT from a third connection:
// all must be free, which they are only while end has locked nothing past
// the lowest, that is, only if it locks in id order. Then it lets end finish.
func (e *keyEnv) requireLocksInIDOrder(t *testing.T, sessions []string, end func(context.Context) error) {
	t.Helper()
	ctx := t.Context()
	hold, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	if _, err := hold.Exec(ctx, `SELECT 1 FROM auth_sessions WHERE id = $1 FOR UPDATE`, sessions[0]); err != nil {
		t.Fatal(err)
	}
	var holder int
	if err := hold.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- end(deadline) }()
	for {
		var blocked bool
		if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid)))`, holder).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the ender finished without waiting on the lowest session: %v", err)
		case <-deadline.Done():
			t.Fatal("the ender never waited on the lowest session")
		case <-time.After(10 * time.Millisecond):
		}
	}
	probe, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var taken []string
	for _, id := range sessions[1:] {
		_, err := probe.Exec(ctx, `SELECT 1 FROM auth_sessions WHERE id = $1 FOR UPDATE NOWAIT`, id)
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "55P03" {
			taken = append(taken, id)
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	_ = probe.Rollback(ctx)
	if err := hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the ender failed: %v", err)
	}
	if len(taken) > 0 {
		t.Fatalf("the ender locked a higher session (%s) before the lowest: it does not lock in id order", taken[0])
	}
}

// Every path that ends or deletes several sessions locks them in id order:
// held at its lowest session, it holds none of the others. Each case gives
// the account eight sessions of the kinds that path takes.
func TestSessionEndersLockInIDOrderDB(t *testing.T) {
	env := newKeyEnv(t, "idorder")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	cases := env.idOrderCases()
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
			c.run(t, fmt.Sprintf("idorder-%d", i))
		})
	}
}

// idOrderCase is one session ender of TestSessionEndersLockInIDOrderDB.
type idOrderCase struct {
	name string
	run  func(t *testing.T, label string)
}

// idOrderCases gives each ender eight sessions of the kinds it takes.
func (e *keyEnv) idOrderCases() []idOrderCase {
	keyed := func(t *testing.T, label string) (*models.User, string) {
		user := e.localAccount(t, label, models.RoleUser)
		_, _, enrolled := e.enroll(t, user)
		return user, enrolled.DeviceID
	}
	return []idOrderCase{
		{"the switch-off sweep", func(t *testing.T, label string) {
			user, key := keyed(t, label)
			ids := e.storeSessions(t, 8, e.sessionOf(user.ID, models.SessionOpenedAppKey, &key, nil))
			e.requireLocksInIDOrder(t, ids, func(ctx context.Context) error { return switchOff(ctx, e.pool) })
		}},
		{"ending all of an account's sessions", func(t *testing.T, label string) {
			user := e.localAccount(t, label, models.RoleUser)
			ids := e.storeSessions(t, 8, e.sessionOf(user.ID, models.SessionOpenedPassword, nil, nil))
			e.requireLocksInIDOrder(t, ids, e.inAccountTx(user.ID, func(ctx context.Context, tx pgx.Tx) error {
				return revokeSignInsInTransaction(ctx, tx, user.ID, nil)
			}))
		}},
		{"the administrator ending all of an account's sessions", func(t *testing.T, label string) {
			user := e.localAccount(t, label, models.RoleUser)
			admin := e.localAccount(t, label+"-admin", models.RoleAdmin)
			ids := e.storeSessions(t, 8, e.sessionOf(user.ID, models.SessionOpenedPassword, nil, nil))
			e.requireLocksInIDOrder(t, ids, func(ctx context.Context) error {
				_, err := e.svc.sessions.RevokeAsAdmin(ctx, admin.ID, user.ID, nil)
				return err
			})
		}},
		{"revoking one device key", func(t *testing.T, label string) {
			user, key := keyed(t, label)
			ids := e.storeSessions(t, 8, e.sessionOf(user.ID, models.SessionOpenedAppKey, &key, nil))
			e.requireLocksInIDOrder(t, ids, func(ctx context.Context) error {
				return NewAppKeyRepository(e.pool).RevokeDeviceKey(ctx, user.ID, key)
			})
		}},
		{"revoking an account's keys", func(t *testing.T, label string) {
			user, key := keyed(t, label)
			ids := e.storeSessions(t, 8, e.sessionOf(user.ID, models.SessionOpenedAppKey, &key, nil))
			e.requireLocksInIDOrder(t, ids, e.inAccountTx(user.ID, func(ctx context.Context, tx pgx.Tx) error {
				return revokeAccountKeysTx(ctx, tx, user.ID)
			}))
		}},
		{"unlinking an identity", func(t *testing.T, _ string) {
			_, enrolled := e.enrollFromProvider(t)
			ids := e.storeSessions(t, 8, e.sessionOf(e.user.ID, models.SessionOpenedAppKey, &enrolled.DeviceID, nil))
			e.requireLocksInIDOrder(t, ids, e.inAccountTx(e.user.ID, func(ctx context.Context, tx pgx.Tx) error {
				return revokeIdentityKeysTx(ctx, tx, e.user.ID, e.identityID)
			}))
		}},
		{"deleting an account", func(t *testing.T, label string) {
			// Password sessions as well as key sessions, the lowest a
			// password one: deleting the keys locks only theirs.
			user, key := keyed(t, label)
			n := 0
			ids := e.storeSessions(t, 8, func(t *testing.T, id string) {
				n++
				if n%2 == 0 {
					e.sessionOf(user.ID, models.SessionOpenedPassword, nil, nil)(t, id)
				} else {
					e.sessionOf(user.ID, models.SessionOpenedAppKey, &key, nil)(t, id)
				}
			})
			e.requireLocksInIDOrder(t, ids, func(ctx context.Context) error {
				_, err := NewUserRepository(e.pool).MutateAdminAccount(ctx, user.ID, -1, nil,
					func(*models.User, pgx.Tx) (bool, error) { return true, nil })
				return err
			})
		}},
		{"replacing a temporary password", func(t *testing.T, label string) {
			user := e.localAccount(t, label, models.RoleUser)
			keep := uuid.NewString()
			e.sessionOf(user.ID, models.SessionOpenedPassword, nil, nil)(t, keep)
			ids := e.storeSessions(t, 8, e.sessionOf(user.ID, models.SessionOpenedPassword, nil, nil))
			var hash string
			if err := e.pool.QueryRow(t.Context(), `SELECT password_hash FROM users WHERE id = $1`, user.ID).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			e.requireLocksInIDOrder(t, ids, func(ctx context.Context) error {
				return NewUserRepository(e.pool).ReplaceTemporaryPassword(ctx, user.ID, hash, "a new long password 9", keep)
			})
		}},
		{"ending sessions viewing as an account after a role change", func(t *testing.T, label string) {
			user := e.localAccount(t, label, models.RoleUser)
			admin := e.localAccount(t, label+"-admin", models.RoleAdmin)
			ids := e.storeSessions(t, 8, e.sessionOf(user.ID, models.SessionOpenedImpersonation, nil, &admin.ID))
			e.requireLocksInIDOrder(t, ids, e.inAccountTx(user.ID, func(ctx context.Context, tx pgx.Tx) error {
				return endImpersonationsInvolving(ctx, tx, user.ID)
			}))
		}},
	}
}

// sessionOf stores a session for userID, opened via via, with the device key
// and impersonator given, under the id it is called with.
func (e *keyEnv) sessionOf(userID int, via string, deviceKey *string, impersonator *int) func(*testing.T, string) {
	return func(t *testing.T, id string) {
		s := models.AuthSession{ID: id, UserID: userID, DeviceName: "device", ExpiresAt: time.Now().Add(time.Hour),
			OpenedVia: via, DeviceKeyID: deviceKey, ImpersonatorUserID: impersonator}
		if impersonator != nil {
			now := time.Now()
			s.ImpersonationStartedAt = &now
		}
		if err := e.svc.sessions.Create(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
}
