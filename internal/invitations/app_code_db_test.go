package invitations

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/appkeys/appkeystest"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
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

// redeem redeems code with new keys on a fresh challenge. A join enrolls
// the sent person key, so a successful answer's person_key_id is its ID.
func (e *joinEnv) redeem(t *testing.T, code string) (*auth.KeySignInResult, error) {
	t.Helper()
	challenge, _, err := e.signIn.IssueChallenge(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	person, device := appkeystest.NewKey(t), appkeystest.NewKey(t)
	result, err := e.signIn.RedeemAppCode(t.Context(), auth.KeyRedeemRequest{
		KeyRequest: auth.KeyRequest{Envelope: person.Redeem(t, device, joinTestServerID, challenge, code, time.Now()), ServerID: joinTestServerID},
		PersonKey:  person.Public(),
	})
	if err == nil && result.PersonKeyID != person.Public().KeyID() {
		t.Fatalf("person_key_id = %q, want the sent key %q", result.PersonKeyID, person.Public().KeyID())
	}
	return result, err
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
			// An administrator's join code never travels in a link.
			if issued.LinkAllowed != (role == models.RoleUser) {
				t.Fatalf("link allowed = %v for a %s invitation", issued.LinkAllowed, role)
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

// A join records the keys it enrolled on the new account: one audit row
// naming them and the code's kind, never the code, and both key rows carry
// the request's address, the one the device's session records. Regression:
// joining through the Silo app added a way in that no audit row showed.
func TestJoinWithAppCodeRecordsAddedKeysDB(t *testing.T) {
	env := newJoinEnv(t)
	token, _ := env.invite(t, models.RoleUser)
	issued, err := env.svc.IssueAppCode(t.Context(), AppCodeRequest{Token: token, Username: env.name("joiner-audit")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := audittest.Context(t, env.pool, audittest.Request{Method: http.MethodPost, Pattern: "/api/v2/auth/app-codes/redeem",
		Path: "/api/v2/auth/app-codes/redeem", OptIn: true})
	challenge, _, err := env.signIn.IssueChallenge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	person, device := appkeystest.NewKey(t), appkeystest.NewKey(t)
	result, err := env.signIn.RedeemAppCode(ctx, auth.KeyRedeemRequest{PersonKey: person.Public(), KeyRequest: auth.KeyRequest{
		Envelope: person.Redeem(t, device, joinTestServerID, challenge, issued.Code, time.Now()), ServerID: joinTestServerID, IP: clientip.FromContext(ctx)}})
	if err != nil {
		t.Fatal(err)
	}
	if result.PersonKeyStatus != auth.PersonKeyEnrolled || result.PersonKeyID != person.Public().KeyID() {
		t.Fatalf("person key %s %q, want enrolled %q", result.PersonKeyStatus, result.PersonKeyID, person.Public().KeyID())
	}
	row := audittest.One(t, env.pool, auditmutation.ActionAppCodeRedeemed, auditmutation.TargetUser, strconv.Itoa(result.User.ID))
	for field, want := range map[string]string{"app_code_kind": `"join"`, "device_key_id": `"` + result.DeviceID + `"`, "person_key_kid": `"` + person.Public().KeyID() + `"`} {
		if got := audittest.Value(row.Change(t, field).After); got != want {
			t.Fatalf("%s = %s, want %s", field, got, want)
		}
	}
	row.AssertNoSecrets(t, issued.Code)
	var sessionIP, personIP, deviceIP string
	if err := env.pool.QueryRow(t.Context(), `SELECT host(s.ip_address), host(p.enrolled_ip_address), host(d.enrolled_ip_address)
		FROM auth_sessions s, auth_person_keys p, auth_device_keys d
		WHERE s.id = $1 AND p.user_id = $2 AND d.id = $3::uuid`, result.Pair.SessionID, result.User.ID, result.DeviceID).Scan(&sessionIP, &personIP, &deviceIP); err != nil {
		t.Fatal(err)
	}
	if sessionIP == "" || personIP != sessionIP || deviceIP != sessionIP {
		t.Fatalf("addresses: session %q, person key %q, device key %q", sessionIP, personIP, deviceIP)
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

// waitUntilBlockedBy waits until a session is waiting on an advisory lock
// that the transaction with backend pid holder holds: the settings lock.
// Other tests' waiters in the same database don't count.
func waitUntilBlockedBy(t *testing.T, pool *pgxpool.Pool, holder uint32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks l
			WHERE l.locktype = 'advisory' AND NOT l.granted AND $1 = ANY (pg_blocking_pids(l.pid)))`, holder).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		// Postgres signals no event for a lock waiter; pg_locks is polled.
		time.Sleep(20 * time.Millisecond) //nolint:forbidigo // polls pg_locks until the other backend waits; the state itself is what is checked
	}
	t.Fatal("the claim never waited for the settings lock")
}

// Every invitation claim takes the settings lock before any invitation or
// code row: a join redeem, issuing a code and accepting with a password.
// Each is started while another transaction holds the settings lock, and
// while it waits there it must hold none of its rows: the other transaction
// can still lock them without waiting. With a row taken first, a claim that
// then went on to another invitation's row (a link invitation supersedes the
// others for its address) could deadlock with a claim of that invitation.
func TestClaimsTakeSettingsLockFirstDB(t *testing.T) {
	env := newJoinEnv(t)
	ctx := t.Context()
	type claim struct {
		run  func() error
		rows func(tx pgx.Tx) error
		then func(t *testing.T)
	}
	cases := map[string]func(t *testing.T) claim{
		"join redeem": func(t *testing.T) claim {
			address := env.name("racer") + "@example.test"
			token, link := env.invite(t, models.RoleUser)
			issued, err := env.svc.IssueAppCode(ctx, AppCodeRequest{Token: token, Username: env.name("racer"), Email: address})
			if err != nil {
				t.Fatal(err)
			}
			emailed, err := env.svc.repo.Create(ctx, models.CreateInvitationInput{Email: address, Role: models.RoleUser, InvitedBy: int64(env.inviter),
				ExpiresAt: time.Now().Add(time.Hour)}, HashToken(fmt.Sprintf("join-%s-emailed", env.suffix)))
			if err != nil {
				t.Fatal(err)
			}
			hash, _ := appCodeHashForTest(issued.Code)
			return claim{
				run: func() error { _, err := env.redeem(t, issued.Code); return err },
				rows: func(tx pgx.Tx) error {
					if _, err := tx.Exec(ctx, `SELECT 1 FROM invitations WHERE id = $1 FOR UPDATE NOWAIT`, link.ID); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `SELECT 1 FROM auth_app_codes WHERE code_hash = $1 FOR UPDATE NOWAIT`, hash)
					return err
				},
				then: func(t *testing.T) {
					var revoked bool
					if err := env.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM invitations WHERE id = $1`, emailed.ID).Scan(&revoked); err != nil || !revoked {
						t.Fatalf("the emailed invitation for the same address was not superseded: %v, %v", revoked, err)
					}
				},
			}
		},
		"issuing a code": func(t *testing.T) claim {
			token, inv := env.invite(t, models.RoleUser)
			return claim{
				run: func() error {
					_, err := env.svc.IssueAppCode(ctx, AppCodeRequest{Token: token, Username: env.name("issuer")})
					return err
				},
				rows: func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `SELECT 1 FROM invitations WHERE id = $1 FOR UPDATE NOWAIT`, inv.ID)
					return err
				},
			}
		},
		"accepting with a password": func(t *testing.T) claim {
			token, inv := env.invite(t, models.RoleUser)
			return claim{
				run: func() error {
					_, _, err := env.svc.Accept(ctx, token, env.name("accepter")+"@example.test", "accept-pass-1", "test", "")
					return err
				},
				rows: func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `SELECT 1 FROM invitations WHERE id = $1 FOR UPDATE NOWAIT`, inv.ID)
					return err
				},
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			c := setup(t)
			other, err := env.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // the explicit rollback below has run by then
			var holder uint32
			if err := other.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
				t.Fatal(err)
			}
			if err := auth.LockServerSettingsInTransaction(ctx, other); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- c.run() }()
			waitUntilBlockedBy(t, env.pool, holder)
			if err := c.rows(other); err != nil {
				t.Fatalf("the waiting claim holds a row: %v", err)
			}
			if err := other.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("the claim after the lock was released: %v", err)
			}
			if c.then != nil {
				c.then(t)
			}
		})
	}
}

