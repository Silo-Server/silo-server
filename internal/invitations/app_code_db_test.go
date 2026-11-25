package invitations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/appkeys/appkeystest"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

const joinTestServerID = "4d0b8f7e-58e4-4c3e-9a51-2b2f0b6c1d7a"

// joinEnv is the invitation service and app key sign-in over the shared
// test database, wired as the server wires them.
type joinEnv struct {
	pool    *pgxpool.Pool
	svc     *Service
	signIn  *auth.AppKeySignIn
	inviter int
	suffix  string
	seq     atomic.Int64
}

func newJoinEnv(t *testing.T) *joinEnv {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	env := &joinEnv{pool: pool, suffix: strconv.FormatInt(time.Now().UnixNano(), 10)}
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'admin', true) RETURNING id`, "join-inviter-"+env.suffix, "join-inviter-"+env.suffix+"@example.invalid").Scan(&env.inviter); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id IN (SELECT accepted_user_id FROM invitations WHERE invited_by = $1)`, env.inviter)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, env.inviter)
	})
	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	authService := auth.NewService(auth.NewLocalProvider(users, sessions), auth.NewJWTService("join-test-jwt-secret-0123456789", time.Hour, 24*time.Hour), sessions, users, nil, nil, nil)
	env.svc = NewService(NewRepository(pool), users, auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(pool)), authService, nil, nil, nil, "https://server.example.invalid")
	env.signIn = auth.NewAppKeySignIn(pool, authService)
	env.signIn.SetJoinClaimer(env.svc)
	return env
}

// invite creates a link invitation (no address) and returns its token.
func (e *joinEnv) invite(t *testing.T, role string) (string, *models.Invitation) {
	t.Helper()
	token := fmt.Sprintf("join-%s-%d", e.suffix, e.seq.Add(1))
	inv, err := e.svc.repo.Create(t.Context(), models.CreateInvitationInput{Role: role, CreateProfile: true, InvitedBy: int64(e.inviter),
		ExpiresAt: time.Now().Add(time.Hour), Delivery: string(DeliveryLink)}, HashToken(token))
	if err != nil {
		t.Fatal(err)
	}
	return token, inv
}

func (e *joinEnv) name(label string) string {
	return strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(label)) + "-" + e.suffix
}

