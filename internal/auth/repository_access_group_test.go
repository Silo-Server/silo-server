package auth

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestUserRepositoryUpdateAccessGroupIDDB(t *testing.T) {
	ctx, pool, suffix := newAccessGroupUserRepoDBTest(t)
	groupID := insertAuthAccessGroupTestGroup(t, ctx, pool, suffix)
	userID := insertAuthAccessGroupTestUser(t, ctx, pool, suffix)
	users := NewUserRepository(pool)

	if err := users.Update(ctx, userID, models.UpdateUserInput{
		AccessGroupIDSet: true,
		AccessGroupID:    &groupID,
	}); err != nil {
		t.Fatalf("Update(access_group_id) error: %v", err)
	}
	user, err := users.GetByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetByID() error: %v", err)
	}
	if user.AccessGroupID == nil || *user.AccessGroupID != groupID {
		t.Fatalf("AccessGroupID = %#v, want %d", user.AccessGroupID, groupID)
	}

	if err := users.Update(ctx, userID, models.UpdateUserInput{AccessGroupIDSet: true}); err != nil {
		t.Fatalf("Update(access_group_id null) error: %v", err)
	}
	user, err = users.GetByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetByID() after null error: %v", err)
	}
	if user.AccessGroupID != nil {
		t.Fatalf("AccessGroupID = %#v, want nil", user.AccessGroupID)
	}
}

func newAccessGroupUserRepoDBTest(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.access_groups')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check access_groups table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied access groups migration")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, "auth-access-group-test-"+suffix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM access_groups WHERE name LIKE $1`, "Auth Access Group Test "+suffix+"%")
	})
	return ctx, pool, suffix
}

func insertAuthAccessGroupTestGroup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO access_groups (name)
		VALUES ($1)
		RETURNING id`,
		"Auth Access Group Test "+suffix,
	).Scan(&id); err != nil {
		t.Fatalf("insert access group: %v", err)
	}
	return id
}

func insertAuthAccessGroupTestUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, role, enabled)
		VALUES ($1, 'user', true)
		RETURNING id`,
		"auth-access-group-test-"+suffix,
	).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}
