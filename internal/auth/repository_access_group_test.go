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

	before, err := users.GetByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetByID() before update error: %v", err)
	}

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
	if user.AccessPolicyRevision != before.AccessPolicyRevision+1 {
		t.Fatalf("AccessPolicyRevision = %d after group change, want %d",
			user.AccessPolicyRevision, before.AccessPolicyRevision+1)
	}

	// Re-asserting the same group is a no-op for the policy revision.
	if err := users.Update(ctx, userID, models.UpdateUserInput{
		AccessGroupIDSet: true,
		AccessGroupID:    &groupID,
	}); err != nil {
		t.Fatalf("Update(same access_group_id) error: %v", err)
	}
	unchanged, err := users.GetByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetByID() after same-group update error: %v", err)
	}
	if unchanged.AccessPolicyRevision != user.AccessPolicyRevision {
		t.Fatalf("AccessPolicyRevision = %d after same-group update, want unchanged %d",
			unchanged.AccessPolicyRevision, user.AccessPolicyRevision)
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
	if user.AccessPolicyRevision != unchanged.AccessPolicyRevision+1 {
		t.Fatalf("AccessPolicyRevision = %d after ungrouping, want %d",
			user.AccessPolicyRevision, unchanged.AccessPolicyRevision+1)
	}
}

func TestUserRepositoryCreateAssignsDefaultAccessGroupDB(t *testing.T) {
	ctx, pool, suffix := newAccessGroupUserRepoDBTest(t)
	seedID := defaultAuthAccessGroupSeedID(t, ctx, pool)
	t.Cleanup(func() {
		restoreAuthDefaultAccessGroup(t, ctx, pool, seedID)
	})
	users := NewUserRepository(pool)

	defaultID := insertAuthAccessGroupTestGroupWithLabel(t, ctx, pool, suffix, "default")
	setAuthDefaultAccessGroup(t, ctx, pool, defaultID)

	created, err := users.Create(ctx, createAuthAccessGroupUserInput(suffix, "assigned-default", nil))
	if err != nil {
		t.Fatalf("Create(default assignment) error: %v", err)
	}
	if created.AccessGroupID == nil || *created.AccessGroupID != defaultID {
		t.Fatalf("AccessGroupID = %#v, want default group %d", created.AccessGroupID, defaultID)
	}

	adminInput := createAuthAccessGroupUserInput(suffix, "admin", nil)
	adminInput.Role = "admin"
	created, err = users.Create(ctx, adminInput)
	if err != nil {
		t.Fatalf("Create(admin) error: %v", err)
	}
	if created.AccessGroupID != nil {
		t.Fatalf("AccessGroupID = %#v for admin, want nil (admins stay ungrouped)", created.AccessGroupID)
	}

	explicitID := insertAuthAccessGroupTestGroupWithLabel(t, ctx, pool, suffix, "explicit")
	created, err = users.Create(ctx, createAuthAccessGroupUserInput(suffix, "explicit", &explicitID))
	if err != nil {
		t.Fatalf("Create(explicit group) error: %v", err)
	}
	if created.AccessGroupID == nil || *created.AccessGroupID != explicitID {
		t.Fatalf("AccessGroupID = %#v, want explicit group %d", created.AccessGroupID, explicitID)
	}

	clearAuthDefaultAccessGroup(t, ctx, pool)
	created, err = users.Create(ctx, createAuthAccessGroupUserInput(suffix, "no-default", nil))
	if err != nil {
		t.Fatalf("Create(no default) error: %v", err)
	}
	if created.AccessGroupID != nil {
		t.Fatalf("AccessGroupID = %#v, want nil without a default group", created.AccessGroupID)
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
	if !authAccessGroupDefaultColumnExists(t, ctx, pool) {
		t.Skip("test database has not applied default access group migration")
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
	return insertAuthAccessGroupTestGroupWithLabel(t, ctx, pool, suffix, "")
}

func insertAuthAccessGroupTestGroupWithLabel(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	label string,
) int64 {
	t.Helper()
	var id int64
	name := "Auth Access Group Test " + suffix
	if label != "" {
		name += " " + label
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO access_groups (name)
		VALUES ($1)
		RETURNING id`,
		name,
	).Scan(&id); err != nil {
		t.Fatalf("insert access group: %v", err)
	}
	return id
}

func createAuthAccessGroupUserInput(suffix, label string, groupID *int64) models.CreateUserInput {
	id := time.Now().UnixNano()
	return models.CreateUserInput{
		Email:         fmt.Sprintf("auth-access-group-test-%s-%s-%d@example.invalid", suffix, label, id),
		Username:      fmt.Sprintf("auth-access-group-test-%s-%s-%d", suffix, label, id),
		Password:      "password",
		Role:          "user",
		AccessGroupID: groupID,
	}
}

func authAccessGroupDefaultColumnExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'access_groups'
			  AND column_name = 'is_default'
		)`).Scan(&exists); err != nil {
		t.Fatalf("check access_groups.is_default column: %v", err)
	}
	return exists
}

func defaultAuthAccessGroupSeedID(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `
		SELECT id
		FROM access_groups
		WHERE name = 'Default Group'
		  AND is_default`).Scan(&id); err != nil {
		t.Fatalf("load seeded default access group: %v", err)
	}
	return id
}

func setAuthDefaultAccessGroup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, groupID int64) {
	t.Helper()
	clearAuthDefaultAccessGroup(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE access_groups
		SET is_default = true
		WHERE id = $1`, groupID); err != nil {
		t.Fatalf("set default access group: %v", err)
	}
}

func clearAuthDefaultAccessGroup(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE access_groups SET is_default = false WHERE is_default`); err != nil {
		t.Fatalf("clear default access group: %v", err)
	}
}

func restoreAuthDefaultAccessGroup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seedID int64) {
	t.Helper()
	setAuthDefaultAccessGroup(t, ctx, pool, seedID)
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