// redeem redeems code with new keys on a fresh challenge.
func (e *joinEnv) redeem(t *testing.T, code string) (*auth.KeySignInResult, error) {
	t.Helper()
	challenge, _, err := e.signIn.IssueChallenge(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	person, device := appkeystest.NewKey(t), appkeystest.NewKey(t)
	return e.signIn.RedeemAppCode(t.Context(), auth.KeyRedeemRequest{
		KeyRequest: auth.KeyRequest{Envelope: person.Redeem(t, device, joinTestServerID, challenge, code, time.Now()), ServerID: joinTestServerID},
		PersonKey:  person.Public(),
	})
}

func (e *joinEnv) invitationAccepted(t *testing.T, id int64) bool {
	t.Helper()
	var accepted bool
	if err := e.pool.QueryRow(t.Context(), `SELECT accepted_at IS NOT NULL FROM invitations WHERE id = $1`, id).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	return accepted
}

func (e *joinEnv) accountExists(t *testing.T, username string) bool {
	t.Helper()
	var exists bool
	if err := e.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM users WHERE username = $1)`, username).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// Joining through the Silo app: redeeming the code creates the account with
// no password, its two keys and a used invitation, in one step, and opens
// the device's session. An administrator invitation's keys start active.
// Until then nothing exists, and the code works once.
func TestJoinWithAppCodeDB(t *testing.T) {
	env := newJoinEnv(t)
	ctx := t.Context()
	for _, role := range []string{models.RoleUser, models.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			token, inv := env.invite(t, role)
			username := env.name("joiner-" + role)
			issued, err := env.svc.IssueAppCode(ctx, AppCodeRequest{Token: token, Username: username})
			if err != nil {
				t.Fatal(err)
			}
			if env.invitationAccepted(t, inv.ID) || env.accountExists(t, username) {
				t.Fatal("issuing a code created the account or used the invitation")
			}
			result, err := env.redeem(t, issued.Code)
			if err != nil {
				t.Fatal(err)
			}
			if result.User.Username != username || result.Pair == nil || result.PersonKeyStatus != auth.PersonKeyEnrolled {
				t.Fatalf("result = %+v", result)
			}
			var hash *string
			var local bool
			var email *string
			var persons, devices int
			if err := env.pool.QueryRow(ctx, `SELECT u.password_hash, u.local_password_login_enabled, u.email,
				(SELECT count(*) FROM auth_person_keys WHERE user_id = u.id AND revoked_at IS NULL),
				(SELECT count(*) FROM auth_device_keys WHERE user_id = u.id AND revoked_at IS NULL AND status = 'active')
				FROM users u WHERE u.id = $1`, result.User.ID).Scan(&hash, &local, &email, &persons, &devices); err != nil {
				t.Fatal(err)
			}
			if hash != nil || local || email != nil || persons != 1 || devices != 1 {
				t.Fatalf("account: hash %v, local sign-in %v, email %v, person keys %d, active device keys %d", hash, local, email, persons, devices)
			}
			if !env.invitationAccepted(t, inv.ID) {
				t.Fatal("the invitation is still open")
			}
			if _, err := env.redeem(t, issued.Code); !errors.Is(err, auth.ErrAppCodeInvalid) {
				t.Fatalf("second redeem: %v", err)
			}
		})
	}
}

// The refusals of a join: an expired code, a code a newer one replaced, a
// name taken at issue or between issue and redeem, an invitation revoked
// since, and a server with local passwords off (at issue, and at redeem when
// the setting changed in between). Two accounts with no email don't collide.
func TestJoinWithAppCodeRefusalsDB(t *testing.T) {
	env := newJoinEnv(t)
	ctx := t.Context()
	issue := func(t *testing.T, username string) (string, *models.Invitation) {
		t.Helper()
		token, inv := env.invite(t, models.RoleUser)
		issued, err := env.svc.IssueAppCode(ctx, AppCodeRequest{Token: token, Username: username})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Code, inv
	}

	t.Run("expired", func(t *testing.T) {
		code, inv := issue(t, env.name("expired"))
		if _, err := env.pool.Exec(ctx, `UPDATE auth_app_codes SET expires_at = NOW() - INTERVAL '1 second' WHERE invitation_id = $1`, inv.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := env.redeem(t, code); !errors.Is(err, auth.ErrAppCodeInvalid) {
			t.Fatalf("redeem: %v", err)
		}
		if env.invitationAccepted(t, inv.ID) {
			t.Fatal("an expired code used the invitation")
		}
	})
	t.Run("replaced by a newer code", func(t *testing.T) {
		token, _ := env.invite(t, models.RoleUser)
		first, err := env.svc.IssueAppCode(ctx, AppCodeRequest{Token: token, Username: env.name("first")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.svc.IssueAppCode(ctx, AppCodeRequest{Token: token, Username: env.name("second")}); err != nil {
			t.Fatal(err)
		}
		if _, err := env.redeem(t, first.Code); !errors.Is(err, auth.ErrAppCodeInvalid) {
			t.Fatalf("redeem: %v", err)
		}
	})
	t.Run("a name taken at issue", func(t *testing.T) {
		reserved := env.name("reserved")
		issue(t, reserved)
		token, _ := env.invite(t, models.RoleUser)
		if _, err := env.svc.IssueAppCode(ctx, AppCodeRequest{Token: token, Username: reserved}); !errors.Is(err, auth.ErrAppCodeNameTaken) {
			t.Fatalf("a reserved name: %v", err)
		}
		if _, err := env.svc.IssueAppCode(ctx, AppCodeRequest{Token: token, Username: "join-inviter-" + env.suffix}); !errors.Is(err, auth.ErrAppCodeNameTaken) {
			t.Fatalf("an account's name: %v", err)
		}
	})
	t.Run("a name taken before redeem", func(t *testing.T) {
		username := env.name("raced")
		code, inv := issue(t, username)
		if _, err := env.pool.Exec(ctx, `INSERT INTO users (username, password_hash, role, enabled) VALUES ($1, 'x', 'user', true)`, username); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = env.pool.Exec(context.Background(), `DELETE FROM users WHERE username = $1`, username) })
		if _, err := env.redeem(t, code); !errors.Is(err, auth.ErrAppCodeNameTaken) {
			t.Fatalf("redeem: %v", err)
		}
		if env.invitationAccepted(t, inv.ID) {
			t.Fatal("a refused join used the invitation")
		}
	})
	t.Run("invitation revoked since", func(t *testing.T) {
		code, inv := issue(t, env.name("revoked"))
		if err := env.svc.Revoke(ctx, inv.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := env.redeem(t, code); !errors.Is(err, auth.ErrAppCodeInvalid) {
			t.Fatalf("redeem: %v", err)
		}
	})
	t.Run("two accounts with no email", func(t *testing.T) {
		for _, label := range []string{"noemail-a", "noemail-b"} {
			code, _ := issue(t, env.name(label))
			if _, err := env.redeem(t, code); err != nil {
				t.Fatalf("%s: %v", label, err)
			}
		}
	})
	t.Run("local passwords off", func(t *testing.T) {
		t.Cleanup(restoreSetting(t, env.pool, config.AuthLocalPasswordLoginSettingKey))
		username := env.name("localoff")
		code, inv := issue(t, username)
		setSetting(t, env.pool, config.AuthLocalPasswordLoginSettingKey, "false")
		if _, err := env.redeem(t, code); !errors.Is(err, auth.ErrJoinNotAvailable) {
			t.Fatalf("redeem after the setting changed: %v", err)
		}
		if env.invitationAccepted(t, inv.ID) || env.accountExists(t, username) {
			t.Fatal("a refused join left an account or a used invitation")
		}
		token, _ := env.invite(t, models.RoleUser)
		if _, err := env.svc.IssueAppCode(ctx, AppCodeRequest{Token: token, Username: env.name("localoff2")}); !errors.Is(err, auth.ErrJoinNotAvailable) {
			t.Fatalf("issue: %v", err)
		}
	})
}

// Two apps redeeming one code at once: exactly one gets the account.
func TestJoinWithAppCodeConcurrentRedeemDB(t *testing.T) {
	env := newJoinEnv(t)
	token, _ := env.invite(t, models.RoleUser)
	issued, err := env.svc.IssueAppCode(t.Context(), AppCodeRequest{Token: token, Username: env.name("concurrent")})
	if err != nil {
		t.Fatal(err)
	}
	const racers = 4
	errs := make([]error, racers)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := range racers {
		done.Go(func() {
			start.Wait()
			_, errs[i] = env.redeem(t, issued.Code)
		})
	}
	start.Done()
	done.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, auth.ErrAppCodeInvalid):
			t.Fatalf("a losing redeem answered %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d redeems succeeded, want 1", won)
	}
}

func setSetting(t *testing.T, pool *pgxpool.Pool, key, value string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO server_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
		t.Fatal(err)
	}
}

// restoreSetting returns a cleanup that puts key back as it is now.
func restoreSetting(t *testing.T, pool *pgxpool.Pool, key string) func() {
	t.Helper()
	var value *string
	_ = pool.QueryRow(t.Context(), `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	return func() {
		ctx := context.Background()
		if value == nil {
			_, _ = pool.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, key)
			return
		}
		_, _ = pool.Exec(ctx, `UPDATE server_settings SET value = $2 WHERE key = $1`, key, *value)
	}
}
