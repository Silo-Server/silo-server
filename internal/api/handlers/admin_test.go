package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestUpdateRequiresSessionRevocation(t *testing.T) {
	role := "admin"
	sameRole := "user"
	enabled := true
	disabled := false
	libraryIDs := []int{1, 2}
	sameLibraryIDs := []int{1}
	emptyLibraryIDs := []int{}
	var allLibraryIDs []int
	maxPlaybackQuality := "1080p"
	sameMaxPlaybackQuality := "original"
	password := "new-password"
	username := "renamed"
	maxStreams := 4
	permissions := []string{"metadata_curation"}
	samePermissions := []string{"download"}

	current := &models.User{
		Role:               "user",
		Permissions:        []string{"download"},
		Enabled:            false,
		LibraryIDs:         []int{1},
		MaxPlaybackQuality: "original",
	}

	tests := []struct {
		name string
		in   models.UpdateUserInput
		want bool
	}{
		{
			name: "permissions set",
			in:   models.UpdateUserInput{Permissions: &permissions},
			want: true,
		},
		{
			name: "permissions unchanged",
			in:   models.UpdateUserInput{Permissions: &samePermissions},
			want: false,
		},
		{
			name: "role",
			in:   models.UpdateUserInput{Role: &role},
			want: true,
		},
		{
			name: "role unchanged",
			in:   models.UpdateUserInput{Role: &sameRole},
			want: false,
		},
		{
			name: "enabled",
			in:   models.UpdateUserInput{Enabled: &enabled},
			want: true,
		},
		{
			name: "enabled unchanged",
			in:   models.UpdateUserInput{Enabled: &disabled},
			want: false,
		},
		{
			name: "library ids does not revoke session",
			in:   models.UpdateUserInput{LibraryIDs: &libraryIDs},
			want: false,
		},
		{
			name: "library ids unchanged",
			in:   models.UpdateUserInput{LibraryIDs: &sameLibraryIDs},
			want: false,
		},
		{
			name: "library ids nil does not revoke session",
			in:   models.UpdateUserInput{LibraryIDs: &allLibraryIDs},
			want: false,
		},
		{
			name: "max playback quality",
			in:   models.UpdateUserInput{MaxPlaybackQuality: &maxPlaybackQuality},
			want: true,
		},
		{
			name: "max playback quality unchanged",
			in:   models.UpdateUserInput{MaxPlaybackQuality: &sameMaxPlaybackQuality},
			want: false,
		},
		{
			name: "password",
			in:   models.UpdateUserInput{Password: &password},
			want: true,
		},
		{
			name: "non access fields",
			in:   models.UpdateUserInput{Username: &username, MaxStreams: &maxStreams},
			want: false,
		},
		{
			name: "empty update",
			in:   models.UpdateUserInput{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateRequiresSessionRevocation(current, tt.in); got != tt.want {
				t.Fatalf("updateRequiresSessionRevocation() = %v, want %v", got, tt.want)
			}
		})
	}

	unrestrictedCurrent := *current
	unrestrictedCurrent.LibraryIDs = nil
	t.Run("library ids empty does not revoke session", func(t *testing.T) {
		if got := updateRequiresSessionRevocation(&unrestrictedCurrent, models.UpdateUserInput{LibraryIDs: &emptyLibraryIDs}); got {
			t.Fatalf("updateRequiresSessionRevocation() = %v, want false", got)
		}
	})

	t.Run("library ids nil unchanged", func(t *testing.T) {
		if got := updateRequiresSessionRevocation(&unrestrictedCurrent, models.UpdateUserInput{LibraryIDs: &allLibraryIDs}); got {
			t.Fatalf("updateRequiresSessionRevocation() = %v, want false", got)
		}
	})
}

type fakeAdminUpdateUserRepo struct {
	createCalled bool
	updateCalled bool
	deleteCalled bool
	user         *models.User
}

func (f *fakeAdminUpdateUserRepo) List(context.Context) ([]*models.User, error) {
	return nil, nil
}

