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
	admin := &models.User{ID: 2, Role: models.RoleAdmin, Enabled: true, LocalPasswordLoginEnabled: true}
	// providerAdmin and providerOwner sign in only through a provider.
	providerAdmin := &models.User{ID: 5, Role: models.RoleAdmin, Enabled: true}
	providerOwner := &models.User{ID: 1, Role: models.RoleAdmin, Enabled: true, IsOwner: true}
	user := &models.User{ID: 4, Role: models.RoleUser, Enabled: true}
	asOwner := OwnerActor{ID: owner.ID, IsOwner: true}
	asAdmin := OwnerActor{ID: 3}
	demote := models.UpdateUserInput{Role: new(models.RoleUser)}
	promote := models.UpdateUserInput{Role: new(models.RoleAdmin)}
	disable := models.UpdateUserInput{Enabled: new(false)}
	rename := models.UpdateUserInput{Username: new("renamed")}

	cases := []struct {
		name string
		err  error
		want error
	}{
		{"admin edits owner", CheckOwnerUpdate(asAdmin, owner, rename), ErrOwnerProtected},
		{"admin deletes owner", CheckOwnerDelete(asAdmin, owner), ErrOwnerProtected},
		{"admin targets owner", CheckOwnerTarget(asAdmin, owner), ErrOwnerProtected},
		{"no actor targets owner", CheckOwnerTarget(OwnerActor{}, owner), ErrOwnerProtected},
		{"owner edits self", CheckOwnerUpdate(asOwner, owner, rename), nil},
		{"owner demotes self", CheckOwnerUpdate(asOwner, owner, demote), ErrOwnerStanding},
		{"owner disables self", CheckOwnerUpdate(asOwner, owner, disable), ErrOwnerStanding},
		{"owner keeps admin role", CheckOwnerUpdate(asOwner, owner, models.UpdateUserInput{Role: new(models.RoleAdmin), Enabled: new(true)}), nil},
		{"owner deletes self", CheckOwnerDelete(asOwner, owner), ErrOwnerStanding},
		{"owner edits admin", CheckOwnerUpdate(asOwner, admin, demote), nil},
		{"owner deletes admin", CheckOwnerDelete(asOwner, admin), nil},
		{"owner promotes user", CheckOwnerUpdate(asOwner, user, promote), nil},
		{"owner grants admin", CheckGrantAdmin(asOwner, models.RoleAdmin), nil},
		{"admin edits admin", CheckOwnerUpdate(asAdmin, admin, rename), ErrAdminProtected},
		{"admin disables admin", CheckOwnerUpdate(asAdmin, admin, disable), ErrAdminProtected},
		{"admin deletes admin", CheckOwnerDelete(asAdmin, admin), ErrAdminProtected},
		{"admin targets admin", CheckOwnerTarget(asAdmin, admin), ErrAdminProtected},
		{"admin edits self", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, rename), nil},
		{"admin demotes self", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, demote), ErrSelfStanding},
		{"admin disables self", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, disable), ErrSelfStanding},
		{"admin deletes self", CheckOwnerDelete(OwnerActor{ID: admin.ID}, admin), ErrSelfStanding},
		{"admin resends own role", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, promote), nil},
		{"admin promotes user", CheckOwnerUpdate(asAdmin, user, promote), ErrAdminProtected},
		{"admin edits user", CheckOwnerUpdate(asAdmin, user, disable), nil},
		{"admin makes itself break-glass", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, models.UpdateUserInput{BreakGlass: new(true)}), ErrBreakGlassOwnerOnly},
		{"admin resends its break-glass flag", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, models.UpdateUserInput{BreakGlass: new(false)}), nil},
		{"owner makes an admin break-glass", CheckOwnerUpdate(asOwner, admin, models.UpdateUserInput{BreakGlass: new(true)}), nil},
		{"owner makes itself break-glass", CheckOwnerUpdate(asOwner, owner, models.UpdateUserInput{BreakGlass: new(true)}), nil},
		{"provider-only admin sets own password", CheckOwnerUpdate(OwnerActor{ID: providerAdmin.ID}, providerAdmin, models.UpdateUserInput{Password: new("long-enough")}), ErrSelfPasswordOwnerOnly},
		{"provider-only admin edits self", CheckOwnerUpdate(OwnerActor{ID: providerAdmin.ID}, providerAdmin, rename), nil},
		{"local admin sets own password", CheckOwnerUpdate(OwnerActor{ID: admin.ID}, admin, models.UpdateUserInput{Password: new("long-enough")}), nil},
		{"provider-only owner sets own password", CheckOwnerUpdate(asOwner, providerOwner, models.UpdateUserInput{Password: new("long-enough")}), nil},
		{"owner sets a provider-only admin's password", CheckOwnerUpdate(asOwner, providerAdmin, models.UpdateUserInput{Password: new("long-enough")}), nil},
		{"admin deletes user", CheckOwnerDelete(asAdmin, user), nil},
		{"admin grants admin", CheckGrantAdmin(asAdmin, models.RoleAdmin), ErrAdminProtected},
		{"admin grants user", CheckGrantAdmin(asAdmin, models.RoleUser), nil},
	}
	for _, tc := range cases {
		if !errors.Is(tc.err, tc.want) || (tc.want == nil && tc.err != nil) {
			t.Errorf("%s: err = %v, want %v", tc.name, tc.err, tc.want)
		}
	}
}

func TestTransferOwnershipPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	owner := testRoleAccount(t, r, models.RoleAdmin)
	if _, err := r.pool.Exec(t.Context(), `UPDATE users SET is_owner = true WHERE id = $1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	admin := testRoleAccount(t, r, models.RoleAdmin)
	user := testRoleAccount(t, r, models.RoleUser)
	disabled := testRoleAccount(t, r, models.RoleAdmin)
	if err := r.Update(t.Context(), disabled.ID, models.UpdateUserInput{Enabled: new(false)}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		from, to int
		want     error
	}{
		{"to a user", owner.ID, user.ID, ErrOwnershipTarget},
		{"to a disabled admin", owner.ID, disabled.ID, ErrOwnershipTarget},
		{"to itself", owner.ID, owner.ID, ErrOwnershipTarget},
		{"to a missing account", owner.ID, user.ID + 1000, ErrNotFound},
		{"by an admin to another", admin.ID, owner.ID, ErrNotOwner},
	} {
		if err := r.TransferOwnership(t.Context(), tc.from, tc.to); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	requireOwner(t, r, owner.ID)

	if err := r.TransferOwnership(t.Context(), owner.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	requireOwner(t, r, admin.ID)
	previous, err := r.GetByID(t.Context(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if previous.IsOwner || previous.Role != models.RoleAdmin || !previous.Enabled {
		t.Fatalf("previous owner: owner %v role %s enabled %v", previous.IsOwner, previous.Role, previous.Enabled)
	}
	// The new Owner becomes break-glass; the previous one keeps its own flag.
	if next, err := r.GetByID(t.Context(), admin.ID); err != nil || !next.BreakGlass {
		t.Fatalf("new owner break-glass: %+v, %v", next, err)
	}
	if previous.BreakGlass {
		t.Fatal("transfer made the previous owner break-glass")
	}
	if actor, err := r.OwnerActor(t.Context(), admin.ID); err != nil || !actor.IsOwner {
		t.Fatalf("new owner actor: %+v, %v", actor, err)
	}
	if actor, err := r.OwnerActor(t.Context(), owner.ID); err != nil || actor.IsOwner {
		t.Fatalf("previous owner actor: %+v, %v", actor, err)
	}
}

func TestSetOwnerPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	if _, _, err := r.SetOwner(t.Context(), "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
	// With no Owner yet, a disabled user account is enabled, promoted and made Owner.
	first := testRoleAccount(t, r, models.RoleUser)
	if err := r.Update(t.Context(), first.ID, models.UpdateUserInput{Enabled: new(false)}); err != nil {
		t.Fatal(err)
	}
	got, previous, err := r.SetOwner(t.Context(), strings.ToUpper(first.Username))
	if err != nil || previous != 0 {
		t.Fatalf("first owner: previous %d, err %v", previous, err)
	}
	if !got.IsOwner || got.Role != models.RoleAdmin || !got.Enabled || got.AccessGroupID != nil || !got.BreakGlass {
		t.Fatalf("first owner: %+v", got)
	}
	requireOwner(t, r, first.ID)

	second := testRoleAccount(t, r, models.RoleAdmin)
	if _, previous, err = r.SetOwner(t.Context(), second.Username); err != nil || previous != first.ID {
		t.Fatalf("second owner: previous %d, err %v", previous, err)
	}
	requireOwner(t, r, second.ID)
	if _, previous, err = r.SetOwner(t.Context(), second.Username); err != nil || previous != second.ID {
		t.Fatalf("repeat: previous %d, err %v", previous, err)
	}
	requireOwner(t, r, second.ID)
}

func TestPromotionRevokesIssuedCredentialsPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	user := testRoleAccount(t, r, models.RoleUser)
	admin := testRoleAccount(t, r, models.RoleAdmin)
	for _, u := range []*models.User{user, admin} {
		issueTestCredentials(t, r, u.ID)
	}

	// Saving an admin with its role unchanged keeps its credentials.
	if err := r.Update(t.Context(), admin.ID, models.UpdateUserInput{Role: new(models.RoleAdmin)}); err != nil {
		t.Fatal(err)
	}
	if keys, links := countTestCredentials(t, r, admin.ID); keys != 1 || links != 1 {
		t.Fatalf("admin saved with its role: keys %d, links %d", keys, links)
	}
	// Promotion drops the keys and reset links other admins could hold.
	if err := r.Update(t.Context(), user.ID, models.UpdateUserInput{Role: new(models.RoleAdmin)}); err != nil {
		t.Fatal(err)
	}
	if keys, links := countTestCredentials(t, r, user.ID); keys != 0 || links != 0 {
		t.Fatalf("promoted account: keys %d, links %d", keys, links)
	}
}

func TestOwnershipMoveEndsViewAsSessionsAndCredentialsPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	owner := testRoleAccount(t, r, models.RoleAdmin)
	if _, err := r.pool.Exec(t.Context(), `UPDATE users SET is_owner = true WHERE id = $1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	admin := testRoleAccount(t, r, models.RoleAdmin)
	other := testRoleAccount(t, r, models.RoleAdmin)
	user := testRoleAccount(t, r, models.RoleUser)
	sessions := NewSessionRepository(r.pool)
	for _, s := range []models.AuthSession{
		{ID: "own", UserID: admin.ID},
		{ID: "view-as", UserID: admin.ID, ImpersonatorUserID: &owner.ID},
		{ID: "old-owner-as-admin", UserID: other.ID, ImpersonatorUserID: &owner.ID},
		{ID: "old-owner-as-user", UserID: user.ID, ImpersonatorUserID: &owner.ID},
	} {
		s.DeviceName, s.IPAddress, s.ExpiresAt = "test", "127.0.0.1", time.Now().Add(time.Hour)
		if err := sessions.Create(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	issueTestCredentials(t, r, admin.ID)

	if err := r.TransferOwnership(t.Context(), owner.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	for id, wantRevoked := range map[string]bool{"own": false, "view-as": true, "old-owner-as-admin": true, "old-owner-as-user": false} {
		var revoked bool
		if err := r.pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM auth_sessions WHERE id = $1`, id).Scan(&revoked); err != nil {
			t.Fatal(err)
		}
		if revoked != wantRevoked {
			t.Errorf("session %s: revoked %v, want %v", id, revoked, wantRevoked)
		}
	}
	if keys, links := countTestCredentials(t, r, admin.ID); keys != 0 || links != 0 {
		t.Fatalf("new owner: keys %d, links %d; want both gone", keys, links)
	}
}

func TestOwnershipMoveRevokesPendingAdminInvitationsPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	owner := testRoleAccount(t, r, models.RoleAdmin)
	if _, err := r.pool.Exec(t.Context(), `UPDATE users SET is_owner = true WHERE id = $1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	admin := testRoleAccount(t, r, models.RoleAdmin)
	for _, inv := range []struct{ email, role string }{{"admin-invite", "admin"}, {"user-invite", "user"}} {
		if _, err := r.pool.Exec(t.Context(), `
			INSERT INTO invitations (email, token_hash, role, invited_by, expires_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, now() + interval '1 day', now(), now())`,
			inv.email+"@example.test", uuid.NewString(), inv.role, owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.TransferOwnership(t.Context(), owner.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	for email, wantRevoked := range map[string]bool{"admin-invite@example.test": true, "user-invite@example.test": false} {
		var revoked bool
		if err := r.pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM invitations WHERE email = $1`, email).Scan(&revoked); err != nil {
			t.Fatal(err)
		}
		if revoked != wantRevoked {
			t.Errorf("%s: revoked %v, want %v", email, revoked, wantRevoked)
		}
	}
}

// issueTestCredentials gives account id one API key and one reset link.
func issueTestCredentials(t *testing.T, r *UserRepository, id int) {
	t.Helper()
	if _, err := r.pool.Exec(t.Context(), `
		INSERT INTO api_keys (id, user_id, label, api_key, revision) VALUES ($1, $2, 'held', $3, 1)`,
		time.Now().UnixNano(), id, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(t.Context(), `
		INSERT INTO password_reset_tokens (user_id, token_hash, password_fingerprint, expires_at)
		VALUES ($1, $2, 'fingerprint', now() + interval '1 hour')`, id, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
}

func countTestCredentials(t *testing.T, r *UserRepository, id int) (keys, links int) {
	t.Helper()
	if err := r.pool.QueryRow(t.Context(), `
		SELECT (SELECT count(*) FROM api_keys WHERE user_id = $1),
		       (SELECT count(*) FROM password_reset_tokens WHERE user_id = $1)`, id).Scan(&keys, &links); err != nil {
		t.Fatal(err)
	}
	return keys, links
}

func testRoleAccount(t *testing.T, r *UserRepository, role string) *models.User {
	t.Helper()
	u, err := r.Create(t.Context(), models.CreateUserInput{Username: uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "original-password", Role: role})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// requireOwner fails unless id is the only Owner.
func requireOwner(t *testing.T, r *UserRepository, id int) {
	t.Helper()
	var owners []int
	rows, err := r.pool.Query(t.Context(), `SELECT id FROM users WHERE is_owner ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var owner int
		if err := rows.Scan(&owner); err != nil {
			t.Fatal(err)
		}
		owners = append(owners, owner)
	}
	if len(owners) != 1 || owners[0] != id {
		t.Fatalf("owners = %v, want [%d]", owners, id)
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
	if !created.BreakGlass || !stored.BreakGlass {
		t.Fatalf("initial owner is not break-glass: returned %v, stored %v", created.BreakGlass, stored.BreakGlass)
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
	promoted := testRoleAccount(t, r, models.RoleAdmin)
	if err := r.CheckOwnerTargetByID(t.Context(), other.ID, promoted.ID); !errors.Is(err, ErrAdminProtected) {
		t.Fatalf("an account targeting another admin: %v", err)
	}
	if err := r.CheckOwnerTargetByID(t.Context(), created.ID, promoted.ID); err != nil {
		t.Fatalf("owner targeting another admin: %v", err)
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

func TestOwnerBreakGlassDefaultMigrationPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	owner := testRoleAccount(t, r, models.RoleAdmin)
	admin := testRoleAccount(t, r, models.RoleAdmin)
	if _, err := r.pool.Exec(t.Context(), `UPDATE users SET is_owner = true, break_glass = false WHERE id = $1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20261002175237_owner_break_glass_default.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.pool.Exec(t.Context(), strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int]bool{owner.ID: true, admin.ID: false} {
		u, err := r.GetByID(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if u.BreakGlass != want {
			t.Errorf("account %d (owner %v): break-glass %v, want %v", id, u.IsOwner, u.BreakGlass, want)
		}
	}
}
