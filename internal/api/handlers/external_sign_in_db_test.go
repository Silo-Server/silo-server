package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

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
	account := func(role string) models.User {
		t.Helper()
		name := fmt.Sprintf("guard-%s-%s", role, suffix)
		user := models.User{Username: name, Email: name + "@example.invalid", Role: role, Enabled: true, LocalPasswordLoginEnabled: true}
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled) VALUES ($1, $2, 'x', $3, true) RETURNING id`,
			user.Username, user.Email, role).Scan(&user.ID); err != nil {
			t.Fatal(err)
		}
		return user
	}
	admin, user := account(models.RoleAdmin), account(models.RoleUser)
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanup, `DELETE FROM plugin_installations WHERE id = $1`, installationID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = ANY($1)`, []int{admin.ID, user.ID})
	})
	const otherAdminID = 7
	owner := ownerAccount()
	users := identityGuardUsers{users: map[int]models.User{
		owner.ID:     owner,
		otherAdminID: {ID: otherAdminID, Role: models.RoleAdmin, Enabled: true},
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