func (f *fakeAdminUpdateUserRepo) Create(_ context.Context, input models.CreateUserInput) (*models.User, error) {
	f.createCalled = true
	return &models.User{ID: 99, Username: input.Username, Email: input.Email, Role: input.Role, Enabled: true}, nil
}

func (f *fakeAdminUpdateUserRepo) Update(context.Context, int, models.UpdateUserInput) error {
	f.updateCalled = true
	return nil
}

func (f *fakeAdminUpdateUserRepo) Delete(context.Context, int) error {
	f.deleteCalled = true
	return nil
}

func (f *fakeAdminUpdateUserRepo) GetByID(context.Context, int) (*models.User, error) {
	if f.user != nil {
		return f.user, nil
	}
	return &models.User{ID: 7, Username: "tom", Role: "user", Enabled: true}, nil
}

type fakeHandlerAdminAuthorizer struct {
	decision auth.AccessDecision
	request  auth.AccessRequest
	called   bool
}

func (f *fakeHandlerAdminAuthorizer) Authorize(_ context.Context, request auth.AccessRequest) (auth.AccessDecision, error) {
	f.called = true
	f.request = request
	return f.decision, nil
}

func (f *fakeHandlerAdminAuthorizer) Explain(_ context.Context, request auth.AccessRequest) (auth.AccessExplanation, error) {
	decision, err := f.Authorize(context.Background(), request)
	return auth.AccessExplanation{Request: request, Decision: decision}, err
}

func TestHandleUpdateUserRejectsAccessGroupChangesWithoutSecurityManage(t *testing.T) {
	userRepo := &fakeAdminUpdateUserRepo{}
	authorizer := &fakeHandlerAdminAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := &AdminHandler{
		userRepo:        userRepo,
		AdminAuthorizer: authorizer,
	}

	req := httptest.NewRequest(http.MethodPut, "/admin/users/7", strings.NewReader(`{
		"username": "tom",
		"access_group_slugs": ["admin"]
	}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "7")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 42, Role: "user", TokenType: auth.TokenTypeAccess})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleUpdateUser(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if userRepo.updateCalled {
		t.Fatalf("user repo update was called")
	}
	if !authorizer.called {
		t.Fatalf("admin authorizer was not called")
	}
	if authorizer.request.Action != auth.ActionSecurityManage {
		t.Fatalf("authorizer action = %q, want %q", authorizer.request.Action, auth.ActionSecurityManage)
	}
}

func TestHandleCreateUserRejectsAdminRoleWithoutSecurityManage(t *testing.T) {
	userRepo := &fakeAdminUpdateUserRepo{}
	authorizer := &fakeHandlerAdminAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := &AdminHandler{
		userRepo:        userRepo,
		AdminAuthorizer: authorizer,
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(`{
		"username": "martha",
		"email": "martha@example.test",
		"password": "secret-password",
		"role": "admin"
	}`))
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 42, Role: "user", TokenType: auth.TokenTypeAccess})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleCreateUser(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if userRepo.createCalled {
		t.Fatalf("user repo create was called")
	}
	if !authorizer.called {
		t.Fatalf("admin authorizer was not called")
	}
	if authorizer.request.Action != auth.ActionSecurityManage {
		t.Fatalf("authorizer action = %q, want %q", authorizer.request.Action, auth.ActionSecurityManage)
	}
}

func TestHandleCreateUserRejectsAccessPolicyWithoutSecurityManage(t *testing.T) {
	userRepo := &fakeAdminUpdateUserRepo{}
	authorizer := &fakeHandlerAdminAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := &AdminHandler{
		userRepo:        userRepo,
		AdminAuthorizer: authorizer,
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(`{
		"username": "tom",
		"email": "tom@example.test",
		"password": "secret-password",
		"role": "user",
		"max_streams": 10
	}`))
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 42, Role: "user", TokenType: auth.TokenTypeAccess})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleCreateUser(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if userRepo.createCalled {
		t.Fatalf("user repo create was called")
	}
	if !authorizer.called {
		t.Fatalf("admin authorizer was not called")
	}
	if authorizer.request.Action != auth.ActionSecurityManage {
		t.Fatalf("authorizer action = %q, want %q", authorizer.request.Action, auth.ActionSecurityManage)
	}
}

func TestHandleUpdateUserRejectsAdminPromotionWithoutSecurityManage(t *testing.T) {
	userRepo := &fakeAdminUpdateUserRepo{}
	authorizer := &fakeHandlerAdminAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := &AdminHandler{
		userRepo:        userRepo,
		AdminAuthorizer: authorizer,
	}

	req := httptest.NewRequest(http.MethodPut, "/admin/users/7", strings.NewReader(`{
		"role": "admin"
	}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "7")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 42, Role: "user", TokenType: auth.TokenTypeAccess})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleUpdateUser(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if userRepo.updateCalled {
		t.Fatalf("user repo update was called")
	}
	if !authorizer.called {
		t.Fatalf("admin authorizer was not called")
	}
	if authorizer.request.Action != auth.ActionSecurityManage {
		t.Fatalf("authorizer action = %q, want %q", authorizer.request.Action, auth.ActionSecurityManage)
	}
}

