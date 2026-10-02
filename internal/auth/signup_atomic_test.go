package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestInvitedAccountAtomicDB(t *testing.T) {
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
	prefix := fmt.Sprintf("signup-atomic-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanup, "DELETE FROM invite_codes WHERE code LIKE $1", prefix+"%")
		_, _ = pool.Exec(cleanup, "DELETE FROM users WHERE username LIKE $1", prefix+"%")
	})
	var creatorID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,email,password_hash,role,enabled) VALUES ($1,$2,'x','admin',true) RETURNING id`, prefix+"-creator", prefix+"-creator@example.invalid").Scan(&creatorID); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepository(pool)
	invites := NewInviteCodeRepository(pool)
	accounts := NewAccountProvisioner(users, pgstore.NewPostgresProvider(pool))
	input := func(name string) CreateAccountInput {
		return CreateAccountInput{User: models.CreateUserInput{Username: prefix + name, Email: prefix + name + "@example.invalid", Password: "test-password", Role: "user"}, DefaultProfile: DefaultProfileOptions{Enabled: true, Name: "Home"}}
	}
	code := func(t *testing.T, name string, maxUses int) string {
		t.Helper()
		value := prefix + name
		if _, err := pool.Exec(ctx, "INSERT INTO invite_codes (code, max_uses, created_by) VALUES ($1, $2, $3)", value, maxUses, creatorID); err != nil {
			t.Fatal(err)
		}
		return value
	}
	uses := func(t *testing.T, code string) int {
		t.Helper()
		ic, err := invites.GetByCode(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		return ic.UseCount
	}
	t.Run("concurrent duplicate consumes one use", func(t *testing.T) {
		invite := code(t, "-duplicate", 10)
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() { <-start; _, err := accounts.CreateInvitedAccount(ctx, input("-same"), invite); results <- err })
		}
		close(start)
		wg.Wait()
		close(results)
		var success, duplicate int
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, ErrDuplicate) {
				duplicate++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || duplicate != 1 || uses(t, invite) != 1 {
			t.Fatalf("success=%d duplicate=%d uses=%d", success, duplicate, uses(t, invite))
		}
		user, err := users.GetByUsername(ctx, input("-same").User.Username)
		if err != nil {
			t.Fatal(err)
		}
		var profiles int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM user_profiles WHERE user_id=$1", user.ID).Scan(&profiles); err != nil {
			t.Fatal(err)
		}
		if profiles != 1 {
			t.Fatalf("profiles=%d, want 1", profiles)
		}
		if _, err := accounts.CreateInvitedAccount(ctx, input("-same"), invite); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("retry error=%v", err)
		}
		if uses(t, invite) != 1 {
			t.Fatalf("retry consumed another use: %d", uses(t, invite))
		}
	})
	t.Run("profile failure rolls back account and invite", func(t *testing.T) {
		invite := code(t, "-failed", 1)
		unavailable := NewAccountProvisioner(users, nil)
		if _, err := unavailable.CreateInvitedAccount(ctx, input("-failed"), invite); err == nil {
			t.Fatal("expected profile failure")
		}
		if _, err := users.GetByUsername(ctx, input("-failed").User.Username); !errors.Is(err, ErrNotFound) {
			t.Fatalf("failed account exists: %v", err)
		}
		if uses(t, invite) != 0 {
			t.Fatalf("failed creation consumed invite: %d", uses(t, invite))
		}
		if _, err := accounts.CreateInvitedAccount(ctx, input("-failed"), invite); err != nil {
			t.Fatalf("retry after rollback: %v", err)
		}
		if uses(t, invite) != 1 {
			t.Fatalf("uses=%d, want 1", uses(t, invite))
		}
	})
	t.Run("invalid account rolls back invite", func(t *testing.T) {
		invite := code(t, "-invalid", 1)
		bad := input("-invalid")
		bad.User.Password = strings.Repeat("x", 73)
		if _, err := accounts.CreateInvitedAccount(ctx, bad, invite); err == nil {
			t.Fatal("expected password hashing failure")
		}
		if uses(t, invite) != 0 {
			t.Fatalf("invalid account consumed invite: %d", uses(t, invite))
		}
		if _, err := users.GetByUsername(ctx, bad.User.Username); !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid account exists: %v", err)
		}
	})
	t.Run("exhaustion refuses account", func(t *testing.T) {
		invite := code(t, "-single", 1)
		if _, err := accounts.CreateInvitedAccount(ctx, input("-first"), invite); err != nil {
			t.Fatal(err)
		}
		if _, err := accounts.CreateInvitedAccount(ctx, input("-second"), invite); !errors.Is(err, ErrInviteCodeExhausted) {
			t.Fatalf("error=%v", err)
		}
		if _, err := users.GetByUsername(ctx, input("-second").User.Username); !errors.Is(err, ErrNotFound) {
			t.Fatalf("refused account exists: %v", err)
		}
		if uses(t, invite) != 1 {
			t.Fatalf("uses=%d, want 1", uses(t, invite))
		}
	})
}
