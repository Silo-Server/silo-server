package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/go-chi/chi/v5"
)

func TestPolicyActingAdminMiddlewareParity(t *testing.T) {
	pdp := newMiddlewarePolicyPDP(t)
	checkErr := errors.New("profile store down")

	tests := []struct {
		name      string
		claims    *auth.Claims
		profileID string
		check     PrimaryProfileChecker
	}{
		{name: "missing_claims"},
		{name: "non_admin", claims: &auth.Claims{UserID: 7, Role: "user", TokenType: auth.TokenTypeAccess}},
		{name: "admin_without_profile", claims: &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAccess}, check: primaryChecker(false, true, nil)},
		{name: "admin_primary_profile", claims: &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAccess}, profileID: "prof-1", check: primaryChecker(true, true, nil)},
		{name: "admin_non_primary_profile", claims: &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAccess}, profileID: "prof-2", check: primaryChecker(false, true, nil)},
		{name: "admin_unknown_profile", claims: &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAccess}, profileID: "prof-x", check: primaryChecker(false, false, nil)},
		{name: "checker_error", claims: &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAccess}, profileID: "prof-1", check: primaryChecker(false, false, checkErr)},
		{name: "nil_checker_allows_declared_profile", claims: &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAccess}, profileID: "prof-2"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			legacy := captureActingAdminResponse(RequireActingAdmin(test.check), test.claims, test.profileID)
			policyBacked := captureActingAdminResponse(NewPolicyActingAdminMiddleware(pdp, test.check), test.claims, test.profileID)
			assertMiddlewareResponsesEqual(t, policyBacked, legacy)
		})
	}
}

func TestPolicyMetadataCurationMiddlewareParity(t *testing.T) {
	pdp := newMiddlewarePolicyPDP(t)
	resolverErr := errors.New("resolver down")
	userErr := errors.New("user store down")

	curator := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: []int{1, 2, 3}, Permissions: []string{policy.PermissionMetadataCuration}}
	unrestrictedCurator := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: nil, Permissions: []string{policy.PermissionMetadataCuration}}
	noPermission := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: []int{1}, Permissions: nil}
	disabledCurator := &models.User{ID: 7, Role: "user", Enabled: false, LibraryIDs: nil, Permissions: []string{policy.PermissionMetadataCuration}}
	nonPrimaryAdmin := &models.User{ID: 7, Role: "admin", Enabled: true, LibraryIDs: nil, Permissions: nil}
	assignedNonPrimaryAdmin := &models.User{ID: 7, Role: "admin", Enabled: true, LibraryIDs: nil, Permissions: []string{policy.PermissionMetadataCuration}}

	tests := []struct {
		name      string
		claims    *auth.Claims
		user      *models.User
		userErr   error
		targetIDs []int
		targetErr error
		profileID string
		check     PrimaryProfileChecker
		itemID    string
	}{
		{name: "missing_claims", itemID: "item-1"},
		{name: "acting_admin_primary_bypasses_missing_repos", claims: adminClaims(), profileID: "prof-1", check: primaryChecker(true, true, nil), itemID: "item-1"},
		{name: "acting_admin_no_profile_bypasses", claims: adminClaims(), itemID: "item-1"},
		{name: "non_primary_admin_without_assigned_permission", claims: adminClaims(), user: nonPrimaryAdmin, targetIDs: []int{1}, profileID: "prof-2", check: primaryChecker(false, true, nil), itemID: "item-1"},
		{name: "non_primary_admin_with_assigned_permission", claims: adminClaims(), user: assignedNonPrimaryAdmin, targetIDs: []int{1}, profileID: "prof-2", check: primaryChecker(false, true, nil), itemID: "item-1"},
		{name: "non_primary_admin_with_assigned_permission_out_of_scope", claims: adminClaims(), user: &models.User{ID: 7, Role: "admin", Enabled: true, LibraryIDs: []int{1}, Permissions: []string{policy.PermissionMetadataCuration}}, targetIDs: []int{2}, profileID: "prof-2", check: primaryChecker(false, true, nil), itemID: "item-1"},
		{name: "user_without_permission", claims: userClaims(), user: noPermission, targetIDs: []int{1}, itemID: "item-1"},
		{name: "unrestricted_curator", claims: userClaims(), user: unrestrictedCurator, targetIDs: []int{8, 9}, itemID: "item-1"},
		{name: "curator_in_scope", claims: userClaims(), user: curator, targetIDs: []int{1, 3}, itemID: "item-1"},
		{name: "curator_out_of_scope", claims: userClaims(), user: curator, targetIDs: []int{1, 4}, itemID: "item-1"},
		{name: "target_resolver_error", claims: userClaims(), user: curator, targetErr: resolverErr, itemID: "item-1"},
		{name: "target_not_found", claims: userClaims(), user: unrestrictedCurator, targetIDs: nil, itemID: "item-1"},
		{name: "missing_item_id", claims: userClaims(), user: curator},
		{name: "user_loader_error", claims: userClaims(), userErr: userErr, itemID: "item-1"},
		{name: "user_not_found", claims: userClaims(), user: nil, itemID: "item-1"},
		{name: "user_disabled", claims: userClaims(), user: disabledCurator, itemID: "item-1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			legacy := captureMetadataCurationResponse(
				NewPermissionMiddleware(
					fakePermissionUserLoader{user: test.user, err: test.userErr},
					fakeTargetLibraryResolver{ids: test.targetIDs, err: test.targetErr},
					test.check,
				),
				test.claims,
				test.profileID,
				test.itemID,
			)
			policyBacked := captureMetadataCurationResponse(
				NewPolicyPermissionMiddleware(
					fakePermissionUserLoader{user: test.user, err: test.userErr},
					fakeTargetLibraryResolver{ids: test.targetIDs, err: test.targetErr},
					test.check,
					pdp,
				),
				test.claims,
				test.profileID,
				test.itemID,
			)
			assertMiddlewareResponsesEqual(t, policyBacked, legacy)
		})
	}
}

