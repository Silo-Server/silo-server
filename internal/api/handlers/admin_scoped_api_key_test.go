package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// scopedKeyUserRepo records the writes an admin-user handler attempts so a
// blocked request can be distinguished from one that merely failed later.
type scopedKeyUserRepo struct {
	user    *models.User
	getErr  error
	created *models.CreateUserInput
	updated *models.UpdateUserInput
	deleted bool
	// promoteBeforeMutate makes the account an admin between the handler's
	// pre-checks and the locked write, as a concurrent promotion would.
	promoteBeforeMutate bool
	// updateErr, when set, is what Update returns.
	updateErr error
	// revoked records whether the last mutation asked to sign the account out.
	revoked bool
}

func (r *scopedKeyUserRepo) List(context.Context) ([]*models.User, error) {
	return []*models.User{r.user}, nil
}

func (r *scopedKeyUserRepo) ListPage(context.Context, int, int, string) ([]*models.User, error) {
	return []*models.User{r.user}, nil
}

func (r *scopedKeyUserRepo) Create(_ context.Context, input models.CreateUserInput) (*models.User, error) {
	r.created = &input
	return r.user, nil
}

func (r *scopedKeyUserRepo) Update(_ context.Context, _ int, input models.UpdateUserInput) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	r.updated = &input
	return nil
}

func (r *scopedKeyUserRepo) Delete(context.Context, int) error { return nil }

// MutateAdminAccount runs the handler's checks against the stored account the
// way the transactional repository does, without a transaction.
func (r *scopedKeyUserRepo) MutateAdminAccount(ctx context.Context, id int, _ int64, input *models.UpdateUserInput, validate func(*models.User, pgx.Tx) (bool, error)) (auth.AdminUserSnapshot, error) {
	current, err := r.GetByID(ctx, id)
	if err != nil {
		return auth.AdminUserSnapshot{}, err
	}
	if r.promoteBeforeMutate {
		current.Role = models.RoleAdmin
	}
	revoke, err := validate(current, nil)
	if err != nil {
		return auth.AdminUserSnapshot{User: current}, err
	}
	r.revoked = revoke
	if input == nil {
		r.deleted = true
		return auth.AdminUserSnapshot{User: current}, nil
	}
	return auth.AdminUserSnapshot{User: current}, r.Update(ctx, id, *input)
}

func (r *scopedKeyUserRepo) GetAdminSnapshot(ctx context.Context, id int) (auth.AdminUserSnapshot, error) {
	current, err := r.GetByID(ctx, id)
	return auth.AdminUserSnapshot{User: current}, err
}

func (r *scopedKeyUserRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.user != nil && id == r.user.ID {
		return r.user, nil
	}
	// Caller 1 is the server Owner, so the tests that use it exercise the
	// scoped-key rules rather than the Owner rules.
	if id == scopedKeyTestOwnerID {
		return &models.User{ID: id, Role: models.RoleAdmin, Enabled: true, IsOwner: true}, nil
	}
	return nil, auth.ErrNotFound
}

const scopedKeyTestOwnerID = 1

// newScopedKeyAdminHandler builds an AdminHandler whose target account has the
// given role, so the "target is already an admin" rule can be exercised.
func newScopedKeyAdminHandler(targetRole string) (*AdminHandler, *scopedKeyUserRepo) {
	repo := &scopedKeyUserRepo{user: &models.User{
		ID:          42,
		Username:    "ada",
		Email:       "ada@example.com",
		Role:        targetRole,
		Permissions: []string{},
		Enabled:     true,
		MaxProfiles: new(5),
	}}
	return &AdminHandler{
		userRepo:           repo,
		accountProvisioner: auth.NewAccountProvisioner(repo, nil),
	}, repo
}

// scopedKeyClaims is what RequireAuth builds for an API key carrying scopes.
func scopedKeyClaims() *auth.Claims {
	return &auth.Claims{
		UserID:       1,
		Role:         "admin",
		TokenType:    auth.TokenTypeAPIKey,
		APIKeyID:     9,
		APIKeyScopes: []string{auth.ScopeAdminUsers},
	}
}

func unscopedKeyClaims() *auth.Claims {
	return &auth.Claims{
		UserID:    1,
		Role:      "admin",
		TokenType: auth.TokenTypeAPIKey,
		APIKeyID:  9,
	}
}

func jwtAdminClaims() *auth.Claims {
	return &auth.Claims{UserID: 1, Role: "admin", TokenType: auth.TokenTypeAccess, SessionID: "s1"}
}

func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