// appCodeHashForTest is the stored hash of a code.
func appCodeHashForTest(code string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

// Two unspent codes never claim one address: an address one code reserves as
// its email can't be another's username, nor the other way round, compared
// as sign-in compares them. Renewing a code with the same name and email
// doesn't clash with the code it replaces.
func TestJoinAppCodeReservationsDB(t *testing.T) {
	env := newJoinEnv(t)
	ctx := t.Context()
	// A code whose invitation can no longer be claimed reserves nothing: the
	// replacement invitation takes the same name and email, and the old code
	// no longer works.
	for _, tc := range []struct {
		name, label string
		replace     func(t *testing.T, inv *models.Invitation) string
	}{
		{name: "resent", label: "resent", replace: func(t *testing.T, inv *models.Invitation) string {
			sent, err := env.svc.Resend(ctx, inv.ID, int64(env.inviter), DeliveryLink)
			if err != nil {
				t.Fatal(err)
			}
			return sent.ClaimURL[strings.LastIndex(sent.ClaimURL, "#")+1:]
		}},
		{name: "revoked, then invited again", label: "revoked", replace: func(t *testing.T, inv *models.Invitation) string {
			if err := env.svc.Revoke(ctx, inv.ID); err != nil {
				t.Fatal(err)
			}
			token, _ := env.invite(t, models.RoleUser)
			return token
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := AppCodeRequest{Username: env.name("again-" + tc.label), Email: env.name("again-"+tc.label) + "@example.test"}
			var inv *models.Invitation
			request.Token, inv = env.invite(t, models.RoleUser)
			old, err := env.svc.IssueAppCode(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			request.Token = tc.replace(t, inv)
			if _, err := env.svc.IssueAppCode(ctx, request); err != nil {
				t.Fatalf("the same name and email on the replacement invitation: %v", err)
			}
			if _, err := env.redeem(t, old.Code); !errors.Is(err, auth.ErrAppCodeInvalid) {
				t.Fatalf("the old code: %v", err)
			}
		})
	}
	t.Run("an address another code reserves", func(t *testing.T) {
		// One code's email is another's address-as-username, and the other
		// way round: two unspent codes never claim one address.
		first := env.name("first") + "@example.test"
		second := env.name("second") + "@example.test"
		issueAs := func(username, email string) error {
			token, _ := env.invite(t, models.RoleUser)
			_, err := env.svc.IssueAppCode(ctx, AppCodeRequest{Token: token, Username: username, Email: email})
			return err
		}
		if err := issueAs(env.name("bob"), first); err != nil {
			t.Fatal(err)
		}
		var taken *auth.NameTakenError
		if err := issueAs(strings.ToUpper(first), first); !errors.As(err, &taken) || taken.Field != auth.NameFieldUsername {
			t.Fatalf("an address reserved as an email, taken as a username: %v", err)
		}
		if err := issueAs(second, second); err != nil {
			t.Fatal(err)
		}
		if err := issueAs(env.name("carol"), strings.ToUpper(second)); !errors.As(err, &taken) || taken.Field != auth.NameFieldEmail {
			t.Fatalf("an address reserved as a username, taken as an email: %v", err)
		}
	})
	t.Run("renewing a code with the same name and email", func(t *testing.T) {
		token, _ := env.invite(t, models.RoleUser)
		request := AppCodeRequest{Token: token, Username: env.name("renew"), Email: env.name("renew") + "@example.test"}
		for range 2 {
			if _, err := env.svc.IssueAppCode(ctx, request); err != nil {
				t.Fatalf("renewal: %v", err)
			}
		}
	})
}