func TestPolicyActingAdminMiddlewareEvalErrorIsInternal(t *testing.T) {
	rec := captureActingAdminResponse(
		NewPolicyActingAdminMiddleware(errorPermissionDecider{}, nil),
		adminClaims(),
		"",
	)
	if rec.code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body %s", rec.code, http.StatusInternalServerError, rec.body)
	}
}

func TestPolicyMetadataCurationMiddlewareAppliesGroupPermissionMask(t *testing.T) {
	groupID := int64(3)
	user := &models.User{
		ID:            7,
		Role:          "user",
		Enabled:       true,
		AccessGroupID: &groupID,
		LibraryIDs:    []int{1},
		Permissions:   []string{policy.PermissionMetadataCuration},
	}
	rec := captureMetadataCurationResponse(
		NewPolicyPermissionMiddleware(
			fakePermissionUserLoader{user: user},
			fakeTargetLibraryResolver{ids: []int{1}},
			nil,
			newMiddlewarePolicyPDP(t),
			middlewareGroupProvider{group: &access.GroupPolicy{
				AllowedPermissions:       []string{policy.PermissionMarkerEdit},
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				RequestsAllowed:          true,
			}},
		),
		userClaims(),
		"",
		"item-1",
	)
	if rec.code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s, want forbidden", rec.code, rec.body)
	}
}

func TestPolicyMarkerEditMiddlewareParity(t *testing.T) {
	pdp := newMiddlewarePolicyPDP(t)
	userErr := errors.New("user store down")

	editor := &models.User{ID: 7, Role: "user", Enabled: true, Permissions: []string{policy.PermissionMarkerEdit}}
	noPermission := &models.User{ID: 7, Role: "user", Enabled: true, Permissions: nil}
	disabledEditor := &models.User{ID: 7, Role: "user", Enabled: false, Permissions: []string{policy.PermissionMarkerEdit}}
	enabledAdmin := &models.User{ID: 7, Role: "admin", Enabled: true, Permissions: nil}

	tests := []struct {
		name    string
		claims  *auth.Claims
		user    *models.User
		userErr error
	}{
		{name: "missing_claims"},
		{name: "admin", claims: adminClaims(), user: enabledAdmin},
		{name: "user_with_permission", claims: userClaims(), user: editor},
		{name: "user_without_permission", claims: userClaims(), user: noPermission},
		{name: "user_disabled", claims: userClaims(), user: disabledEditor},
		{name: "user_loader_error", claims: userClaims(), userErr: userErr},
		{name: "user_not_found", claims: userClaims(), user: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			legacy := captureMarkerEditResponse(
				NewPermissionMiddleware(
					fakePermissionUserLoader{user: test.user, err: test.userErr},
					nil,
					nil,
				),
				test.claims,
			)
			policyBacked := captureMarkerEditResponse(
				NewPolicyPermissionMiddleware(
					fakePermissionUserLoader{user: test.user, err: test.userErr},
					nil,
					nil,
					pdp,
				),
				test.claims,
			)
			assertMiddlewareResponsesEqual(t, policyBacked, legacy)
		})
	}
}

func TestPolicyMarkerEditMiddlewareAppliesGroupPermissionMask(t *testing.T) {
	groupID := int64(3)
	user := &models.User{
		ID:            7,
		Role:          "user",
		Enabled:       true,
		AccessGroupID: &groupID,
		Permissions:   []string{policy.PermissionMarkerEdit},
	}
	rec := captureMarkerEditResponse(
		NewPolicyPermissionMiddleware(
			fakePermissionUserLoader{user: user},
			nil,
			nil,
			newMiddlewarePolicyPDP(t),
			middlewareGroupProvider{group: &access.GroupPolicy{
				AllowedPermissions:       []string{policy.PermissionMetadataCuration},
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				RequestsAllowed:          true,
			}},
		),
		userClaims(),
	)
	if rec.code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s, want forbidden", rec.code, rec.body)
	}
}

