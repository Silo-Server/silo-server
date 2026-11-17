package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestUserRepositoryUsernameChangeRenamesPrimaryProfileDB covers issue #3450:
// renaming an account renames its primary profile only while that profile
// still carries the old username.
func TestUserRepositoryUsernameChangeRenamesPrimaryProfileDB(t *testing.T) {
	ctx, pool, prefix := newPrimaryProfileRenameDBTest(t)
	users := NewUserRepository(pool)
	name := func(key, suffix string) string { return prefix + "-" + key + "-" + suffix }

	tests := []struct {
		name        string
		key         string
		oldUsername string // "" stores a NULL username
		primary     string
		other       string
		wantPrimary string
	}{
		{
			name: "untouched primary follows", key: "a", oldUsername: name("a", "old"),
			primary: name("a", "old"), other: "Kid", wantPrimary: name("a", "new"),
		},
		{
			name: "case-only match follows", key: "b", oldUsername: name("b", "old"),
			primary: strings.ToUpper(name("b", "old")), other: "Kid", wantPrimary: name("b", "new"),
		},
		{
			// The other profile carries the old username, so only the primary
			// profile may follow the account.
			name: "renamed primary keeps its name", key: "c", oldUsername: name("c", "old"),
			primary: "Mum", other: name("c", "old"), wantPrimary: "Mum",
		},
		{
			name: "taken name keeps primary", key: "d", oldUsername: name("d", "old"),
			primary: name("d", "old"), other: strings.ToUpper(name("d", "new")), wantPrimary: name("d", "old"),
		},
		{
			// A restored email-only account has a NULL username.
			name: "account without a username", key: "e",
			primary: "Viewer", other: "Kid", wantPrimary: "Viewer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := insertPrimaryProfileRenameUser(t, ctx, pool, tc.oldUsername, name(tc.key, "mail")+"@example.invalid")
			insertPrimaryProfileRenameProfile(t, ctx, pool, id, "primary", tc.primary, true)
			insertPrimaryProfileRenameProfile(t, ctx, pool, id, "other", tc.other, false)

			newUsername := name(tc.key, "new")
			if _, err := users.MutateAdminAccount(ctx, id, -1, &models.UpdateUserInput{Username: &newUsername},
				func(*models.User, pgx.Tx) (bool, error) { return false, nil }); err != nil {
				t.Fatalf("MutateAdminAccount(username) error: %v", err)
			}

			user, err := users.GetByID(ctx, id)
			if err != nil {
				t.Fatalf("GetByID() error: %v", err)
			}
			if user.Username != newUsername {
				t.Fatalf("username = %q, want %q", user.Username, newUsername)
			}
			if got := primaryProfileRenameName(t, ctx, pool, id, "primary"); got != tc.wantPrimary {
				t.Fatalf("primary profile name = %q, want %q", got, tc.wantPrimary)
			}
			if got := primaryProfileRenameName(t, ctx, pool, id, "other"); got != tc.other {
				t.Fatalf("other profile name = %q, want unchanged %q", got, tc.other)
			}
		})
	}
}

func newPrimaryProfileRenameDBTest(t *testing.T) (context.Context, *pgxpool.Pool, string) {
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
	prefix := fmt.Sprintf("auth-profile-rename-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1 OR email LIKE $1`, prefix+"%")
	})
	return ctx, pool, prefix
}

func insertPrimaryProfileRenameUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, username, email string) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES (NULLIF($1, ''), $2, 'x', 'user', true)
		RETURNING id`, username, email).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func insertPrimaryProfileRenameProfile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int, profileID, name string, primary bool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, $3, $4)`,
		fmt.Sprintf("%d-%s", userID, profileID), userID, name, primary); err != nil {
		t.Fatalf("insert profile %s: %v", profileID, err)
	}
}

func primaryProfileRenameName(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int, profileID string) string {
	t.Helper()
	var name string
	if err := pool.QueryRow(ctx, `SELECT name FROM user_profiles WHERE user_id = $1 AND id = $2`,
		userID, fmt.Sprintf("%d-%s", userID, profileID)).Scan(&name); err != nil {
		t.Fatalf("read profile %s: %v", profileID, err)
	}
	return name
}
