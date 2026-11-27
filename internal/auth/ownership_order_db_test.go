package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// switchOffAs turns app keys off as the settings write does, in its order:
// the ownership lock (shared) before anything, the settings lock, the Owner
// check for actor, the setting, then the sweep.
func switchOffAs(ctx context.Context, env *keyEnv, actor int) error {
	return pgx.BeginFunc(ctx, env.pool, func(tx pgx.Tx) error {
		if err := LockSwitchWrite(ctx, tx); err != nil {
			return err
		}
		if err := lockServerSettings(ctx, tx); err != nil {
			return err
		}
		if err := CheckSwitchOffActor(ctx, tx, actor); err != nil {
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

// An ownership transfer, a sign-in for the account it moves ownership from,
// and a settings write turning app keys off, started in every order: none
// deadlocks. The transfer takes the ownership lock then account rows; the
// sign-in (by key, or by password for an account that is not break-glass)
// takes the account row then the settings lock; the write takes the
// ownership lock then the settings lock. A row lock on the account and one
// on the Owner's key session hold each party at its second lock, so all
// three hold their first before any is released.
func TestOwnershipTransferSignInAndSwitchOffDB(t *testing.T) {
	env := newKeyEnv(t, "ownorder")
	ctx := t.Context()
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	var formerOwner *int
	_ = env.pool.QueryRow(ctx, `SELECT id FROM users WHERE is_owner`).Scan(&formerOwner)
	if formerOwner != nil {
		if _, err := env.pool.Exec(ctx, `UPDATE users SET is_owner = false WHERE id = $1`, *formerOwner); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = env.pool.Exec(context.Background(), `UPDATE users SET is_owner = true WHERE id = $1`, *formerOwner)
		})
	}
	users := NewUserRepository(env.pool)
	orders := [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	names := [3]string{"transfer", "sign-in", "switch-off"}
	n := 0
	for _, byPassword := range []bool{false, true} {
		for _, order := range orders {
			n++
			label := fmt.Sprintf("ownorder-%d", n)
			sign := map[bool]string{false: "key", true: "password"}[byPassword]
			t.Run(fmt.Sprintf("%s sign-in: %s, %s, %s", sign, names[order[0]], names[order[1]], names[order[2]]), func(t *testing.T) {
				env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
				owner := env.localAccount(t, label+"-owner", models.RoleAdmin)
				next := env.localAccount(t, label+"-next", models.RoleAdmin)
				if _, err := env.pool.Exec(ctx, `UPDATE users SET is_owner = true, break_glass = false WHERE id = $1`, owner.ID); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = env.pool.Exec(context.Background(), `UPDATE users SET is_owner = false WHERE id = ANY($1)`, []int{owner.ID, next.ID})
				})
				_, device, enrolled := env.enroll(t, owner)
				req := env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})

				hold, err := env.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
				if _, err := hold.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, owner.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := hold.Exec(ctx, `SELECT 1 FROM auth_sessions WHERE id = $1 FOR UPDATE`, enrolled.Pair.SessionID); err != nil {
					t.Fatal(err)
				}
				deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				parties := [3]func() error{
					func() error { return users.TransferOwnership(deadline, owner.ID, next.ID) },
					func() error {
						var err error
						if byPassword {
							_, _, err = env.svc.Login(deadline, owner.Username, "correct horse battery", "test", "")
						} else {
							_, err = env.signIn.SignIn(deadline, req)
						}
						if errors.Is(err, ErrAppKeySignInDisabled) {
							return nil // the switch-off won
						}
						return err
					},
					func() error { return switchOffAs(deadline, env, owner.ID) },
				}
				done := make(chan error, 3)
				for i, p := range order {
					go func() {
						done <- fmt.Errorf("%s: %w", names[p], parties[p]())
					}()
					waitForLockWaitersWhile(t, env.pool, i+1, done)
				}
				if err := hold.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
				var failed []string
				for range 3 {
					if err := <-done; errors.Unwrap(err) != nil {
						failed = append(failed, err.Error())
					}
				}
				if len(failed) > 0 {
					t.Fatalf("a party failed (a deadlock aborts one): %s", strings.Join(failed, "; "))
				}
			})
		}
	}
}