func TestPolicyMarkerEditMiddlewareEvalErrorIsInternal(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, Permissions: []string{policy.PermissionMarkerEdit}}
	rec := captureMarkerEditResponse(
		NewPolicyPermissionMiddleware(
			fakePermissionUserLoader{user: user},
			nil,
			nil,
			errorPermissionDecider{},
		),
		userClaims(),
	)
	if rec.code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body %s", rec.code, http.StatusInternalServerError, rec.body)
	}
}

func TestPolicySubtitleUploadMiddlewareParity(t *testing.T) {
	pdp := newMiddlewarePolicyPDP(t)
	userErr := errors.New("user store down")

	uploader := &models.User{ID: 7, Role: "user", Enabled: true, Permissions: []string{policy.PermissionSubtitleUpload}}
	markerOnly := &models.User{ID: 7, Role: "user", Enabled: true, Permissions: []string{policy.PermissionMarkerEdit}}
	disabledUploader := &models.User{ID: 7, Role: "user", Enabled: false, Permissions: []string{policy.PermissionSubtitleUpload}}
	admin := &models.User{ID: 7, Role: "admin", Enabled: true}
	disabledAdmin := &models.User{ID: 7, Role: "admin", Enabled: false}
	assignedAdmin := &models.User{ID: 7, Role: "admin", Enabled: true, Permissions: []string{policy.PermissionSubtitleUpload}}

	tests := []struct {
		name      string
		claims    *auth.Claims
		user      *models.User
		userErr   error
		profileID string
		check     PrimaryProfileChecker
		wantCode  int
	}{
		{name: "missing_claims", wantCode: http.StatusUnauthorized},
		{name: "acting_admin_no_profile", claims: adminClaims(), user: admin, wantCode: http.StatusNoContent},
		{name: "disabled_admin", claims: adminClaims(), user: disabledAdmin, wantCode: http.StatusForbidden},
		{name: "acting_admin_primary_profile", claims: adminClaims(), user: admin, profileID: "prof-1", check: primaryChecker(true, true, nil), wantCode: http.StatusNoContent},
		{name: "non_primary_admin_without_assigned_permission", claims: adminClaims(), user: admin, profileID: "prof-2", check: primaryChecker(false, true, nil), wantCode: http.StatusForbidden},
		{name: "non_primary_admin_with_assigned_permission", claims: adminClaims(), user: assignedAdmin, profileID: "prof-2", check: primaryChecker(false, true, nil), wantCode: http.StatusNoContent},
		{name: "primary_check_error", claims: adminClaims(), user: admin, profileID: "prof-1", check: primaryChecker(false, false, errors.New("profiles down")), wantCode: http.StatusInternalServerError},
		{name: "user_with_permission", claims: userClaims(), user: uploader, wantCode: http.StatusNoContent},
		{name: "user_without_permission", claims: userClaims(), user: markerOnly, wantCode: http.StatusForbidden},
		{name: "user_disabled", claims: userClaims(), user: disabledUploader, wantCode: http.StatusForbidden},
		{name: "user_loader_error", claims: userClaims(), userErr: userErr, wantCode: http.StatusForbidden},
		{name: "user_not_found", claims: userClaims(), wantCode: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			legacy := captureSubtitleUploadResponse(
				NewPermissionMiddleware(fakePermissionUserLoader{user: test.user, err: test.userErr}, nil, test.check),
				test.claims,
				test.profileID,
			)
			policyBacked := captureSubtitleUploadResponse(
				NewPolicyPermissionMiddleware(fakePermissionUserLoader{user: test.user, err: test.userErr}, nil, test.check, pdp),
				test.claims,
				test.profileID,
			)
			assertMiddlewareResponsesEqual(t, policyBacked, legacy)
			if policyBacked.code != test.wantCode {
				t.Fatalf("status = %d body = %s, want %d", policyBacked.code, policyBacked.body, test.wantCode)
			}
		})
	}
}

