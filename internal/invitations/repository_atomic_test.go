package invitations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type atomicInvitationFixture struct {
	repo   *Repository
	pool   *pgxpool.Pool
	schema string
	ctx    context.Context
}

func atomicInvitationDB(t *testing.T) atomicInvitationFixture {
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
	schema := fmt.Sprintf("invitation_atomic_%d", time.Now().UnixNano())
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+q+" CASCADE"); admin.Close() })
	for _, table := range []string{"users", "user_profiles", "user_profile_allowed_libraries", "invitations"} {
		if _, err = admin.Exec(ctx, "CREATE TABLE "+q+"."+table+" (LIKE public."+table+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+q+".users ALTER COLUMN id DROP IDENTITY IF EXISTS; CREATE SEQUENCE "+q+".user_fixture_seq; ALTER TABLE "+q+".users ALTER COLUMN id SET DEFAULT nextval('"+q+".user_fixture_seq'); ALTER TABLE "+q+".user_profiles ADD FOREIGN KEY(user_id) REFERENCES "+q+".users(id); ALTER TABLE "+q+".invitations ADD FOREIGN KEY(invited_by) REFERENCES "+q+".users(id), ADD FOREIGN KEY(accepted_user_id) REFERENCES "+q+".users(id)"); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `INSERT INTO users(username,email,password_hash,role,enabled) VALUES('inviter','inviter@example.invalid','x','admin',true)`); err != nil {
		t.Fatal(err)
	}
	return atomicInvitationFixture{NewRepository(pool), pool, schema, ctx}
}
func (f atomicInvitationFixture) invite(t *testing.T, name string) *models.Invitation {
	t.Helper()
	inv, err := f.repo.Create(t.Context(), models.CreateInvitationInput{Email: name + "@example.invalid", Role: "user", CreateProfile: true, ShowTour: true, InvitedBy: 1, ExpiresAt: time.Now().Add(time.Hour)}, HashToken(name))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}
