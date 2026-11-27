package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A session app code an account issued while it was ordinary dies with its
// promotion to administrator: redeeming it afterwards is refused and enrolls
// no key. A redeem racing the promotion queues on the account row, which both
// take first: with the promotion first the redeem is refused and no key or
// session exists; with the redeem first it enrolls, and the promotion then
// revokes the keys and ends the session they opened, so neither outlives the
// promotion either way.
func TestPromotionEndsSessionAppCodesDB(t *testing.T) {
	env := newKeyEnv(t, "promotecode")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	ctx := t.Context()
	promote := func(ctx context.Context, id int) error {
		_, err := NewUserRepository(env.pool).MutateAdminAccount(ctx, id, -1, &models.UpdateUserInput{Role: new(models.RoleAdmin)},
			func(*models.User, pgx.Tx) (bool, error) { return false, nil })
		return err
	}
	type redeemCase struct {
		user    *models.User
		request KeyRequest
		person  testKey
	}
	issue := func(t *testing.T, label string) redeemCase {
		t.Helper()
		env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
		user := env.localAccount(t, label, models.RoleUser)
		issued, err := env.signIn.IssueSessionAppCode(ctx, env.loginSession(t, user, models.SessionOpenedPassword, nil), ActingProfile{})
		if err != nil {
			t.Fatal(err)
		}
		person, device := newTestKey(t), newTestKey(t)
		return redeemCase{user: user, person: person,
			request: env.request(t, message{purpose: appkeys.PurposeRedeem, signer: person, device: &device, fields: map[string]any{"code": issued.Code}})}
	}
	redeem := func(ctx context.Context, c redeemCase) error {
		_, err := env.signIn.RedeemAppCode(ctx, KeyRedeemRequest{KeyRequest: c.request, PersonKey: c.person.pub})
		return err
	}
	// Nothing a key gave the account outlives the promotion.
	requireNoKeys := func(t *testing.T, userID int) {
		t.Helper()
		var keys, sessions int
		if err := env.pool.QueryRow(ctx, `SELECT
			(SELECT COUNT(*) FROM auth_person_keys WHERE user_id = $1 AND revoked_at IS NULL)
			+ (SELECT COUNT(*) FROM auth_device_keys WHERE user_id = $1 AND revoked_at IS NULL),
			(SELECT COUNT(*) FROM auth_sessions WHERE user_id = $1 AND device_key_id IS NOT NULL AND revoked_at IS NULL)`,
			userID).Scan(&keys, &sessions); err != nil {
			t.Fatal(err)
		}
		if keys != 0 || sessions != 0 {
			t.Fatalf("after the promotion: %d live keys, %d live key sessions; want none", keys, sessions)
		}
	}

	// Issue, promote, redeem.
	c := issue(t, "promotecode-then")
	if err := promote(ctx, c.user.ID); err != nil {
		t.Fatal(err)
	}
	if err := redeem(ctx, c); !errors.Is(err, ErrAppCodeInvalid) {
		t.Fatalf("redeem after promotion = %v, want ErrAppCodeInvalid", err)
	}
	requireNoKeys(t, c.user.ID)

	for _, promoteFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "promotion first", false: "redeem first"}[promoteFirst], func(t *testing.T) {
			c := issue(t, map[bool]string{true: "promotecode-race-p", false: "promotecode-race-r"}[promoteFirst])
			hold, err := env.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
			if _, err := hold.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, c.user.ID); err != nil {
				t.Fatal(err)
			}
			deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			promoted, redeemed := make(chan error, 1), make(chan error, 1)
			returned := make(chan error, 2)
			startPromote := func() {
				go func() {
					err := promote(deadline, c.user.ID)
					promoted <- err
					returned <- fmt.Errorf("promotion: %v", err)
				}()
			}
			startRedeem := func() {
				go func() {
					err := redeem(deadline, c)
					redeemed <- err
					returned <- fmt.Errorf("redeem: %v", err)
				}()
			}
			first, second := startRedeem, startPromote
			if promoteFirst {
				first, second = startPromote, startRedeem
			}
			first()
			waitForLockWaitersWhile(t, env.pool, 1, returned)
			second()
			waitForLockWaitersWhile(t, env.pool, 2, returned)
			if err := hold.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-promoted; err != nil {
				t.Fatalf("promotion: %v", err)
			}
			err = <-redeemed
			switch {
			case promoteFirst && !errors.Is(err, ErrAppCodeInvalid):
				t.Fatalf("redeem after the promotion committed = %v, want ErrAppCodeInvalid", err)
			case !promoteFirst && err != nil:
				t.Fatalf("redeem before the promotion = %v, want it enrolled", err)
			}
			requireNoKeys(t, c.user.ID)
		})
	}
}