func TestHandleUpdateUserRejectsAccessPolicyWithoutSecurityManage(t *testing.T) {
	userRepo := &fakeAdminUpdateUserRepo{}
	authorizer := &fakeHandlerAdminAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := &AdminHandler{
		userRepo:        userRepo,
		AdminAuthorizer: authorizer,
	}

	req := httptest.NewRequest(http.MethodPut, "/admin/users/7", strings.NewReader(`{
		"permissions": ["metadata_curation"]
	}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "7")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 42, Role: "user", TokenType: auth.TokenTypeAccess})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleUpdateUser(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if userRepo.updateCalled {
		t.Fatalf("user repo update was called")
	}
	if !authorizer.called {
		t.Fatalf("admin authorizer was not called")
	}
	if authorizer.request.Action != auth.ActionSecurityManage {
		t.Fatalf("authorizer action = %q, want %q", authorizer.request.Action, auth.ActionSecurityManage)
	}
}

func TestHandleUpdateUserRejectsAdminAccountChangesWithoutSecurityManage(t *testing.T) {
	userRepo := &fakeAdminUpdateUserRepo{
		user: &models.User{ID: 7, Username: "martha", Role: "admin", Enabled: true},
	}
	authorizer := &fakeHandlerAdminAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := &AdminHandler{
		userRepo:        userRepo,
		AdminAuthorizer: authorizer,
	}

	req := httptest.NewRequest(http.MethodPut, "/admin/users/7", strings.NewReader(`{
		"username": "martha-renamed"
	}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "7")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 42, Role: "user", TokenType: auth.TokenTypeAccess})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleUpdateUser(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if userRepo.updateCalled {
		t.Fatalf("user repo update was called")
	}
	if !authorizer.called {
		t.Fatalf("admin authorizer was not called")
	}
	if authorizer.request.Action != auth.ActionSecurityManage {
		t.Fatalf("authorizer action = %q, want %q", authorizer.request.Action, auth.ActionSecurityManage)
	}
}

func TestHandleDeleteUserRejectsAdminAccountWithoutSecurityManage(t *testing.T) {
	userRepo := &fakeAdminUpdateUserRepo{
		user: &models.User{ID: 7, Username: "martha", Role: "admin", Enabled: true},
	}
	authorizer := &fakeHandlerAdminAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := &AdminHandler{
		userRepo:        userRepo,
		AdminAuthorizer: authorizer,
	}

	req := httptest.NewRequest(http.MethodDelete, "/admin/users/7", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "7")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 42, Role: "user", TokenType: auth.TokenTypeAccess})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleDeleteUser(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if userRepo.deleteCalled {
		t.Fatalf("user repo delete was called")
	}
	if !authorizer.called {
		t.Fatalf("admin authorizer was not called")
	}
	if authorizer.request.Action != auth.ActionSecurityManage {
		t.Fatalf("authorizer action = %q, want %q", authorizer.request.Action, auth.ActionSecurityManage)
	}
}