func (f atomicInvitationFixture) provision(provider userstore.UserStoreProvider) func(*models.Invitation, pgx.Tx) (*models.User, error) {
	accounts := auth.NewAccountProvisioner(auth.NewUserRepository(f.pool), provider)
	return func(inv *models.Invitation, tx pgx.Tx) (*models.User, error) {
		return accounts.CreateAccountInTransaction(f.ctx, tx, auth.CreateAccountInput{User: models.CreateUserInput{Username: inv.Email, Email: inv.Email, Password: "fixture-password", Role: inv.Role}, DefaultProfile: auth.DefaultProfileOptions{Enabled: inv.CreateProfile, Name: "Home"}})
	}
}
func (f atomicInvitationFixture) counts(t *testing.T, inv *models.Invitation, wantUsers, wantProfiles, wantClaims int) {
	t.Helper()
	var users, profiles, claims int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users WHERE username=$1),(SELECT count(*) FROM user_profiles p JOIN users u ON u.id=p.user_id WHERE u.username=$1),(SELECT count(*) FROM invitations WHERE id=$2 AND accepted_at IS NOT NULL)`, inv.Email, inv.ID).Scan(&users, &profiles, &claims); err != nil {
		t.Fatal(err)
	}
	if users != wantUsers || profiles != wantProfiles || claims != wantClaims {
		t.Fatalf("users/profiles/claims=%d/%d/%d want %d/%d/%d", users, profiles, claims, wantUsers, wantProfiles, wantClaims)
	}
}
func (f atomicInvitationFixture) waitBlocked(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		var blocked bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, f.schema).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		runtime.Gosched()
	}
}
func TestInvitationAtomicConcurrentAccept(t *testing.T) {
	f := atomicInvitationDB(t)
	inv := f.invite(t, "concurrent")
	provision := f.provision(pgstore.NewPostgresProvider(f.pool))
	var calls atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			<-start
			_, err := f.repo.Accept(t.Context(), inv.TokenHash, func(i *models.Invitation, tx pgx.Tx) (*models.User, error) { calls.Add(1); return provision(i, tx) })
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrNotClaimable) && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if wins != 1 || calls.Load() != 1 {
		t.Fatalf("wins=%d provisions=%d", wins, calls.Load())
	}
	f.counts(t, inv, 1, 1, 1)
}
func TestInvitationAtomicRollbackAndExpiry(t *testing.T) {
	for _, scenario := range []string{"callback failure", "deferred commit failure", "expiry during provisioning", "expiry after lock wait"} {
		t.Run(scenario, func(t *testing.T) {
			f := atomicInvitationDB(t)
			inv := f.invite(t, "rollback")
			provision := f.provision(pgstore.NewPostgresProvider(f.pool))
			if scenario == "deferred commit failure" {
				_, err := f.pool.Exec(t.Context(), `CREATE FUNCTION fail_invitation_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture deferred failure'; END $$; CREATE CONSTRAINT TRIGGER fail_invitation_commit AFTER UPDATE ON invitations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.accepted_at IS NOT NULL) EXECUTE FUNCTION fail_invitation_commit()`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "expiry after lock wait" {
				tx, err := f.pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
				if _, err = tx.Exec(t.Context(), `UPDATE invitations SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, inv.ID); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				var calls atomic.Int32
				go func() {
					_, err := f.repo.Accept(t.Context(), inv.TokenHash, func(i *models.Invitation, tx pgx.Tx) (*models.User, error) { calls.Add(1); return provision(i, tx) })
					done <- err
				}()
				f.waitBlocked(t)
				if err = tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err = <-done; !errors.Is(err, ErrNotClaimable) && !errors.Is(err, ErrNotFound) {
					t.Fatalf("wait expiry=%v", err)
				}
				if calls.Load() != 0 {
					t.Fatal("expired invite provisioned")
				}
			} else {
				_, err := f.repo.Accept(t.Context(), inv.TokenHash, func(i *models.Invitation, tx pgx.Tx) (*models.User, error) {
					user, err := provision(i, tx)
					if err != nil {
						return nil, err
					}
					switch scenario {
					case "callback failure":
						return nil, errors.New("fixture failure")
					case "expiry during provisioning":
						_, err = tx.Exec(t.Context(), `UPDATE invitations SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, i.ID)
					}
					return user, err
				})
				if err == nil {
					t.Fatal("expected transaction failure")
				}
				if scenario == "expiry during provisioning" && !errors.Is(err, ErrNotClaimable) {
					t.Fatal(err)
				}
			}
			f.counts(t, inv, 0, 0, 0)
		})
	}
}

// Hiding the optional transactional method models the SQLite/nontransactional provider boundary.
type nontransactionalInvitationProvider struct{ userstore.UserStoreProvider }

