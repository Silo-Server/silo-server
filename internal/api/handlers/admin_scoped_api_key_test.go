package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

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
}

func (r *scopedKeyUserRepo) List(context.Context) ([]*models.User, error) {
	return []*models.User{r.user}, nil
}

func (r *scopedKeyUserRepo) Create(_ context.Context, input models.CreateUserInput) (*models.User, error) {
	r.created = &input
	return r.user, nil
}

func (r *scopedKeyUserRepo) Update(_ context.Context, _ int, input models.UpdateUserInput) error {
	r.updated = &input
	return nil
}

func (r *scopedKeyUserRepo) Delete(context.Context, int) error { return nil }

func (r *scopedKeyUserRepo) GetByID(context.Context, int) (*models.User, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.user, nil
}

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
		MaxProfiles: 5,
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

func TestHandleCreateUserRejectsScopedAPIKeyEscalation(t *testing.T) {
	tests := []struct {
		name       string
		claims     *auth.Claims
		body       string
		wantStatus int
		wantCreate bool
	}{
		{
			name:       "scoped key cannot mint an admin",
			claims:     scopedKeyClaims(),
			body:       `{"username":"mallory","email":"m@example.com","password":"hunter2","role":"admin"}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "scoped key may provision an ordinary account",
			claims:     scopedKeyClaims(),
			body:       `{"username":"ada","email":"ada@example.com","password":"hunter2","role":"user"}`,
			wantStatus: http.StatusCreated,
			wantCreate: true,
		},
		{
			name:       "unscoped key keeps full access",
			claims:     unscopedKeyClaims(),
			body:       `{"username":"ada","email":"ada@example.com","password":"hunter2","role":"admin"}`,
			wantStatus: http.StatusCreated,
			wantCreate: true,
		},
		{
			name:       "jwt admin keeps full access",
			claims:     jwtAdminClaims(),
			body:       `{"username":"ada","email":"ada@example.com","password":"hunter2","role":"admin"}`,
			wantStatus: http.StatusCreated,
			wantCreate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := newScopedKeyAdminHandler("user")
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", strings.NewReader(tt.body))
			req = req.WithContext(apimw.SetClaims(req.Context(), tt.claims))
			rec := httptest.NewRecorder()

			h.HandleCreateUser(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusForbidden {
				if code := decodeErrorCode(t, rec); code != "insufficient_scope" {
					t.Fatalf("error code = %q, want insufficient_scope", code)
				}
			}
			if got := repo.created != nil; got != tt.wantCreate {
				t.Fatalf("user created = %v, want %v", got, tt.wantCreate)
			}
		})
	}
}

func TestHandleUpdateUserRejectsScopedAPIKeyEscalation(t *testing.T) {
	tests := []struct {
		name       string
		claims     *auth.Claims
		targetRole string
		body       string
		wantStatus int
		wantUpdate bool
	}{
		{
			name:       "scoped key cannot promote to admin",
			claims:     scopedKeyClaims(),
			targetRole: "user",
			body:       `{"role":"admin"}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "scoped key cannot take over an admin",
			claims:     scopedKeyClaims(),
			targetRole: "admin",
			body:       `{"password":"hunter2"}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "scoped key cannot demote an admin",
			claims:     scopedKeyClaims(),
			targetRole: "admin",
			body:       `{"role":"user"}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "scoped key may reset an ordinary password",
			claims:     scopedKeyClaims(),
			targetRole: "user",
			body:       `{"password":"hunter2"}`,
			wantStatus: http.StatusOK,
			wantUpdate: true,
		},
		{
			name:       "scoped key may still edit policy fields on an admin",
			claims:     scopedKeyClaims(),
			targetRole: "admin",
			body:       `{"max_streams":3}`,
			wantStatus: http.StatusOK,
			wantUpdate: true,
		},
		{
			name:       "unscoped key keeps full access",
			claims:     unscopedKeyClaims(),
			targetRole: "admin",
			body:       `{"password":"hunter2","role":"admin"}`,
			wantStatus: http.StatusOK,
			wantUpdate: true,
		},
		{
			name:       "jwt admin keeps full access",
			claims:     jwtAdminClaims(),
			targetRole: "admin",
			body:       `{"password":"hunter2","role":"admin"}`,
			wantStatus: http.StatusOK,
			wantUpdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := newScopedKeyAdminHandler(tt.targetRole)
			rec := updateUserRequestFor(t, h, tt.claims, tt.body)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusForbidden {
				if code := decodeErrorCode(t, rec); code != "insufficient_scope" {
					t.Fatalf("error code = %q, want insufficient_scope", code)
				}
			}
			if got := repo.updated != nil; got != tt.wantUpdate {
				t.Fatalf("user updated = %v, want %v", got, tt.wantUpdate)
			}
		})
	}
}

