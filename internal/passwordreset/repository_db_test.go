package passwordreset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type resetDB struct {
	repo *Repository
	pool *pgxpool.Pool
}

// newResetDB copies the tables a reset touches into a throwaway schema.
func newResetDB(t *testing.T) resetDB {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("password_reset_%d", time.Now().UnixNano())
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+q+" CASCADE"); admin.Close() })
	for _, table := range []string{"users", "auth_sessions", "password_reset_tokens", "abs_sessions", "jellycompat_sessions", "device_login_requests"} {
		if _, err = admin.Exec(ctx, "CREATE TABLE "+q+"."+table+" (LIKE public."+table+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+q+".users ALTER COLUMN id DROP IDENTITY IF EXISTS; CREATE SEQUENCE "+q+".user_fixture_seq; ALTER TABLE "+q+".users ALTER COLUMN id SET DEFAULT nextval('"+q+".user_fixture_seq')"); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// LIKE INCLUDING ALL omits triggers; install the production email policy.
	migration, err := os.ReadFile("../../migrations/sql/20261012124303_retire_password_resets_on_email_change.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.ReplaceAll(strings.Split(string(migration), "-- +goose Down")[0], "public.", q+".")
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	return resetDB{NewRepository(pool), pool}
}

// account inserts an account holding password "old-password" with the given
// state, and one login session it owns.
func (d resetDB) account(t *testing.T, name string, enabled, localLogin bool) int {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var id int
	err = d.pool.QueryRow(t.Context(), `INSERT INTO users(username, email, password_hash, role, enabled, local_password_login_enabled, password_change_required)
		VALUES ($1, $2, $3, 'user', $4, $5, true) RETURNING id`, name, name+"@example.invalid", string(hash), enabled, localLogin).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(t.Context(), `INSERT INTO auth_sessions(id, user_id, device_name, expires_at) VALUES ($1, $2, 'test', now() + interval '1 day')`, name+"-session", id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (d resetDB) issue(t *testing.T, userID int, token string, expiresAt time.Time) {
	t.Helper()
	user, err := auth.NewUserRepository(d.pool).GetByID(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.repo.Issue(t.Context(), userID, user.Email, auth.HashLinkToken(token), Issuer{}, expiresAt, DeliveryLink); err != nil {
		t.Fatal(err)
	}
}

func TestResetLinkRetiresOnRecoveryEmailChangeDB(t *testing.T) {
	d := newResetDB(t)
	ctx := t.Context()
	id := d.account(t, "email-change", true, true)
	d.issue(t, id, "email-link", time.Now().Add(time.Hour))
	hash := auth.HashLinkToken("email-link")
	// Resending the same identity with different casing preserves the link.
	if _, err := d.pool.Exec(ctx, `UPDATE users SET email = upper(email) WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.repo.Lookup(ctx, hash); err != nil {
		t.Fatal("unchanged recovery identity retired the link", err)
	}
	// An email write and the link's retirement must roll back together.
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `UPDATE users SET email = 'rolled-back@example.test' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.repo.Lookup(ctx, hash); err != nil {
		t.Fatal("rolled-back email write retired the link", err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE users SET email = 'replacement@example.test' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.repo.Lookup(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Errorf("link issued to the previous address still resolves: %v", err)
	}
	if _, err := d.repo.Complete(ctx, hash, "attacker-password"); !errors.Is(err, ErrNotFound) {
		t.Errorf("link issued to the previous address completed a reset: %v", err)
	}
	// Restoring the address must never resurrect the old capability.
	if _, err := d.pool.Exec(ctx, `UPDATE users SET email = 'email-change@example.invalid' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.repo.Lookup(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Errorf("restoring the address resurrected the link: %v", err)
	}
	d.issue(t, id, "new-email-link", time.Now().Add(time.Hour))
	if _, err := d.repo.Complete(ctx, auth.HashLinkToken("new-email-link"), "new-password"); err != nil {
		t.Fatal("a link issued after the email change must work", err)
	}
}

// changingRecoveryEmail returns the real snapshot read before a committed email
// change, reproducing the gap between the service's lookup and link issuance.
type changingRecoveryEmail struct {
	*auth.UserRepository
	pool *pgxpool.Pool
}

func (d changingRecoveryEmail) change(ctx context.Context, user *models.User, err error) (*models.User, error) {
	if err != nil {
		return nil, err
	}
	_, err = d.pool.Exec(ctx, `UPDATE users SET email = 'changed@example.test' WHERE id = $1`, user.ID)
	return user, err
}

func (d changingRecoveryEmail) GetByID(ctx context.Context, id int) (*models.User, error) {
	user, err := d.UserRepository.GetByID(ctx, id)
	return d.change(ctx, user, err)
}

func (d changingRecoveryEmail) GetByUsername(ctx context.Context, name string) (*models.User, error) {
	user, err := d.UserRepository.GetByUsername(ctx, name)
	return d.change(ctx, user, err)
}

func TestResetIssuanceRejectsChangedRecoveryEmailDB(t *testing.T) {
	for _, selfService := range []bool{false, true} {
		t.Run(fmt.Sprintf("self-service=%t", selfService), func(t *testing.T) {
			d := newResetDB(t)
			id := d.account(t, "stale-email", true, true)
			users := auth.NewUserRepository(d.pool)
			sender := &fakeMail{enabled: true}
			s := NewService(d.repo, changingRecoveryEmail{users, d.pool}, nil, sender, selfServiceSettings(), nil, "https://silo.example.test")
			issue := func() error {
				if selfService {
					return s.sendRequested(t.Context(), "stale-email")
				}
				_, err := s.Issue(t.Context(), IssueInput{UserID: id, Delivery: DeliveryEmail})
				return err
			}
			wantErr := ErrNotEligible
			if selfService {
				wantErr = nil
			}
			if err := issue(); !errors.Is(err, wantErr) {
				t.Errorf("issue against stale email: %v, want %v", err, wantErr)
			}
			var links int
			if err := d.pool.QueryRow(t.Context(), `SELECT count(*) FROM password_reset_tokens WHERE user_id = $1`, id).Scan(&links); err != nil {
				t.Fatal(err)
			}
			if links != 0 || len(sender.sent) != 0 {
				t.Fatalf("stale address received a reset: %d links, %d emails", links, len(sender.sent))
			}
			// A fresh lookup must still be able to deliver a usable link.
			s.users = users
			if err := issue(); err != nil {
				t.Fatal("issuing at the current email", err)
			}
			if len(sender.sent) != 1 || len(sender.sent[0].To) != 1 || sender.sent[0].To[0] != "changed@example.test" {
				t.Fatalf("fresh issue did not deliver to current email: %+v", sender.sent)
			}
			_, token, ok := strings.Cut(sender.sent[0].TextBody, "https://silo.example.test/reset-password/")
			token, _, _ = strings.Cut(token, "\n")
			if !ok {
				t.Fatal("email has no reset link")
			}
			if _, err := d.repo.Lookup(t.Context(), auth.HashLinkToken(token)); err != nil {
				t.Fatal("fresh link is unusable", err)
			}
		})
	}
}

func TestResetIssuanceSerializesEmailChangeDB(t *testing.T) {
	for _, selfService := range []bool{false, true} {
		t.Run(fmt.Sprintf("self-service=%t", selfService), func(t *testing.T) {
			d := newResetDB(t)
			id := d.account(t, "pending-email", true, true)
			tx, err := d.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
			if _, err := tx.Exec(t.Context(), `UPDATE users SET email = 'new@example.test' WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
			var blocker int
			if err := tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if selfService {
					stored, err := d.repo.IssueUnlessRecent(t.Context(), id, "pending-email@example.invalid", "pending-link", time.Now().Add(time.Hour), time.Minute)
					if stored && err == nil {
						err = errors.New("stored a link for the previous address")
					}
					done <- err
				} else {
					done <- d.repo.Issue(t.Context(), id, "pending-email@example.invalid", "pending-link", Issuer{}, time.Now().Add(time.Hour), DeliveryLink)
				}
			}()
			// Observe the real database lock before committing the email change.
			d.awaitBlocked(t, blocker, done)
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			wantErr := ErrNotEligible
			if selfService {
				wantErr = nil
			}
			if err := <-done; !errors.Is(err, wantErr) {
				t.Fatalf("issuance after pending email change: %v, want %v", err, wantErr)
			}
			if _, err := d.repo.Lookup(t.Context(), "pending-link"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("stale link survived the email change: %v", err)
			}
		})
	}
}

// awaitBlocked waits until a session is blocked on a lock held by backend
// blocker, failing if done reports first.
func (d resetDB) awaitBlocked(t *testing.T, blocker int, done <-chan error) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := d.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("issuance did not wait for the locked account: %v", err)
		case <-deadline.C:
			t.Fatal("issuance never waited for the account")
		case <-tick.C:
		}
	}
}

// TestResetIssuanceSerializesPromotionDB holds the real promotion with the
// account locked and starts issuance behind it, so the link is judged after
// the promotion deleted the account's links: only the Owner, without a scoped
// key, may still store one for the new admin.
func TestResetIssuanceSerializesPromotionDB(t *testing.T) {
	d := newResetDB(t)
	owner := d.account(t, "owner", true, true)
	admin := d.account(t, "admin", true, true)
	if _, err := d.pool.Exec(t.Context(), `UPDATE users SET role = 'admin' WHERE id = ANY($1)`, []int{owner, admin}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(t.Context(), `UPDATE users SET is_owner = true, break_glass = true WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		name      string
		owner     bool
		scopedKey bool
		want      error
	}{
		{"admin", false, false, auth.ErrAdminProtected},
		{"owner with a scoped key", true, true, ErrScopedKeyAdmin},
		{"owner", true, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			name := fmt.Sprintf("target-%d", i)
			target := d.account(t, name, true, true)
			token := name + "-link"
			issuer := Issuer{ID: admin, ScopedKey: tc.scopedKey}
			if tc.owner {
				issuer.ID = owner
			}

			locked := make(chan int, 1)
			release := make(chan struct{})
			releaseOnce := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseOnce)
			promoted := make(chan error, 1)
			go func() {
				_, err := auth.NewUserRepository(d.pool).MutateAdminAccount(ctx, target, -1, &models.UpdateUserInput{Role: new(models.RoleAdmin)},
					func(_ *models.User, tx pgx.Tx) (bool, error) {
						var pid int
						if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
							return false, err
						}
						locked <- pid
						<-release
						return false, nil
					})
				promoted <- err
			}()
			var blocker int
			select {
			case blocker = <-locked:
			case err := <-promoted:
				t.Fatalf("promotion ended before locking the account: %v", err)
			}

			done := make(chan error, 1)
			go func() {
				done <- d.repo.Issue(ctx, target, name+"@example.invalid", token, issuer, time.Now().Add(time.Hour), DeliveryLink)
			}()
			d.awaitBlocked(t, blocker, done)
			releaseOnce()
			if err := <-promoted; err != nil {
				t.Fatal("promotion:", err)
			}
			if err := <-done; !errors.Is(err, tc.want) {
				t.Fatalf("issuance after promotion: %v, want %v", err, tc.want)
			}
			_, err := d.repo.Lookup(ctx, token)
			if tc.want == nil && err != nil {
				t.Fatal("the Owner's link for the new admin is unusable:", err)
			}
			if tc.want != nil && !errors.Is(err, ErrNotFound) {
				t.Fatalf("a link issued against the pre-promotion role survived: %v", err)
			}
		})
	}
}

// TestResetIssuanceSerializesOwnershipMoveDB holds an ownership move away from
// the issuing Owner, with the locks and writes TransferOwnership makes (it
// cannot be paused from a test), so issuance waits on the issuer and then
// judges it as the admin it has become.
func TestResetIssuanceSerializesOwnershipMoveDB(t *testing.T) {
	d := newResetDB(t)
	ctx := t.Context()
	owner := d.account(t, "owner", true, true)
	successor := d.account(t, "successor", true, true)
	target := d.account(t, "target", true, true)
	if _, err := d.pool.Exec(ctx, `UPDATE users SET role = 'admin' WHERE id = ANY($1)`, []int{owner, successor, target}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE users SET is_owner = true, break_glass = true WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, stmt := range []struct {
		sql string
		arg any
	}{
		{`SELECT 1 FROM users WHERE id = ANY($1) ORDER BY id FOR UPDATE`, []int{owner, successor}},
		{`UPDATE users SET is_owner = false WHERE id = $1`, owner},
		{`UPDATE users SET is_owner = true, break_glass = true WHERE id = $1`, successor},
	} {
		if _, err := tx.Exec(ctx, stmt.sql, stmt.arg); err != nil {
			t.Fatal(err)
		}
	}
	var blocker int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- d.repo.Issue(ctx, target, "target@example.invalid", "moved-link", Issuer{ID: owner}, time.Now().Add(time.Hour), DeliveryLink)
	}()
	d.awaitBlocked(t, blocker, done)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, auth.ErrAdminProtected) {
		t.Fatalf("issuance after the ownership move: %v, want %v", err, auth.ErrAdminProtected)
	}
	if _, err := d.repo.Lookup(ctx, "moved-link"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the former Owner holds a link for another admin: %v", err)
	}
}