func TestInvitationAtomicProfileProviders(t *testing.T) {
	for _, kind := range []string{"postgres", "wrapped postgres", "nil", "nontransactional"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/profile=%v", kind, enabled), func(t *testing.T) {
				f := atomicInvitationDB(t)
				inv := f.invite(t, "provider")
				if _, err := f.pool.Exec(t.Context(), `UPDATE invitations SET create_profile=$2 WHERE id=$1`, inv.ID, enabled); err != nil {
					t.Fatal(err)
				}
				var provider userstore.UserStoreProvider
				switch kind {
				case "postgres":
					provider = pgstore.NewPostgresProvider(f.pool)
				case "wrapped postgres":
					provider = notifications.WrapUserStoreProvider(pgstore.NewPostgresProvider(f.pool), &notifications.System{})
				case "nontransactional":
					provider = nontransactionalInvitationProvider{pgstore.NewPostgresProvider(f.pool)}
				}
				_, err := f.repo.Accept(t.Context(), inv.TokenHash, f.provision(provider))
				if enabled && (kind == "nil" || kind == "nontransactional") {
					if !errors.Is(err, auth.ErrTransactionalProfileUnavailable) {
						t.Fatalf("required profile error=%v", err)
					}
					f.counts(t, inv, 0, 0, 0)
					var called bool
					if err = f.pool.QueryRow(t.Context(), `SELECT is_called FROM user_fixture_seq`).Scan(&called); err != nil {
						t.Fatal(err)
					}
					var value int
					if err = f.pool.QueryRow(t.Context(), `SELECT last_value FROM user_fixture_seq`).Scan(&value); err != nil {
						t.Fatal(err)
					}
					if !called || value != 1 {
						t.Fatal("unsupported provider attempted account insertion")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				profiles := 0
				if enabled {
					profiles = 1
				}
				f.counts(t, inv, 1, profiles, 1)
			})
		}
	}
}
func TestInvitationAtomicLifecycleOrdering(t *testing.T) {
	for _, operation := range []string{"revoke", "resend", "delete"} {
		for _, acceptFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/accept-first=%v", operation, acceptFirst), func(t *testing.T) {
				f := atomicInvitationDB(t)
				inv := f.invite(t, "lifecycle")
				provision := f.provision(pgstore.NewPostgresProvider(f.pool))
				mutate := func() error {
					switch operation {
					case "revoke":
						return f.repo.Revoke(t.Context(), inv.ID)
					case "delete":
						return f.repo.Delete(t.Context(), inv.ID)
					default:
						_, err := f.repo.Resend(t.Context(), inv.ID, models.CreateInvitationInput{Email: inv.Email, Role: inv.Role, CreateProfile: true, InvitedBy: 1, ExpiresAt: time.Now().Add(time.Hour)}, HashToken("replacement"))
						return err
					}
				}
				if acceptFirst {
					entered := make(chan struct{})
					release := make(chan struct{})
					accepted := make(chan error, 1)
					go func() {
						_, err := f.repo.Accept(t.Context(), inv.TokenHash, func(i *models.Invitation, tx pgx.Tx) (*models.User, error) {
							close(entered)
							<-release
							return provision(i, tx)
						})
						accepted <- err
					}()
					<-entered
					mutated := make(chan error, 1)
					go func() { mutated <- mutate() }()
					f.waitBlocked(t)
					close(release)
					if err := <-accepted; err != nil {
						t.Fatal(err)
					}
					err := <-mutated
					if operation == "resend" {
						if err == nil {
							t.Fatal("resend accepted claimed invitation")
						}
					} else if err != nil {
						t.Fatal(err)
					}
					claims := 1
					if operation == "delete" {
						claims = 0
					}
					f.counts(t, inv, 1, 1, claims)
				} else {
					// Hold the row while administration queues first. PostgreSQL's row lock
					// wait queue gives that already-observed writer priority over acceptance.
					blocker, err := f.pool.Begin(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = blocker.Rollback(context.WithoutCancel(t.Context())) }()
					if _, err = blocker.Exec(t.Context(), `SELECT id FROM invitations WHERE id=$1 FOR UPDATE`, inv.ID); err != nil {
						t.Fatal(err)
					}
					mutated := make(chan error, 1)
					go func() { mutated <- mutate() }()
					f.waitBlocked(t)
					accepted := make(chan error, 1)
					var calls atomic.Int32
					go func() {
						_, err := f.repo.Accept(t.Context(), inv.TokenHash, func(i *models.Invitation, tx pgx.Tx) (*models.User, error) { calls.Add(1); return provision(i, tx) })
						accepted <- err
					}()
					if err = blocker.Commit(t.Context()); err != nil {
						t.Fatal(err)
					}
					if err = <-mutated; err != nil {
						t.Fatal(err)
					}
					if err = <-accepted; !errors.Is(err, ErrNotClaimable) && !errors.Is(err, ErrNotFound) {
						t.Fatalf("accept after %s=%v", operation, err)
					}
					if calls.Load() != 0 {
						t.Fatal("losing acceptance provisioned account")
					}
					f.counts(t, inv, 0, 0, 0)
				}
			})
		}
	}
}
func TestInvitationAtomicResend(t *testing.T) {
	f := atomicInvitationDB(t)
	ctx := t.Context()
	for _, state := range []string{"expired", "revoked", "existing email", "existing username", "insert failure"} {
		t.Run(state, func(t *testing.T) {
			inv := f.invite(t, state)
			input := models.CreateInvitationInput{Email: inv.Email, Role: "user", CreateProfile: true, InvitedBy: 1, ExpiresAt: time.Now().Add(time.Hour)}
			switch state {
			case "expired":
				if _, err := f.pool.Exec(ctx, `UPDATE invitations SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, inv.ID); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if err := f.repo.Revoke(ctx, inv.ID); err != nil {
					t.Fatal(err)
				}
			case "existing email":
				if _, err := f.pool.Exec(ctx, `INSERT INTO users(username,email,password_hash,role) VALUES('email-holder',$1,'x','user')`, inv.Email); err != nil {
					t.Fatal(err)
				}
			case "existing username":
				if _, err := f.pool.Exec(ctx, `INSERT INTO users(username,email,password_hash,role) VALUES($1,'different@example.invalid','x','user')`, inv.Email); err != nil {
					t.Fatal(err)
				}
			case "insert failure":
				input.Role = "invalid-role"
			}
			replacementHash := HashToken("new-" + state)
			if state == "insert failure" {
				replacementHash = inv.TokenHash
			}
			replacement, err := f.repo.Resend(ctx, inv.ID, input, replacementHash)
			if state == "expired" {
				if err != nil {
					t.Fatal(err)
				}
				old, err := f.repo.GetByID(ctx, inv.ID)
				if err != nil || old.RevokedAt == nil || replacement.ID == inv.ID {
					t.Fatal("replacement did not atomically supersede", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected resend rejection")
			}
			old, err := f.repo.GetByID(ctx, inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state != "revoked" && old.RevokedAt != nil {
				t.Fatal("failed resend revoked source")
			}
		})
	}
}

func TestInvitationAtomicSQLiteProvider(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("profile=%v", enabled), func(t *testing.T) {
			f := atomicInvitationDB(t)
			inv := f.invite(t, "sqlite")
			if _, err := f.pool.Exec(t.Context(), `UPDATE invitations SET create_profile=$2 WHERE id=$1`, inv.ID, enabled); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			provider := userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: dir}))
			t.Cleanup(func() {
				if err := provider.Close(); err != nil {
					t.Error(err)
				}
			})
			_, err := f.repo.Accept(t.Context(), inv.TokenHash, f.provision(provider))
			if enabled {
				if !errors.Is(err, auth.ErrTransactionalProfileUnavailable) {
					t.Fatalf("required SQLite profile=%v", err)
				}
				f.counts(t, inv, 0, 0, 0)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				f.counts(t, inv, 1, 0, 1)
			}
			files, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 0 {
				t.Fatalf("transactional acceptance wrote %d SQLite entries", len(files))
			}
		})
	}
}
func TestInvitationAtomicWriteFailures(t *testing.T) {
	for _, scenario := range []string{"profile SQL failure", "claim SQL failure", "cancel before claim", "duplicate signup account"} {
		t.Run(scenario, func(t *testing.T) {
			f := atomicInvitationDB(t)
			inv := f.invite(t, "writefailure")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.ctx = ctx
			provision := f.provision(pgstore.NewPostgresProvider(f.pool))
			switch scenario {
			case "profile SQL failure":
				if _, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_profile_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture profile failure'; END $$; CREATE TRIGGER fail_profile_write BEFORE INSERT ON user_profiles FOR EACH ROW EXECUTE FUNCTION fail_profile_write()`); err != nil {
					t.Fatal(err)
				}
			case "claim SQL failure":
				if _, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_claim_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture claim failure'; END $$; CREATE TRIGGER fail_claim_write BEFORE UPDATE ON invitations FOR EACH ROW WHEN(NEW.accepted_at IS NOT NULL) EXECUTE FUNCTION fail_claim_write()`); err != nil {
					t.Fatal(err)
				}
			case "duplicate signup account":
				if _, err := auth.NewAccountProvisioner(auth.NewUserRepository(f.pool), pgstore.NewPostgresProvider(f.pool)).CreateAccount(ctx, auth.CreateAccountInput{User: models.CreateUserInput{Username: inv.Email, Email: inv.Email, Password: "signup-password", Role: "user"}, DefaultProfile: auth.DefaultProfileOptions{Enabled: true, Name: "Home"}}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.repo.Accept(ctx, inv.TokenHash, func(i *models.Invitation, tx pgx.Tx) (*models.User, error) {
				user, err := provision(i, tx)
				if err == nil && scenario == "cancel before claim" {
					cancel()
				}
				return user, err
			})
			if err == nil {
				t.Fatal("expected write failure")
			}
			if scenario == "cancel before claim" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error=%v", err)
			}
			if scenario == "duplicate signup account" {
				if !errors.Is(err, auth.ErrDuplicate) {
					t.Fatalf("duplicate error=%v", err)
				}
				f.counts(t, inv, 1, 1, 0)
			} else {
				f.counts(t, inv, 0, 0, 0)
			}
		})
	}
}
func TestInvitationAtomicConcurrentFreshCreate(t *testing.T) {
	f := atomicInvitationDB(t)
	ctx := t.Context()
	// Each create has already observed no pending row before it reaches this
	// insert trigger. The advisory barrier makes that competing-insert case exact.
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION wait_for_create_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtext(current_schema())); RETURN NEW; END $$; CREATE TRIGGER wait_for_create_barrier BEFORE INSERT ON invitations FOR EACH ROW EXECUTE FUNCTION wait_for_create_barrier()`); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()))`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 4)
	var wg sync.WaitGroup
	for n := range 4 {
		wg.Go(func() {
			_, err := f.repo.Create(ctx, models.CreateInvitationInput{Email: "fresh@example.invalid", Role: "user", InvitedBy: 1, ExpiresAt: time.Now().Add(time.Hour)}, HashToken(fmt.Sprintf("fresh-%d", n)))
			results <- err
		})
	}
	waitCtx, done := context.WithTimeout(ctx, 3*time.Second)
	defer done()
	for {
		var waiting int
		if err = f.pool.QueryRow(waitCtx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock'`, f.schema).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 4 {
			break
		}
		runtime.Gosched()
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	var total, pending int
	if err = f.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE accepted_at IS NULL AND revoked_at IS NULL) FROM invitations WHERE email='fresh@example.invalid'`).Scan(&total, &pending); err != nil {
		t.Fatal(err)
	}
	if wins != 1 || total != 1 || pending != 1 {
		t.Fatalf("wins/total/pending=%d/%d/%d", wins, total, pending)
	}
}

func TestInvitationListPageDB(t *testing.T) {
	f := atomicInvitationDB(t)
	migration, err := os.ReadFile("../../migrations/sql/20260905233159_add_invitation_paging_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(migration), "-- +goose Down")
	if !ok {
		t.Fatal("missing down migration")
	}
	if _, err := f.pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), down); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	first := f.invite(t, "page-first")
	second := f.invite(t, "page-second")
	// Equal timestamps require the immutable ID tie-breaker; status may change
	// without invalidating the pagination position.
	if _, err := f.pool.Exec(t.Context(), `UPDATE invitations SET created_at=$1`, first.CreatedAt); err != nil {
		t.Fatal(err)
	}
	rows, more, err := f.repo.ListPage(t.Context(), nil, 1)
	if err != nil || !more || len(rows) != 1 || rows[0].ID != second.ID {
		t.Fatalf("first page=%v more=%v err=%v", rows, more, err)
	}
	if err := f.repo.Revoke(t.Context(), second.ID); err != nil {
		t.Fatal(err)
	}
	after := &PageKey{CreatedAt: rows[0].CreatedAt, ID: rows[0].ID}
	rows, more, err = f.repo.ListPage(t.Context(), after, 1)
	if err != nil || more || len(rows) != 1 || rows[0].ID != first.ID {
		t.Fatalf("second page=%v more=%v err=%v", rows, more, err)
	}
	rows, more, err = f.repo.ListPage(t.Context(), &PageKey{CreatedAt: rows[0].CreatedAt, ID: rows[0].ID}, 1)
	if err != nil || more || len(rows) != 0 {
		t.Fatalf("terminal page=%v more=%v err=%v", rows, more, err)
	}
	for _, limit := range []int{0, 201} {
		if _, _, err := f.repo.ListPage(t.Context(), nil, limit); err == nil {
			t.Fatalf("accepted limit%d", limit)
		}
	}
}