// A scoped admin-users API key manages ordinary accounts but cannot mint an
// admin.
func TestCreateAdminAccountRejectsScopedAPIKeyEscalation(t *testing.T) {
	// The refusal comes before any database work; the pool never connects.
	pool, err := pgxpool.New(t.Context(), "postgres://silo:silo@127.0.0.1:1/silo?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	h, repo := newScopedKeyAdminHandler(models.RoleUser)
	h.pool = pool

	_, err = h.CreateAdminAccount(apimw.SetClaims(t.Context(), scopedKeyClaims()), auth.CreateAccountInput{User: models.CreateUserInput{
		Username: "mallory", Email: "m@example.com", Password: "a-new-long-password", Role: models.RoleAdmin,
	}})
	requireAdminError(t, err, http.StatusForbidden, "insufficient_scope")
	if repo.created != nil {
		t.Fatal("scoped key created an admin")
	}
}

// A scoped key cannot grant admin or change an admin's credentials, recovery
// email, or role; it can still manage ordinary accounts and admin policy
// fields. Unscoped keys and signed-in admins keep full access. The rules run
// against the account as the locked write reads it.
func TestUpdateAdminAccountRejectsScopedAPIKeyEscalation(t *testing.T) {
	password := "a-new-long-password"
	attacker := "attacker@example.test"
	ordinaryEmail := "new@example.test"
	admin, user := models.RoleAdmin, models.RoleUser
	for _, tt := range []struct {
		name       string
		claims     *auth.Claims
		targetRole string
		self       bool
		promote    bool
		input      models.UpdateUserInput
		allowed    bool
	}{
		{name: "scoped key cannot promote to admin", claims: scopedKeyClaims(), targetRole: user, input: models.UpdateUserInput{Role: &admin}},
		{name: "scoped key cannot take over an admin", claims: scopedKeyClaims(), targetRole: admin, input: models.UpdateUserInput{Password: &password}},
		{name: "scoped key cannot demote an admin", claims: scopedKeyClaims(), targetRole: admin, input: models.UpdateUserInput{Role: &user}},
		{name: "scoped key cannot redirect its admin owner's recovery email", claims: scopedKeyClaims(), targetRole: admin, self: true, input: models.UpdateUserInput{Email: &attacker}},
		{name: "scoped key cannot redirect another admin's recovery email", claims: scopedKeyClaims(), targetRole: admin, input: models.UpdateUserInput{Email: &attacker}},
		{name: "scoped key rechecks a password write against the locked account", claims: scopedKeyClaims(), targetRole: user, promote: true, input: models.UpdateUserInput{Password: &password}},
		{name: "scoped key rechecks an email write against the locked account", claims: scopedKeyClaims(), targetRole: user, promote: true, input: models.UpdateUserInput{Email: &attacker}},
		{name: "scoped key may edit an ordinary recovery email", claims: scopedKeyClaims(), targetRole: user, input: models.UpdateUserInput{Email: &ordinaryEmail}, allowed: true},
		{name: "scoped key may reset an ordinary password", claims: scopedKeyClaims(), targetRole: user, input: models.UpdateUserInput{Password: &password}, allowed: true},
		{name: "scoped key may still edit policy fields on an admin", claims: scopedKeyClaims(), targetRole: admin, input: models.UpdateUserInput{MaxStreams: models.SetValue(3)}, allowed: true},
		{name: "unscoped key keeps full access", claims: unscopedKeyClaims(), targetRole: admin, input: models.UpdateUserInput{Password: &password, Role: &admin, Email: &ordinaryEmail}, allowed: true},
		{name: "jwt admin keeps full access", claims: jwtAdminClaims(), targetRole: admin, input: models.UpdateUserInput{Password: &password, Role: &admin, Email: &ordinaryEmail}, allowed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := newScopedKeyAdminHandler(tt.targetRole)
			repo.promoteBeforeMutate = tt.promote
			if tt.self {
				tt.claims.UserID = repo.user.ID
			}
			_, err := h.UpdateAdminAccount(apimw.SetClaims(t.Context(), tt.claims), repo.user.ID, -1, 0, tt.input)
			if tt.allowed {
				if err != nil || repo.updated == nil {
					t.Fatalf("err = %v, updated = %v; want the update written", err, repo.updated != nil)
				}
				return
			}
			requireAdminError(t, err, http.StatusForbidden, "insufficient_scope")
			if repo.updated != nil {
				t.Fatal("refused update was written")
			}
		})
	}
}

// An admin never belongs to an access group, whether it is being promoted,
// already is one, or echoes its current role.
func TestUpdateAdminAccountRejectsGroupingAnAdmin(t *testing.T) {
	admin := models.RoleAdmin
	for _, tt := range []struct {
		name  string
		role  string
		input models.UpdateUserInput
	}{
		{name: "existing admin, group only", role: admin, input: models.UpdateUserInput{AccessGroupID: models.SetValue[int64](5)}},
		{name: "existing admin echoing role", role: admin, input: models.UpdateUserInput{Role: &admin, AccessGroupID: models.SetValue[int64](5)}},
		{name: "promote with group", role: models.RoleUser, input: models.UpdateUserInput{Role: &admin, AccessGroupID: models.SetValue[int64](5)}},
		{name: "promote with unknown group", role: models.RoleUser, input: models.UpdateUserInput{Role: &admin, AccessGroupID: models.SetValue[int64](99999)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := newScopedKeyAdminHandler(tt.role)
			_, err := h.UpdateAdminAccount(apimw.SetClaims(t.Context(), jwtAdminClaims()), repo.user.ID, -1, 0, tt.input)
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Field != "access_group_id" {
				t.Fatalf("err = %v, want an access_group_id field error", err)
			}
			if repo.updated != nil {
				t.Fatal("grouped-admin assignment must not be written")
			}
		})
	}
}

// The scoped-key guard loads the target account first, so a missing account
// is not found rather than an escalation decision.
func TestUpdateAdminAccountScopedAPIKeyMissingTarget(t *testing.T) {
	h, repo := newScopedKeyAdminHandler(models.RoleUser)
	repo.getErr = auth.ErrNotFound
	password := "a-new-long-password"
	_, err := h.UpdateAdminAccount(apimw.SetClaims(t.Context(), scopedKeyClaims()), repo.user.ID, -1, 0, models.UpdateUserInput{Password: &password})
	if !auth.IsNotFound(err) || repo.updated != nil {
		t.Fatalf("err = %v, updated = %v; want not found and nothing written", err, repo.updated != nil)
	}
}
