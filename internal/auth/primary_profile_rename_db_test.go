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
	ident := func(key, suffix string) string { return prefix + "-" + key + "-" + suffix }

	// Another account whose primary profile shares a renamed account's old
	// username must keep it.
	bystander := insertPrimaryProfileRenameUser(t, ctx, pool, ident("z", "bystander"), ident("z", "mail")+"@example.invalid")
	insertPrimaryProfileRenameProfile(t, ctx, pool, bystander, "primary", ident("a", "old"), true)

	tests := []struct {
		name        string
		key         string
		oldUsername string // "" stores a NULL username
		newUsername string
		primary     string
		other       string
		wantPrimary string
	}{
		{
			name: "untouched primary follows", key: "a", oldUsername: ident("a", "old"), newUsername: ident("a", "new"),
			primary: ident("a", "old"), other: "Kid", wantPrimary: ident("a", "new"),
		},
		{
			name: "primary differing in case follows", key: "b", oldUsername: ident("b", "old"), newUsername: ident("b", "new"),
			primary: strings.ToUpper(ident("b", "old")), other: "Kid", wantPrimary: ident("b", "new"),
		},
		{
			// The primary profile is not a conflict with its own new name.
			name: "case-only username change follows", key: "c", oldUsername: strings.ToUpper(ident("c", "user")), newUsername: ident("c", "user"),
			primary: strings.ToUpper(ident("c", "user")), other: "Kid", wantPrimary: ident("c", "user"),
		},
		{
			// A secondary profile named after the old username never changes.
			name: "renamed primary keeps its name", key: "d", oldUsername: ident("d", "old"), newUsername: ident("d", "new"),
			primary: "Mum", other: ident("d", "old"), wantPrimary: "Mum",
		},
		{
			// The profile API counts final sigma and sigma as the same letter.
			name: "taken name keeps primary", key: "e", oldUsername: ident("e", "old"), newUsername: ident("e", "newσ"),
			primary: ident("e", "old"), other: strings.ToUpper(ident("e", "new")) + "ς", wantPrimary: ident("e", "old"),
		},
		{
			// A restored email-only account has a NULL username.
			name: "account without a username", key: "f", newUsername: ident("f", "new"),
			primary: "Viewer", other: "Kid", wantPrimary: "Viewer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := insertPrimaryProfileRenameUser(t, ctx, pool, tc.oldUsername, ident(tc.key, "mail")+"@example.invalid")
			insertPrimaryProfileRenameProfile(t, ctx, pool, id, "primary", tc.primary, true)
			insertPrimaryProfileRenameProfile(t, ctx, pool, id, "other", tc.other, false)

			newUsername := tc.newUsername
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
	t.Run("primary profile being saved keeps its name", func(t *testing.T) {
		id := insertPrimaryProfileRenameUser(t, ctx, pool, ident("g", "old"), ident("g", "mail")+"@example.invalid")
		insertPrimaryProfileRenameProfile(t, ctx, pool, id, "primary", ident("g", "old"), true)
		// A profile save holds the row; waiting for it could deadlock.
		save, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin profile save: %v", err)
		}
		t.Cleanup(func() { _ = save.Rollback(context.Background()) })
		if _, err := save.Exec(ctx, `UPDATE user_profiles SET updated_at = updated_at WHERE user_id = $1 AND is_primary`, id); err != nil {
			t.Fatalf("hold primary profile: %v", err)
		}

		renameCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		newUsername := ident("g", "new")
		if _, err := users.MutateAdminAccount(renameCtx, id, -1, &models.UpdateUserInput{Username: &newUsername},
			func(*models.User, pgx.Tx) (bool, error) { return false, nil }); err != nil {
			t.Fatalf("MutateAdminAccount(username) while the profile is saved: %v", err)
		}
		if got := primaryProfileRenameName(t, ctx, pool, id, "primary"); got != ident("g", "old") {
			t.Fatalf("primary profile name = %q, want unchanged %q", got, ident("g", "old"))
		}
	})
	if got := primaryProfileRenameName(t, ctx, pool, bystander, "primary"); got != ident("a", "old") {
		t.Fatalf("bystander primary profile name = %q, want unchanged %q", got, ident("a", "old"))
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
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username ILIKE $1 OR email LIKE $1`, prefix+"%")
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
