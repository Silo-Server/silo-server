package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestLoginIdentitySpaceDB pins user_login_identifiers: no account's username
// may equal another account's email, because LookupLogin resolves a typed
// identifier against the username column before the email column and would
// otherwise sign in or reset the wrong account.
func TestLoginIdentitySpaceDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := fmt.Sprintf("login-identity-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), "DELETE FROM users WHERE username LIKE $1 OR email LIKE $1", prefix+"%")
	})
	users := NewUserRepository(pool)
	input := func(username, email string) models.CreateUserInput {
		return models.CreateUserInput{Username: username, Email: email, Password: "test-password", Role: "user"}
	}
	create := func(t *testing.T, username, email string) (*models.User, error) {
		t.Helper()
		return users.Create(ctx, input(username, email))
	}
	mustCreate := func(t *testing.T, username, email string) *models.User {
		t.Helper()
		user, err := create(t, username, email)
		if err != nil {
			t.Fatalf("create %q/%q: %v", username, email, err)
		}
		return user
	}
	requireDuplicate := func(t *testing.T, err error) {
		t.Helper()
		if !IsDuplicate(err) {
			t.Fatalf("err = %v, want ErrDuplicate", err)
		}
		if !strings.Contains(err.Error(), "user_login_identifiers_holder_key") {
			t.Fatalf("err = %v, want the user_login_identifiers_holder_key index", err)
		}
	}
	// seedLegacyCollision writes an account whose username is owner's email
	// with the sync trigger bypassed, as rows written before the migration can
	// be, and records it the way the backfill does: owner holds the shared
	// identifier and the new account keeps a legacy_duplicate row for it.
	seedLegacyCollision := func(t *testing.T, owner *models.User, email string) int {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
			t.Skipf("cannot bypass triggers to seed a legacy collision: %v", err)
		}
		var id int
		if err := tx.QueryRow(ctx, `INSERT INTO users (username,email,password_hash,role,enabled) VALUES ($1,$2,'x','user',true) RETURNING id`, owner.Email, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_login_identifiers (identifier, user_id, legacy_duplicate) VALUES ($1, $2, false), ($3, $2, true)`, email, id, owner.Email); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// session starts a dedicated connection and returns it with its backend
	// pid, so a test can see when a statement on it blocks.
	session := func(t *testing.T) (*pgxpool.Conn, int) {
		t.Helper()
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(conn.Release)
		var pid int
		if err := conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			t.Fatal(err)
		}
		return conn, pid
	}
	// waitBlocked returns once the backend is waiting on a lock, and fails if
	// the pending statement finishes first.
	waitBlocked := func(t *testing.T, pid int, done <-chan error) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var waiting bool
			if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)", pid).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				return
			}
			select {
			case err := <-done:
				t.Fatalf("statement finished before it blocked: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("statement never blocked")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	t.Run("username cannot be another account's email", func(t *testing.T) {
		owner := mustCreate(t, prefix+"-owner1", prefix+"-alice@example.invalid")
		_, err := create(t, strings.ToUpper(owner.Email), prefix+"-other1@example.invalid")
		requireDuplicate(t, err)

		other := mustCreate(t, prefix+"-other1b", prefix+"-other1b@example.invalid")
		username := owner.Email
		requireDuplicate(t, users.Update(ctx, other.ID, models.UpdateUserInput{Username: &username}))
	})

	t.Run("email cannot be another account's username", func(t *testing.T) {
		owner := mustCreate(t, prefix+"-bob@example.invalid", prefix+"-bob-mail@example.invalid")
		_, err := create(t, prefix+"-other2", owner.Username)
		requireDuplicate(t, err)

		other := mustCreate(t, prefix+"-other2b", prefix+"-other2b@example.invalid")
		email := strings.ToUpper(owner.Username)
		requireDuplicate(t, users.Update(ctx, other.ID, models.UpdateUserInput{Email: &email}))
	})

	t.Run("renamed identifiers are released", func(t *testing.T) {
		first := mustCreate(t, prefix+"-first3", prefix+"-erin@example.invalid")
		email := prefix + "-erin-new@example.invalid"
		if err := users.Update(ctx, first.ID, models.UpdateUserInput{Email: &email}); err != nil {
			t.Fatal(err)
		}
		mustCreate(t, prefix+"-erin@example.invalid", prefix+"-second3@example.invalid")
		_, err := create(t, email, prefix+"-third3@example.invalid")
		requireDuplicate(t, err)
	})

	t.Run("an account may use its own email as its username", func(t *testing.T) {
		invited := mustCreate(t, prefix+"-invited@example.invalid", prefix+"-invited@example.invalid")
		username, email := invited.Username, invited.Email
		if err := users.Update(ctx, invited.ID, models.UpdateUserInput{Username: &username, Email: &email}); err != nil {
			t.Fatalf("rewriting own identifiers: %v", err)
		}
		swappedEmail := prefix + "-invited-new@example.invalid"
		if err := users.Update(ctx, invited.ID, models.UpdateUserInput{Email: &swappedEmail}); err != nil {
			t.Fatalf("changing email away from username: %v", err)
		}
		_, err := create(t, prefix+"-other4", invited.Username)
		requireDuplicate(t, err)
	})

	t.Run("an existing collision stays editable", func(t *testing.T) {
		owner := mustCreate(t, prefix+"-owner5", prefix+"-carol@example.invalid")
		legacyID := seedLegacyCollision(t, owner, prefix+"-legacy5@example.invalid")

		username := owner.Email
		enabled := false
		if err := users.Update(ctx, legacyID, models.UpdateUserInput{Username: &username, Enabled: &enabled}); err != nil {
			t.Fatalf("editing a legacy collision without changing identifiers: %v", err)
		}
		email := prefix + "-legacy5-new@example.invalid"
		if err := users.Update(ctx, legacyID, models.UpdateUserInput{Email: &email}); err != nil {
			t.Fatalf("changing the other identifier of a legacy collision: %v", err)
		}
		fixed := prefix + "-legacy5"
		if err := users.Update(ctx, legacyID, models.UpdateUserInput{Username: &fixed}); err != nil {
			t.Fatalf("renaming a legacy collision away: %v", err)
		}
	})

	t.Run("a released identifier passes to a legacy holder", func(t *testing.T) {
		owner := mustCreate(t, prefix+"-owner6", prefix+"-frank@example.invalid")
		seedLegacyCollision(t, owner, prefix+"-legacy6@example.invalid")
		email := prefix + "-frank-new@example.invalid"
		if err := users.Update(ctx, owner.ID, models.UpdateUserInput{Email: &email}); err != nil {
			t.Fatal(err)
		}
		_, err := create(t, prefix+"-other6", prefix+"-frank@example.invalid")
		requireDuplicate(t, err)

		deleted := mustCreate(t, prefix+"-owner6b", prefix+"-grace@example.invalid")
		seedLegacyCollision(t, deleted, prefix+"-legacy6b@example.invalid")
		if err := users.Delete(ctx, deleted.ID); err != nil {
			t.Fatal(err)
		}
		_, err = create(t, prefix+"-other6b", prefix+"-grace@example.invalid")
		requireDuplicate(t, err)
	})

	t.Run("a repeatable-read writer sees a claim committed after its snapshot", func(t *testing.T) {
		identifier := prefix + "-heidi@example.invalid"
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT count(*) FROM users"); err != nil {
			t.Fatal(err)
		}
		mustCreate(t, prefix+"-owner7", identifier)
		_, err = createUser(ctx, tx, input(identifier, prefix+"-other7@example.invalid"))
		requireDuplicate(t, err)
	})

	t.Run("a concurrent writer waits for an uncommitted claim", func(t *testing.T) {
		identifier := prefix + "-dave@example.invalid"
		first, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = first.Rollback(ctx) }()
		if _, err := createUser(ctx, first, input(prefix+"-owner8", identifier)); err != nil {
			t.Fatal(err)
		}

		conn, pid := session(t)
		second := make(chan error, 1)
		go func() {
			_, err := createUser(ctx, conn, input(strings.ToUpper(identifier), prefix+"-other8@example.invalid"))
			second <- err
		}()
		waitBlocked(t, pid, second)
		if err := first.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		requireDuplicate(t, <-second)
	})

	t.Run("writers claiming a swapped pair do not deadlock", func(t *testing.T) {
		a, b := prefix+"-pair-a@example.invalid", prefix+"-pair-b@example.invalid"
		anchor := mustCreate(t, prefix+"-anchor9", prefix+"-anchor9@example.invalid")
		// Hold b so the first writer stops between its two claims.
		hold, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = hold.Rollback(ctx) }()
		if _, err := hold.Exec(ctx, "INSERT INTO user_login_identifiers (identifier, user_id) VALUES ($1, $2)", b, anchor.ID); err != nil {
			t.Fatal(err)
		}

		firstConn, firstPID := session(t)
		first := make(chan error, 1)
		go func() {
			_, err := createUser(ctx, firstConn, input(a, b))
			first <- err
		}()
		waitBlocked(t, firstPID, first)

		secondConn, secondPID := session(t)
		second := make(chan error, 1)
		go func() {
			_, err := createUser(ctx, secondConn, input(b, a))
			second <- err
		}()
		waitBlocked(t, secondPID, second)

		if err := hold.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-first; err != nil {
			t.Fatalf("first writer: %v", err)
		}
		requireDuplicate(t, <-second)
	})

	t.Run("a hand-over skips a holder renamed concurrently", func(t *testing.T) {
		owner := mustCreate(t, prefix+"-owner10", prefix+"-ivan@example.invalid")
		legacyID := seedLegacyCollision(t, owner, prefix+"-legacy10@example.invalid")

		rename, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rename.Rollback(ctx) }()
		renamed := prefix + "-legacy10"
		if err := updateUser(ctx, rename, legacyID, models.UpdateUserInput{Username: &renamed}); err != nil {
			t.Fatal(err)
		}

		conn, pid := session(t)
		released := make(chan error, 1)
		go func() {
			email := prefix + "-ivan-new@example.invalid"
			released <- updateUser(ctx, conn, owner.ID, models.UpdateUserInput{Email: &email})
		}()
		waitBlocked(t, pid, released)
		if err := rename.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-released; err != nil {
			t.Fatalf("releasing the shared identifier: %v", err)
		}
		mustCreate(t, owner.Email, prefix+"-other10@example.invalid")
	})

	t.Run("a repeatable-read hand-over refuses a stale holder", func(t *testing.T) {
		owner := mustCreate(t, prefix+"-owner11", prefix+"-judy@example.invalid")
		legacyID := seedLegacyCollision(t, owner, prefix+"-legacy11@example.invalid")

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT count(*) FROM users"); err != nil {
			t.Fatal(err)
		}
		renamed := prefix + "-legacy11"
		if err := users.Update(ctx, legacyID, models.UpdateUserInput{Username: &renamed}); err != nil {
			t.Fatal(err)
		}
		email := prefix + "-judy-new@example.invalid"
		err = updateUser(ctx, tx, owner.ID, models.UpdateUserInput{Email: &email})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "40001" {
			t.Fatalf("err = %v, want a serialization failure", err)
		}
		_ = tx.Rollback(ctx)

		if err := users.Update(ctx, owner.ID, models.UpdateUserInput{Email: &email}); err != nil {
			t.Fatalf("retrying the release: %v", err)
		}
		mustCreate(t, owner.Email, prefix+"-other11@example.invalid")
	})

	t.Run("accounts exchanging identifiers concurrently do not deadlock", func(t *testing.T) {
		for i := range 20 {
			x := fmt.Sprintf("%s-swap%d-x@example.invalid", prefix, i)
			y := fmt.Sprintf("%s-swap%d-y@example.invalid", prefix, i)
			a := mustCreate(t, fmt.Sprintf("%s-swap%d-a", prefix, i), x)
			b := mustCreate(t, y, fmt.Sprintf("%s-swap%d-b@example.invalid", prefix, i))

			start := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-start
				results <- users.Update(ctx, a.ID, models.UpdateUserInput{Email: &y})
			}()
			go func() {
				<-start
				results <- users.Update(ctx, b.ID, models.UpdateUserInput{Username: &x})
			}()
			close(start)
			for range 2 {
				if err := <-results; err != nil && !IsDuplicate(err) {
					t.Fatalf("exchange %d: %v", i, err)
				}
			}
		}
	})
}
