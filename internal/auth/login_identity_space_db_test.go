package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestLoginIdentitySpaceDB pins the users_login_identity_space trigger: no
// account's username may equal another account's email, because LookupLogin
// resolves a typed identifier against the username column before the email
// column and would otherwise sign in or reset the wrong account.
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
	create := func(t *testing.T, username, email string) (*models.User, error) {
		t.Helper()
		return users.Create(ctx, models.CreateUserInput{Username: username, Email: email, Password: "test-password", Role: "user"})
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
		if !strings.Contains(err.Error(), "users_login_identity_space") {
			t.Fatalf("err = %v, want the users_login_identity_space constraint", err)
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
	})

	t.Run("an existing collision stays editable", func(t *testing.T) {
		owner := mustCreate(t, prefix+"-owner4", prefix+"-carol@example.invalid")
		// Rows written before the trigger existed can already collide.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
			t.Skipf("cannot bypass triggers to seed a legacy collision: %v", err)
		}
		var legacyID int
		if err := tx.QueryRow(ctx, `INSERT INTO users (username,email,password_hash,role,enabled) VALUES ($1,$2,'x','user',true) RETURNING id`, owner.Email, prefix+"-legacy@example.invalid").Scan(&legacyID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		username := owner.Email
		enabled := false
		if err := users.Update(ctx, legacyID, models.UpdateUserInput{Username: &username, Enabled: &enabled}); err != nil {
			t.Fatalf("editing a legacy collision without changing identifiers: %v", err)
		}
		fixed := prefix + "-legacy"
		if err := users.Update(ctx, legacyID, models.UpdateUserInput{Username: &fixed}); err != nil {
			t.Fatalf("renaming a legacy collision away: %v", err)
		}
	})

	t.Run("concurrent writers claiming one identifier serialize", func(t *testing.T) {
		identifier := prefix + "-dave@example.invalid"
		first, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = first.Rollback(ctx) }()
		if _, err := createUser(ctx, first, models.CreateUserInput{Username: prefix + "-owner5", Email: identifier, Password: "test-password", Role: "user"}); err != nil {
			t.Fatal(err)
		}

		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		var pid int
		if err := conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			t.Fatal(err)
		}
		second := make(chan error, 1)
		go func() {
			_, err := createUser(ctx, conn, models.CreateUserInput{Username: strings.ToUpper(identifier), Email: prefix + "-other5@example.invalid", Password: "test-password", Role: "user"})
			second <- err
		}()

		// The second writer must wait on the first's uncommitted claim rather
		// than miss it; commit only once it is blocked.
		deadline := time.Now().Add(10 * time.Second)
		for {
			var waiting bool
			if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)", pid).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			select {
			case err := <-second:
				t.Fatalf("second writer finished before the first committed: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("second writer never blocked on the first writer's claim")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := first.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		requireDuplicate(t, <-second)
	})
}
