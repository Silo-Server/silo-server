package auth

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestOwnerChecks(t *testing.T) {
	owner := &models.User{ID: 1, Role: models.RoleAdmin, Enabled: true, IsOwner: true}
	admin := &models.User{ID: 2, Role: models.RoleAdmin, Enabled: true}
	demote := models.UpdateUserInput{Role: new(models.RoleUser)}
	disable := models.UpdateUserInput{Enabled: new(false)}
	rename := models.UpdateUserInput{Username: new("renamed")}

	cases := []struct {
		name string
		err  error
		want error
	}{
		{"admin edits owner", CheckOwnerUpdate(admin.ID, owner, rename), ErrOwnerProtected},
		{"admin deletes owner", CheckOwnerDelete(admin.ID, owner), ErrOwnerProtected},
		{"admin targets owner", CheckOwnerTarget(admin.ID, owner), ErrOwnerProtected},
		{"no actor targets owner", CheckOwnerTarget(0, owner), ErrOwnerProtected},
		{"owner edits self", CheckOwnerUpdate(owner.ID, owner, rename), nil},
		{"owner demotes self", CheckOwnerUpdate(owner.ID, owner, demote), ErrOwnerStanding},
		{"owner disables self", CheckOwnerUpdate(owner.ID, owner, disable), ErrOwnerStanding},
		{"owner keeps admin role", CheckOwnerUpdate(owner.ID, owner, models.UpdateUserInput{Role: new(models.RoleAdmin), Enabled: new(true)}), nil},
		{"owner deletes self", CheckOwnerDelete(owner.ID, owner), ErrOwnerStanding},
		{"owner edits admin", CheckOwnerUpdate(owner.ID, admin, demote), nil},
		{"owner deletes admin", CheckOwnerDelete(owner.ID, admin), nil},
		{"admin edits admin", CheckOwnerUpdate(3, admin, disable), nil},
	}
	for _, tc := range cases {
		if !errors.Is(tc.err, tc.want) || (tc.want == nil && tc.err != nil) {
			t.Errorf("%s: err = %v, want %v", tc.name, tc.err, tc.want)
		}
	}
}

func TestInitialSetupClaimsOwnerPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	s := &Service{jwt: NewJWTService("owner-test-secret-0000000000000000000000", time.Minute, time.Hour), sessions: NewSessionRepository(r.pool), users: r, accounts: NewAccountProvisioner(r, nil)}
	_, created, err := s.SetupInitialUser(t.Context(), "owner", "owner@example.test", "initial-password", false, "", "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := r.GetByID(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !created.IsOwner || !stored.IsOwner {
		t.Fatalf("initial account is not the owner: returned %v, stored %v", created.IsOwner, stored.IsOwner)
	}
	other := testAdminAccount(t, r)
	if other.IsOwner {
		t.Fatal("a later account became the owner")
	}
	if err := r.CheckOwnerTargetByID(t.Context(), other.ID, created.ID); !errors.Is(err, ErrOwnerProtected) {
		t.Fatalf("another account targeting the owner: %v", err)
	}
	if err := r.CheckOwnerTargetByID(t.Context(), created.ID, created.ID); err != nil {
		t.Fatalf("owner targeting itself: %v", err)
	}
	if err := r.CheckOwnerTargetByID(t.Context(), created.ID, other.ID); err != nil {
		t.Fatalf("owner targeting another account: %v", err)
	}
	if err := r.CheckOwnerTargetByID(t.Context(), created.ID, other.ID+1000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
}

func TestOwnerImpersonationPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	s := &Service{jwt: NewJWTService("owner-test-secret-0000000000000000000000", time.Minute, time.Hour), sessions: NewSessionRepository(r.pool), users: r}
	account := func(role string, owner bool) *models.User {
		t.Helper()
		u, err := r.Create(t.Context(), models.CreateUserInput{Username: uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "original-password", Role: role})
		if err != nil {
			t.Fatal(err)
		}
		if owner {
			if _, err := r.pool.Exec(t.Context(), `UPDATE users SET is_owner = true WHERE id = $1`, u.ID); err != nil {
				t.Fatal(err)
			}
		}
		return u
	}
	owner := account(models.RoleAdmin, true)
	admin := account(models.RoleAdmin, false)
	otherAdmin := account(models.RoleAdmin, false)
	user := account(models.RoleUser, false)

	cases := []struct {
		name          string
		actor, target *models.User
		allowed       bool
	}{
		{"owner views as admin", owner, admin, true},
		{"owner views as user", owner, user, true},
		{"admin views as user", admin, user, true},
		{"admin views as admin", admin, otherAdmin, false},
		{"admin views as owner", admin, owner, false},
	}
	for _, tc := range cases {
		_, _, target, err := s.StartImpersonation(t.Context(), tc.actor.ID, tc.target.ID, "test", "127.0.0.1")
		if tc.allowed && (err != nil || target.ID != tc.target.ID) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
		if !tc.allowed && !errors.Is(err, ErrImpersonationNotAllowed) {
			t.Errorf("%s: err = %v, want ErrImpersonationNotAllowed", tc.name, err)
		}
	}
}

func TestServerOwnerMigrationPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	// Return the copied table to its shape before the migration, then seed it.
	if _, err := r.pool.Exec(t.Context(), `ALTER TABLE users DROP COLUMN is_owner`); err != nil {
		t.Fatal(err)
	}
	_, err := r.pool.Exec(t.Context(), `INSERT INTO users(username,email,password_hash,role,enabled,created_at) VALUES
	 ('disabled-admin','a@example.test','x','admin',false,'2020-01-01'),
	 ('older-user','b@example.test','x','user',true,'2020-06-01'),
	 ('first-admin','c@example.test','x','admin',true,'2021-01-01'),
	 ('later-admin','d@example.test','x','admin',true,'2022-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260926045319_users_server_owner.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.pool.Exec(t.Context(), strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	var owners []string
	rows, err := r.pool.Query(t.Context(), `SELECT username FROM users WHERE is_owner`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		owners = append(owners, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0] != "first-admin" {
		t.Fatalf("owners = %v, want the earliest enabled admin", owners)
	}
	for name, statement := range map[string]string{
		"second owner":  `UPDATE users SET is_owner = true WHERE username = 'later-admin'`,
		"demote owner":  `UPDATE users SET role = 'user' WHERE username = 'first-admin'`,
		"disable owner": `UPDATE users SET enabled = false WHERE username = 'first-admin'`,
	} {
		if _, err := r.pool.Exec(t.Context(), statement); err == nil {
			t.Errorf("%s: the database accepted it", name)
		}
	}
}
