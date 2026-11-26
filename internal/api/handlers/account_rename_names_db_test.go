package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// renameRig is one account whose username equals its primary profile's
// name, as signup names it, with an administrator, and a pool per request
// so each request runs on its own connection and can be found by its
// application_name while it waits.
type renameRig struct {
	f       *editsFixture
	tag     string
	old     string
	adminID int
}

func newRenameRig(t *testing.T) *renameRig {
	t.Helper()
	f := newEditsFixture(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	r := &renameRig{f: f, tag: tag, old: "parent-" + tag, adminID: f.adminID}
	if _, err := f.pool.Exec(t.Context(), `UPDATE users SET username = $2 WHERE id = $1`, f.userID, r.old); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE user_profiles SET name = $2 WHERE user_id = $1 AND id = $3`, f.userID, r.old, f.primary); err != nil {
		t.Fatal(err)
	}
	return r
}

// actor is a profile service and account repository on a pool of its own,
// named app.
type actor struct {
	app     string
	handler *ProfileHandler
	repo    *auth.UserRepository
	userID  int
	adminID int
}

func (r *renameRig) actor(t *testing.T, role string) *actor {
	t.Helper()
	app := "rename-" + role + "-" + r.tag
	pool, err := pgxpool.New(t.Context(), os.Getenv("SILO_TEST_DATABASE_URL")+"&application_name="+app)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	handler := NewProfileHandler(pgstore.NewPostgresProvider(pool))
	handler.UserRepo = auth.NewUserRepository(pool)
	handler.ProfileSetAvailable = true
	return &actor{app: app, handler: handler, repo: auth.NewUserRepository(pool), userID: r.f.userID, adminID: r.adminID}
}

func (a *actor) adminCtx() context.Context {
	return apimw.SetClaims(context.Background(), &auth.Claims{UserID: a.adminID, Role: models.RoleAdmin, TokenType: auth.TokenTypeAccess})
}

// renameAccount is an administrator's change of the account's username,
// at revision (-1 for none).
func (a *actor) renameAccount(ctx context.Context, username string, revision int64) error {
	_, err := a.repo.MutateAdminAccount(ctx, a.userID, revision, &models.UpdateUserInput{Username: &username},
		func(*models.User, pgx.Tx) (bool, error) { return false, nil })
	return err
}

// createProfile and renameProfile are an administrator's profile writes,
// acting as no profile of the account.
func (a *actor) createProfile(ctx context.Context, name string) error {
	_, err := a.handler.CreateProfile(ctx, ProfileCreateCommand{UserID: a.userID, Request: ProfileCreateRequest{Name: name}, AccountAdmin: true})
	return err
}

func (a *actor) renameProfile(ctx context.Context, profileID, name string) error {
	_, err := a.handler.UpdateProfile(ctx, ProfileUpdateCommand{UserID: a.userID, ProfileID: profileID,
		Request: ProfileUpdateRequest{Name: &name}, AccountAdmin: true})
	return err
}

// started is a request running on its own goroutine.
type started struct {
	done chan struct{}
	err  error
}

func start(fn func() error) *started {
	s := &started{done: make(chan struct{})}
	go func() { s.err = fn(); close(s.done) }()
	return s
}

func (s *started) finished() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// wait returns the request's error, failing if it doesn't finish in time:
// a lock cycle PostgreSQL did not break fails here instead of hanging.
func (s *started) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-s.done:
		return s.err
	case <-time.After(15 * time.Second):
		t.Fatal("a request did not finish")
		return nil
	}
}

// waitBlocked waits until the session named app waits on a lock. With
// orDone, a request that finished instead also ends the wait.
func waitBlocked(t *testing.T, pool *pgxpool.Pool, app string, s *started, orDone bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if s.finished() {
			if orDone {
				return
			}
			t.Fatalf("%s finished (%v) while it should have been waiting", app, s.err)
		}
		var blocked bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE application_name = $1 AND cardinality(pg_blocking_pids(pid)) > 0)`, app).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never waited on a lock", app)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// namedCount is how many of the account's profiles are named name, compared
// as the profile API compares names.
func (r *renameRig) namedCount(t *testing.T, name string) int {
	t.Helper()
	profiles, err := r.f.store.ListProfiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range profiles {
		if sameProfileName(p.Name, name) {
			n++
		}
	}
	return n
}

func (r *renameRig) username(t *testing.T) string {
	t.Helper()
	var u string
	if err := r.f.pool.QueryRow(t.Context(), `SELECT username FROM users WHERE id = $1`, r.f.userID).Scan(&u); err != nil {
		t.Fatal(err)
	}
	return u
}

func requireNameConflictOrNil(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if apiErr := requireAPIStatus(t, err, http.StatusConflict); apiErr.Code != "name_conflict" {
		t.Fatalf("error = %+v, want name_conflict", apiErr)
	}
}

// TestAccountRenameProfileNamesDB: an administrator's change of the account's
// username, which renames the primary profile when it still carries the old
// username, never leaves two profiles with one name against an
// administrator's profile write that overlaps it. The account rename itself
// always succeeds; the primary profile keeps its old name when the new one
// is taken, and a profile write that would take the primary's new name is
// refused with name_conflict. Each staging holds one request mid-way on a
// lock the test controls. It catches the account rename checking the names
// without the account's profile lock, which left [Sam, sam].
func TestAccountRenameProfileNamesDB(t *testing.T) {
	// The account rename waits on the new username (another transaction
	// is inserting an account with it) after it locked the account row; a
	// profile create of that name starts meanwhile and must not slip in.
	t.Run("a profile create against the account rename", func(t *testing.T) {
		r := newRenameRig(t)
		next := "sam-" + r.tag
		renamer, creator := r.actor(t, "account"), r.actor(t, "create")
		ctx := t.Context()
		holder, err := r.f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback(context.Background()) }()
		if _, err := holder.Exec(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, 'x', 'user')`,
			next, next+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
		rename := start(func() error { return renamer.renameAccount(context.Background(), next, -1) })
		waitBlocked(t, r.f.pool, renamer.app, rename, false)
		create := start(func() error { return creator.createProfile(creator.adminCtx(), strings.ToUpper(next)) })
		waitBlocked(t, r.f.pool, creator.app, create, false)
		if err := holder.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := rename.wait(t); err != nil {
			t.Fatalf("the account rename failed: %v", err)
		}
		requireNameConflictOrNil(t, create.wait(t))
		if got := r.username(t); got != next {
			t.Fatalf("username = %q, want %q", got, next)
		}
		if n := r.namedCount(t, next); n != 1 {
			t.Fatalf("%d profiles named %q, want 1", n, next)
		}
	})
	// A profile rename to the new username has checked the names and waits
	// for its profile row; the account rename starts meanwhile and must
	// not rename the primary profile to that name too.
	t.Run("a profile rename against the account rename", func(t *testing.T) {
		r := newRenameRig(t)
		next := "sam-" + r.tag
		if err := r.f.create("Kid"); err != nil {
			t.Fatal(err)
		}
		kidID := r.f.profileIDNamed(t, "Kid")
		renamer, profiler := r.actor(t, "account"), r.actor(t, "profile")
		ctx := t.Context()
		holder, err := r.f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback(context.Background()) }()
		if _, err := holder.Exec(ctx, `SELECT 1 FROM user_profiles WHERE user_id = $1 AND id = $2 FOR UPDATE`, r.f.userID, kidID); err != nil {
			t.Fatal(err)
		}
		profileRename := start(func() error { return profiler.renameProfile(profiler.adminCtx(), kidID, strings.ToUpper(next)) })
		waitBlocked(t, r.f.pool, profiler.app, profileRename, false)
		rename := start(func() error { return renamer.renameAccount(context.Background(), next, -1) })
		waitBlocked(t, r.f.pool, renamer.app, rename, true)
		if err := holder.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		requireNameConflictOrNil(t, profileRename.wait(t))
		if err := rename.wait(t); err != nil {
			t.Fatalf("the account rename failed: %v", err)
		}
		if got := r.username(t); got != next {
			t.Fatalf("username = %q, want %q", got, next)
		}
		if n := r.namedCount(t, next); n != 1 {
			t.Fatalf("%d profiles named %q, want 1", n, next)
		}
	})
}

// TestAccountRenameWaitsWithRevisionDB: two administrators save one
// account's username at the same revision while a profile transaction holds
// the account's profile lock. Both wait; the first to run saves, and the
// second, judged on the row it reads after the wait, is refused as stale
// (ErrAdminUserRevision, 412 precondition_failed on the route) and writes
// nothing. It catches the revision compared on a read taken before the wait.
func TestAccountRenameWaitsWithRevisionDB(t *testing.T) {
	r := newRenameRig(t)
	a, b := r.actor(t, "first"), r.actor(t, "second")
	ctx := t.Context()
	snapshot, err := a.repo.GetAdminSnapshot(ctx, r.f.userID)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := r.f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if err := pgstore.LockAccountForImport(ctx, holder, r.f.userID); err != nil {
		t.Fatal(err)
	}
	names := map[*actor]string{a: "first-" + r.tag, b: "second-" + r.tag}
	first := start(func() error { return a.renameAccount(context.Background(), names[a], snapshot.Revision) })
	waitBlocked(t, r.f.pool, a.app, first, false)
	second := start(func() error { return b.renameAccount(context.Background(), names[b], snapshot.Revision) })
	waitBlocked(t, r.f.pool, b.app, second, false)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	errs := map[*actor]error{a: first.wait(t), b: second.wait(t)}
	var saved, refused *actor
	for who, err := range errs {
		switch {
		case err == nil:
			saved = who
		case errors.Is(err, auth.ErrAdminUserRevision):
			refused = who
		default:
			t.Fatalf("%s: %v", who.app, err)
		}
	}
	if saved == nil || refused == nil {
		t.Fatalf("results %v, want one saved and one stale", errs)
	}
	if got := r.username(t); got != names[saved] {
		t.Fatalf("username = %q, want the saved %q", got, names[saved])
	}
}

// TestAccountRenameLockOrderDB: an account rename and each other writer that
// takes the account's profile lock, either one first, both queued on that
// lock and then let go: neither deadlocks, and each ends as it would alone.
// It catches a writer that takes the account row before the account's
// profile lock, which would deadlock with the rename.
func TestAccountRenameLockOrderDB(t *testing.T) {
	type op struct {
		name string
		run  func(t *testing.T, r *renameRig, a *actor) func() error
		// gone is an account the op deletes: a rename after it finds none.
		gone bool
	}
	ops := []op{
		{name: "a profile create", run: func(_ *testing.T, _ *renameRig, a *actor) func() error {
			return func() error { return a.createProfile(a.adminCtx(), "Guest") }
		}},
		{name: "a profile rename", run: func(t *testing.T, r *renameRig, a *actor) func() error {
			kid := r.f.profileIDNamed(t, "Kid")
			return func() error { return a.renameProfile(a.adminCtx(), kid, "Buddy") }
		}},
		{name: "a set apply", run: func(t *testing.T, r *renameRig, a *actor) func() error {
			set := setDoc(t, 1, newSetItem(r.old))
			return func() error {
				_, err := a.handler.ApplyProfileSet(context.Background(), ProfileSetCommand{UserID: a.userID, Set: set, Signed: true})
				return err
			}
		}},
		{name: "a profile delete", run: func(t *testing.T, r *renameRig, a *actor) func() error {
			kid := r.f.profileIDNamed(t, "Kid")
			return func() error {
				store, err := a.handler.storeProvider.ForUser(context.Background(), a.userID)
				if err != nil {
					return err
				}
				_, err = store.DeleteProfile(context.Background(), kid)
				return err
			}
		}},
		{name: "an account delete", gone: true, run: func(_ *testing.T, _ *renameRig, a *actor) func() error {
			return func() error {
				_, err := a.repo.MutateAdminAccount(context.Background(), a.userID, -1, nil, func(*models.User, pgx.Tx) (bool, error) { return false, nil })
				return err
			}
		}},
	}
	for _, o := range ops {
		for _, renameFirst := range []bool{true, false} {
			order := "the rename first"
			if !renameFirst {
				order = o.name + " first"
			}
			t.Run(o.name+", "+order, func(t *testing.T) {
				r := newRenameRig(t)
				if err := r.f.create("Kid"); err != nil {
					t.Fatal(err)
				}
				renamer, other := r.actor(t, "account"), r.actor(t, "other")
				next := "renamed-" + r.tag
				otherRun := o.run(t, r, other)
				ctx := t.Context()
				holder, err := r.f.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = holder.Rollback(context.Background()) }()
				if err := pgstore.LockAccountForImport(ctx, holder, r.f.userID); err != nil {
					t.Fatal(err)
				}
				var rename, otherReq *started
				if renameFirst {
					rename = start(func() error { return renamer.renameAccount(context.Background(), next, -1) })
					waitBlocked(t, r.f.pool, renamer.app, rename, false)
					otherReq = start(otherRun)
					waitBlocked(t, r.f.pool, other.app, otherReq, false)
				} else {
					otherReq = start(otherRun)
					waitBlocked(t, r.f.pool, other.app, otherReq, false)
					rename = start(func() error { return renamer.renameAccount(context.Background(), next, -1) })
					waitBlocked(t, r.f.pool, renamer.app, rename, false)
				}
				if err := holder.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
				if err := otherReq.wait(t); err != nil {
					t.Fatalf("%s failed: %v", o.name, err)
				}
				err = rename.wait(t)
				if o.gone && !renameFirst {
					if !errors.Is(err, auth.ErrNotFound) {
						t.Fatalf("a rename after the account was deleted = %v, want not found", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("the account rename failed: %v", err)
				}
			})
		}
	}
}
