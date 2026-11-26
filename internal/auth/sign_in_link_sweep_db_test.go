package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A sign-in link's app code redeemed while app keys are turned off through
// the settings write (its sweep included) leaves no key session alive with
// the switch off, whichever side takes the settings lock first: the sweep
// first, and the redeem, re-reading the switch under that lock, is refused
// with nothing written; the redeem first (holding the lock shared, held at
// the person key row it locks after), and the sweep, waiting on the lock,
// ends the session the redeem opened.
func TestSignInLinkCodeRedeemAgainstSweepDB(t *testing.T) {
	env := newKeyEnv(t, "linkcodesweep")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	backendBlockedBy := func(t *testing.T, ctx context.Context, holder uint32) uint32 {
		t.Helper()
		for {
			var pid uint32
			err := env.pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity
				WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid)) LIMIT 1`, holder).Scan(&pid)
			if err == nil {
				return pid
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
				t.Fatal("nothing waited on the held lock")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	for _, sweepFirst := range []bool{true, false} {
		name := "the redeem first"
		if sweepFirst {
			name = "the sweep first"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
			user := env.localAccount(t, "linkcodesweep-"+map[bool]string{true: "sweep", false: "redeem"}[sweepFirst], models.RoleUser)
			// A person key the redeem replaces: it locks that row after the
			// settings lock, which is where the redeem-first case holds it.
			env.enroll(t, user)
			link, err := env.signIn.RedeemSignInLink(ctx, env.issueLink(t, user).Token, true)
			if err != nil || link.AppCode == nil {
				t.Fatalf("link redeem = %+v, %v", link, err)
			}
			person, device := newTestKey(t), newTestKey(t)
			request := env.request(t, message{purpose: appkeys.PurposeRedeem, signer: person, device: &device,
				fields: map[string]any{"code": link.AppCode.Code}})
			redeemed := make(chan error, 1)
			redeem := func() {
				_, err := env.signIn.RedeemAppCode(ctx, KeyRedeemRequest{KeyRequest: request, PersonKey: person.pub})
				redeemed <- err
			}

			holder, err := env.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // ended below
			var holderPID uint32
			if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
				t.Fatal(err)
			}
			if sweepFirst {
				// The settings write, sweep included, holding the lock.
				if err := lockServerSettings(ctx, holder); err != nil {
					t.Fatal(err)
				}
				if _, err := holder.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'false')
					ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthAppKeySignInSettingKey); err != nil {
					t.Fatal(err)
				}
				if _, err := EndAppKeySessionsTx(ctx, holder); err != nil {
					t.Fatal(err)
				}
				go redeem()
				backendBlockedBy(t, ctx, holderPID)
				if err := holder.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-redeemed; !errors.Is(err, ErrAppKeysOff) {
					t.Fatalf("redeem after the sweep = %v, want ErrAppKeysOff", err)
				}
			} else {
				if _, err := holder.Exec(ctx, `SELECT 1 FROM auth_person_keys WHERE user_id = $1 AND revoked_at IS NULL FOR UPDATE`, user.ID); err != nil {
					t.Fatal(err)
				}
				go redeem()
				redeemPID := backendBlockedBy(t, ctx, holderPID)
				var shared bool
				if err := env.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks
					WHERE pid = $1 AND locktype = 'advisory' AND mode = 'ShareLock' AND granted)`, redeemPID).Scan(&shared); err != nil || !shared {
					t.Fatalf("the redeem holds the settings lock shared = %v (%v)", shared, err)
				}
				swept := make(chan error, 1)
				go func() { swept <- switchOff(ctx, env.pool) }()
				backendBlockedBy(t, ctx, redeemPID)
				if err := holder.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-redeemed; err != nil {
					t.Fatalf("redeem before the sweep = %v", err)
				}
				if err := <-swept; err != nil {
					t.Fatalf("switch-off = %v", err)
				}
			}
			var live int
			if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions
				WHERE user_id = $1 AND device_key_id IS NOT NULL AND revoked_at IS NULL`, user.ID).Scan(&live); err != nil {
				t.Fatal(err)
			}
			if live != 0 {
				t.Fatalf("%d key sessions alive with app keys off", live)
			}
		})
	}
}