// Promoting passes the role through untouched; the repository owns clearing
// the group so every caller (invitations, provisioning) gets the same rule.
func TestHandleUpdateUserPromoteLeavesGroupToRepository(t *testing.T) {
	h, repo := newScopedKeyAdminHandler("user")
	groupID := int64(5)
	repo.user.AccessGroupID = &groupID

	rec := updateUserRequestFor(t, h, jwtAdminClaims(), `{"role":"admin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if repo.updated == nil || repo.updated.Role == nil || *repo.updated.Role != "admin" {
		t.Fatalf("update = %+v, want role admin", repo.updated)
	}
	if repo.updated.AccessGroupID.Set {
		t.Fatalf("AccessGroupID = %+v, want untouched (repository clears it)", repo.updated.AccessGroupID)
	}
}

// A group named alongside the admin role is rejected the same way whether the
// account is being promoted, is already an admin, or echoes its current role.
func TestHandleUpdateUserRejectsGroupingAnAdmin(t *testing.T) {
	tests := []struct {
		name string
		role string
		body string
	}{
		{name: "existing admin, group only", role: "admin", body: `{"access_group_id":5}`},
		{name: "existing admin echoing role", role: "admin", body: `{"role":"admin","access_group_id":5}`},
		{name: "promote with group", role: "user", body: `{"role":"admin","access_group_id":5}`},
		{name: "promote with unknown group", role: "user", body: `{"role":"admin","access_group_id":99999}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := newScopedKeyAdminHandler(tt.role)

			rec := updateUserRequestFor(t, h, jwtAdminClaims(), tt.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
			}
			if code := decodeErrorCode(t, rec); code != "unprocessable_entity" {
				t.Fatalf("error code = %q, want unprocessable_entity", code)
			}
			if repo.updated != nil {
				t.Fatal("grouped-admin assignment must not be written")
			}
		})
	}
}

// Demoting passes through as well; the repository lands the ex-admin on the
// default group unless the request names one.
func TestHandleUpdateUserDemoteLeavesGroupToRepository(t *testing.T) {
	h, repo := newScopedKeyAdminHandler("admin")

	rec := updateUserRequestFor(t, h, jwtAdminClaims(), `{"role":"user"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if repo.updated == nil || repo.updated.Role == nil || *repo.updated.Role != "user" {
		t.Fatalf("update = %+v, want role user", repo.updated)
	}
	if repo.updated.AccessGroupID.Set {
		t.Fatalf("AccessGroupID = %+v, want untouched (repository assigns the default group)", repo.updated.AccessGroupID)
	}
}

func TestHandleCreateUserRejectsGroupedAdmin(t *testing.T) {
	h, repo := newScopedKeyAdminHandler("user")
	body := `{"username":"mallory","email":"m@example.com","password":"hunter2","role":"admin","access_group_id":5}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), jwtAdminClaims()))
	rec := httptest.NewRecorder()

	h.HandleCreateUser(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if code := decodeErrorCode(t, rec); code != "unprocessable_entity" {
		t.Fatalf("error code = %q, want unprocessable_entity", code)
	}
	if repo.created != nil {
		t.Fatal("grouped admin must not be created")
	}
}

// The scoped-key guard loads the target account before validating, so a
// missing account has to surface as 404 rather than an escalation decision.
func TestHandleUpdateUserScopedAPIKeyMissingTarget(t *testing.T) {
	h, repo := newScopedKeyAdminHandler("user")
	repo.getErr = auth.ErrNotFound

	rec := updateUserRequestFor(t, h, scopedKeyClaims(), `{"password":"hunter2"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if repo.updated != nil {
		t.Fatal("a missing target must not be updated")
	}
}

func updateUserRequestFor(t *testing.T, h *AdminHandler, claims *auth.Claims, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/42", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	req = req.WithContext(apimw.SetClaims(ctx, claims))
	rec := httptest.NewRecorder()
	h.HandleUpdateUser(rec, req)
	return rec
}