func TestSubtitleUploadMiddlewareAppliesGroupPermissionMask(t *testing.T) {
	groupID := int64(3)
	user := &models.User{
		ID:            7,
		Role:          "user",
		Enabled:       true,
		AccessGroupID: &groupID,
		Permissions:   []string{policy.PermissionSubtitleUpload},
	}
	groups := middlewareGroupProvider{group: &access.GroupPolicy{
		AllowedPermissions:       []string{policy.PermissionMarkerEdit},
		DownloadAllowed:          true,
		DownloadTranscodeAllowed: true,
		RequestsAllowed:          true,
	}}
	for name, gate := range map[string]subtitleUploadGate{
		"legacy": NewPermissionMiddleware(fakePermissionUserLoader{user: user}, nil, nil, groups),
		"policy": NewPolicyPermissionMiddleware(fakePermissionUserLoader{user: user}, nil, nil, newMiddlewarePolicyPDP(t), groups),
	} {
		t.Run(name, func(t *testing.T) {
			rec := captureSubtitleUploadResponse(gate, userClaims(), "")
			if rec.code != http.StatusForbidden {
				t.Fatalf("status = %d body = %s, want forbidden", rec.code, rec.body)
			}
		})
	}
}

func TestPolicySubtitleUploadMiddlewareEvalErrorIsInternal(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, Permissions: []string{policy.PermissionSubtitleUpload}}
	rec := captureSubtitleUploadResponse(
		NewPolicyPermissionMiddleware(fakePermissionUserLoader{user: user}, nil, nil, errorPermissionDecider{}),
		userClaims(),
		"",
	)
	if rec.code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body %s", rec.code, http.StatusInternalServerError, rec.body)
	}
}

func newMiddlewarePolicyPDP(t *testing.T) *policy.PDP {
	t.Helper()
	engine, err := policy.NewEngine(context.Background())
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	return policy.NewPDP(engine)
}

type middlewareResponse struct {
	code int
	body string
}

func captureActingAdminResponse(
	mw func(http.Handler) http.Handler,
	claims *auth.Claims,
	profileID string,
) middlewareResponse {
	next := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/admin/sessions", nil)
	if profileID != "" {
		req.Header.Set("X-Profile-Id", profileID)
	}
	if claims != nil {
		req = req.WithContext(SetClaims(req.Context(), claims))
	}
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)
	return middlewareResponse{code: rec.Code, body: rec.Body.String()}
}

type metadataCurationGate interface {
	RequireMetadataCurationForItem(http.Handler) http.Handler
}

func captureMetadataCurationResponse(
	mw metadataCurationGate,
	claims *auth.Claims,
	profileID string,
	itemID string,
) middlewareResponse {
	next := mw.RequireMetadataCurationForItem(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/admin/items/"+itemID+"/refresh-metadata", nil)
	if profileID != "" {
		req.Header.Set("X-Profile-Id", profileID)
	}
	ctx := req.Context()
	if claims != nil {
		ctx = SetClaims(ctx, claims)
	}
	routeCtx := chi.NewRouteContext()
	if itemID != "" {
		routeCtx.URLParams.Add("id", itemID)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req.WithContext(ctx))
	return middlewareResponse{code: rec.Code, body: rec.Body.String()}
}

type markerEditGate interface {
	RequireMarkerEdit(http.Handler) http.Handler
}

func captureMarkerEditResponse(mw markerEditGate, claims *auth.Claims) middlewareResponse {
	next := mw.RequireMarkerEdit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPut, "/markers/files/5", nil)
	if claims != nil {
		req = req.WithContext(SetClaims(req.Context(), claims))
	}
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)
	return middlewareResponse{code: rec.Code, body: rec.Body.String()}
}

type subtitleUploadGate interface {
	RequireSubtitleUpload(http.Handler) http.Handler
}

func captureSubtitleUploadResponse(mw subtitleUploadGate, claims *auth.Claims, profileID string) middlewareResponse {
	next := mw.RequireSubtitleUpload(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/subtitles/upload", nil)
	if profileID != "" {
		req.Header.Set("X-Profile-Id", profileID)
	}
	if claims != nil {
		req = req.WithContext(SetClaims(req.Context(), claims))
	}
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)
	return middlewareResponse{code: rec.Code, body: rec.Body.String()}
}

func assertMiddlewareResponsesEqual(t *testing.T, got, want middlewareResponse) {
	t.Helper()
	if got != want {
		t.Fatalf("policy response = %#v, want legacy %#v", got, want)
	}
}

func adminClaims() *auth.Claims {
	return &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAccess}
}

func userClaims() *auth.Claims {
	return &auth.Claims{UserID: 7, Role: "user", TokenType: auth.TokenTypeAccess}
}

type errorPermissionDecider struct{}

func (errorPermissionDecider) CheckPermission(context.Context, policy.PermissionInput) (policy.PermissionDecision, policy.Meta, error) {
	return policy.PermissionDecision{}, policy.Meta{}, errors.New("policy unavailable")
}

type middlewareGroupProvider struct {
	group *access.GroupPolicy
	err   error
}

func (p middlewareGroupProvider) GetPolicyForUser(context.Context, int) (*access.GroupPolicy, error) {
	return p.group, p.err
}
