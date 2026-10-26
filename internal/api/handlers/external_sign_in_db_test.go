package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// identityGuardUsers answers the Owner rules' account reads from a map.
type identityGuardUsers struct {
	UserRepository
	users map[int]models.User
}

func (r identityGuardUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	user, ok := r.users[id]
	if !ok {
		return nil, auth.ErrNotFound
	}
	return &user, nil
}

type noLoginProviders struct{}

func (noLoginProviders) ListProviders() []auth.LoginProviderInfo { return nil }

// TestExternalSignInAdminIdentityGuardsDB: an administrator's identity link
// and unlink follow the Owner rules. Only the Owner changes the Owner's or
// another admin's sign-in, a scoped API key never changes an admin's, and an
// admin may link and unlink a plain user's identity.
func TestExternalSignInAdminIdentityGuardsDB(t *testing.T) {
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
	suffix := fmt.Sprint(time.Now().UnixNano())
	var installationID int
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/identity-guard-test', true, 'manual', 'plugin') RETURNING id`, "identity-guard-"+suffix).Scan(&installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'oidc', false)`, installationID); err != nil {
		t.Fatal(err)
	}
	account := func(label, role string) models.User {
		t.Helper()
		name := fmt.Sprintf("guard-%s-%s", label, suffix)
		user := models.User{Username: name, Email: name + "@example.invalid", Role: role, Enabled: true, LocalPasswordLoginEnabled: true}
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled) VALUES ($1, $2, 'x', $3, true) RETURNING id`,
			user.Username, user.Email, role).Scan(&user.ID); err != nil {
			t.Fatal(err)
		}
		return user
	}
	admin, user := account("admin", models.RoleAdmin), account("user", models.RoleUser)
	otherAdmin := account("other-admin", models.RoleAdmin)
	ownedIDs := []int{admin.ID, user.ID, otherAdmin.ID}
	owner := ownerAccount()
	err = pool.QueryRow(ctx, `SELECT id FROM users WHERE is_owner`).Scan(&owner.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		owner = account("owner", models.RoleAdmin)
		if _, err := pool.Exec(ctx, `UPDATE users SET is_owner = true WHERE id = $1`, owner.ID); err != nil {
			t.Fatal(err)
		}
		owner.IsOwner = true
		ownedIDs = append(ownedIDs, owner.ID)
	} else if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanup, `DELETE FROM plugin_installations WHERE id = $1`, installationID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = ANY($1)`, ownedIDs)
	})
	otherAdminID := otherAdmin.ID
	users := identityGuardUsers{users: map[int]models.User{
		owner.ID:     owner,
		otherAdminID: otherAdmin,
		admin.ID:     admin,
		user.ID:      user,
	}}
	h := NewExternalSignInHandler(auth.NewIdentityService(pool), noLoginProviders{}, users, nil)
	session := claimsCtx
	scopedKey := func(userID int) context.Context {
		return apimw.SetClaims(context.Background(), &auth.Claims{UserID: userID, Role: "admin", TokenType: auth.TokenTypeAPIKey, APIKeyScopes: []string{"admin"}})
	}
	identityCount := func(userID int) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE user_id = $1`, userID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	link := func(ctx context.Context, userID int) (ExternalIdentityView, error) {
		return h.LinkAdminUserIdentity(ctx, userID, AdminIdentityLinkInput{InstallationID: installationID, ExternalSubject: fmt.Sprintf("subject-%d-%s", userID, suffix)})
	}
	requireRefusal := func(name string, err error, code string) {
		t.Helper()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Code != code {
			t.Errorf("%s: err = %v, want 403 %s", name, err, code)
		}
	}

	// Refused links reach nothing.
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		target int
		code   string
	}{
		{"admin links the owner", session(otherAdminID), owner.ID, codeOwnerProtected},
		{"admin links another admin", session(otherAdminID), admin.ID, codeOwnerProtected},
		{"owner's scoped key links an admin", scopedKey(owner.ID), admin.ID, "insufficient_scope"},
	} {
		_, err := link(tc.ctx, tc.target)
		requireRefusal(tc.name, err, tc.code)
	}
	if n := identityCount(admin.ID); n != 0 {
		t.Fatalf("a refused link linked the admin: %d identities", n)
	}

	// The Owner links another admin; nobody else unlinks it.
	linked, err := link(session(owner.ID), admin.ID)
	if err != nil {
		t.Fatalf("owner links an admin: %v", err)
	}
	requireRefusal("admin unlinks another admin", h.UnlinkAdminUserIdentity(session(otherAdminID), admin.ID, linked.ID), codeOwnerProtected)
	requireRefusal("owner's scoped key unlinks an admin", h.UnlinkAdminUserIdentity(scopedKey(owner.ID), admin.ID, linked.ID), "insufficient_scope")
	requireRefusal("admin unlinks the owner", h.UnlinkAdminUserIdentity(session(otherAdminID), owner.ID, linked.ID), codeOwnerProtected)
	if n := identityCount(admin.ID); n != 1 {
		t.Fatalf("a refused unlink removed the identity: %d identities", n)
	}
	if err := h.UnlinkAdminUserIdentity(session(owner.ID), admin.ID, linked.ID); err != nil {
		t.Fatalf("owner unlinks an admin: %v", err)
	}

	// An admin manages a plain user's sign-in.
	linked, err = link(session(otherAdminID), user.ID)
	if err != nil {
		t.Fatalf("admin links a user: %v", err)
	}
	if err := h.UnlinkAdminUserIdentity(session(otherAdminID), user.ID, linked.ID); err != nil {
		t.Fatalf("admin unlinks a user: %v", err)
	}
	if n := identityCount(admin.ID) + identityCount(user.ID); n != 0 {
		t.Fatalf("identities left = %d", n)
	}
}

// identityMutationRaceUsers pauses after the handler's initial account
// read, so a committed account change precedes its mutation transaction.
type identityMutationRaceUsers struct {
	UserRepository
	pauseID int
	read    chan struct{}
	resume  chan struct{}
}

func (r *identityMutationRaceUsers) GetByID(ctx context.Context, id int) (*models.User, error) {
	user, err := r.UserRepository.GetByID(ctx, id)
	if err == nil && id == r.pauseID {
		close(r.read)
		select {
		case <-r.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return user, err
}

func TestExternalSignInAdminIdentityRechecksAccountsDB(t *testing.T) {
	for _, action := range []string{"link", "unlink"} {
		for _, tc := range []struct {
			name   string
			target bool
			scoped bool
			change models.UpdateUserInput
			code   string
		}{
			{"target promoted", true, false, models.UpdateUserInput{Role: new(models.RoleAdmin)}, codeOwnerProtected},
			{"scoped key target promoted", true, true, models.UpdateUserInput{Role: new(models.RoleAdmin)}, "insufficient_scope"},
			{"actor demoted", false, false, models.UpdateUserInput{Role: new(models.RoleUser)}, "permission_denied"},
			{"actor disabled", false, false, models.UpdateUserInput{Enabled: new(false)}, "permission_denied"},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				dsn := os.Getenv("SILO_TEST_DATABASE_URL")
				if dsn == "" {
					t.Skip("SILO_TEST_DATABASE_URL is not set")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				pool, err := pgxpool.New(ctx, dsn)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(pool.Close)
				suffix := fmt.Sprint(time.Now().UnixNano())
				var installationID int
				if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
					VALUES ($1, '0', '/nonexistent/identity-race-test', true, 'manual', 'plugin') RETURNING id`, "identity-race-"+suffix).Scan(&installationID); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'oidc', false)`, installationID); err != nil {
					t.Fatal(err)
				}
				account := func(label, role string) int {
					t.Helper()
					name := "identity-race-" + label + "-" + suffix
					var id int
					if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
						VALUES ($1, $2, 'x', $3, true) RETURNING id`, name, name+"@example.invalid", role).Scan(&id); err != nil {
						t.Fatal(err)
					}
					return id
				}
				actor, target := account("actor", models.RoleAdmin), account("target", models.RoleUser)
				t.Cleanup(func() {
					cleanup := context.WithoutCancel(ctx)
					_, _ = pool.Exec(cleanup, `DELETE FROM plugin_installations WHERE id = $1`, installationID)
					_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = ANY($1)`, []int{actor, target})
				})
				repo := auth.NewUserRepository(pool)
				identities := auth.NewIdentityService(pool)
				subject := "identity-race-subject-" + suffix
				var identityID int64
				if action == "unlink" {
					linked, err := identities.AdminLink(ctx, auth.AdminLinkInput{UserID: target, InstallationID: installationID,
						Identity: auth.ExternalIdentity{Subject: subject}})
					if err != nil {
						t.Fatal(err)
					}
					identityID = linked.ID
				}
				pauseID := actor
				if tc.target {
					pauseID = target
				}
				users := &identityMutationRaceUsers{UserRepository: repo, pauseID: pauseID, read: make(chan struct{}), resume: make(chan struct{})}
				h := NewExternalSignInHandler(identities, noLoginProviders{}, users, nil)
				claims := &auth.Claims{UserID: actor, Role: models.RoleAdmin, TokenType: auth.TokenTypeAccess, SessionID: "synthetic-session"}
				if tc.scoped {
					claims.TokenType, claims.APIKeyScopes = auth.TokenTypeAPIKey, []string{"admin"}
				}
				requestCtx := apimw.SetClaims(ctx, claims)
				result := make(chan error, 1)
				go func() {
					var err error
					if action == "link" {
						_, err = h.LinkAdminUserIdentity(requestCtx, target, AdminIdentityLinkInput{InstallationID: installationID, ExternalSubject: subject})
					} else {
						err = h.UnlinkAdminUserIdentity(requestCtx, target, identityID)
					}
					result <- err
				}()
				select {
				case <-users.read:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if err := repo.Update(ctx, pauseID, tc.change); err != nil {
					close(users.resume)
					t.Fatal(err)
				}
				close(users.resume)
				select {
				case err := <-result:
					apiErr, ok := errors.AsType[*APIError](err)
					if !ok || apiErr.Status != http.StatusForbidden || apiErr.Code != tc.code {
						t.Fatalf("identity mutation after account change = %v; want 403 %s", err, tc.code)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				var count int
				if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE user_id = $1`, target).Scan(&count); err != nil {
					t.Fatal(err)
				}
				want := 0
				if action == "unlink" {
					want = 1
				}
				if count != want {
					t.Fatalf("identity count = %d, want %d after refusal", count, want)
				}
			})
		}
	}
}
