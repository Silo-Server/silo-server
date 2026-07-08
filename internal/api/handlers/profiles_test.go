package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type testProfileUserRepo struct {
	user *models.User
	err  error
}

func (r testProfileUserRepo) GetByID(context.Context, int) (*models.User, error) {
	return r.user, r.err
}

func newAuthorizedProfileRequest(body string) *http.Request {
	return newAuthorizedProfileRequestWithRole(http.MethodPost, "/profiles", body, "user", "")
}

func newAuthorizedProfileRequestWithRole(
	method, path, body, role, activeProfileID string,
) *http.Request {
	return newAuthorizedProfileRequestWithSession(method, path, body, role, activeProfileID, "")
}

func newAuthorizedProfileRequestWithSession(
	method, path, body, role, activeProfileID, sessionID string,
) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{
		UserID:    1,
		Role:      role,
		SessionID: sessionID,
		TokenType: auth.TokenTypeAccess,
	})
	if activeProfileID != "" {
		ctx = apimw.SetProfileID(ctx, activeProfileID)
	}
	return req.WithContext(ctx)
}

func withProfileRouteParam(req *http.Request, key, value string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func newProfileTestStore(t *testing.T) userstore.UserStore {
	return newProfileTestStoreWithSeed(t, true)
}

func newEmptyProfileTestStore(t *testing.T) userstore.UserStore {
	return newProfileTestStoreWithSeed(t, false)
}

func newProfileTestStoreWithSeed(t *testing.T, seedProfile bool) userstore.UserStore {
	t.Helper()

	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	if seedProfile {
		if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
			t.Fatalf("create profile: %v", err)
		}
	}

	return store
}

func TestHandleCreateProfile_EnforcesUserProfileLimit(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{
		user: &models.User{ID: 1, MaxProfiles: 1},
	}

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/profiles",
		`{"name":"Kids","max_playback_quality":"1080p"}`,
		"admin",
		"",
	)
	rr := httptest.NewRecorder()

	handler.HandleCreateProfile(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "profile_limit_reached" {
		t.Fatalf("error = %q, want %q", resp.Error, "profile_limit_reached")
	}

	profiles, err := store.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profile count = %d, want 1", len(profiles))
	}
}

func TestHandleCreateProfile_AllowsNonAdminUpToCap(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{
		user: &models.User{ID: 1, MaxProfiles: 5},
	}

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/profiles",
		`{"name":"Kids"}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleCreateProfile(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profiles, err := store.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("profile count = %d, want 2", len(profiles))
	}
}

func TestHandleCreateProfile_RejectsDuplicateName(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	// The seeded store already holds "Main"; a trimmed, case-insensitive
	// match must be rejected within the same account.
	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/profiles",
		`{"name":" main "}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleCreateProfile(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "name_conflict" {
		t.Fatalf("error = %q, want %q", resp.Error, "name_conflict")
	}

	profiles, err := store.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profile count = %d, want 1", len(profiles))
	}
}

func TestHandleCreateProfile_RejectsWhitespaceOnlyName(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/profiles",
		`{"name":"   "}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleCreateProfile(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profiles, err := store.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profile count = %d, want 1", len(profiles))
	}
}

func TestHandleCreateProfile_TrimsStoredName(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/profiles",
		`{"name":" Laura "}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleCreateProfile(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profiles, err := store.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	for _, p := range profiles {
		if p.Name == "Laura" {
			return
		}
	}
	t.Fatalf("no profile stored with trimmed name, profiles: %+v", profiles)
}

func TestHandleCreateProfile_BlocksNonPrimaryNonAdmin(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{
		user: &models.User{ID: 1, MaxProfiles: 5},
	}

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/profiles",
		`{"name":"Extra"}`,
		"user",
		"profile-2",
	)
	rr := httptest.NewRecorder()

	handler.HandleCreateProfile(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleCreateProfile_RequiresVerifiedPrimaryPINWhenPrimaryHasPIN(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.UpdateProfile(context.Background(), "profile-1", userstore.UpdateProfileInput{
		PIN: ptr("1234"),
	}); err != nil {
		t.Fatalf("set primary pin: %v", err)
	}

	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{
		user: &models.User{ID: 1, MaxProfiles: 5, AccessPolicyRevision: 7},
	}
	handler.ProfileTokens = access.NewProfileTokenService("test-secret", time.Minute)

	req := newAuthorizedProfileRequestWithSession(
		http.MethodPost,
		"/profiles",
		`{"name":"Kids"}`,
		"user",
		"profile-1",
		"sess-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleCreateProfile(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "verifying the primary profile PIN") {
		t.Fatalf("body = %s", rr.Body.String())
	}
}

func TestHandleCreateProfile_AllowsVerifiedPrimaryPIN(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.UpdateProfile(context.Background(), "profile-1", userstore.UpdateProfileInput{
		PIN: ptr("1234"),
	}); err != nil {
		t.Fatalf("set primary pin: %v", err)
	}

	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{
		user: &models.User{ID: 1, MaxProfiles: 5, AccessPolicyRevision: 7},
	}
	handler.ProfileTokens = access.NewProfileTokenService("test-secret", time.Minute)

	token, _, err := handler.ProfileTokens.Mint(access.ProfileTokenClaims{
		UserID:         1,
		SessionID:      "sess-1",
		ProfileID:      "profile-1",
		PolicyRevision: 7,
	})
	if err != nil {
		t.Fatalf("mint profile token: %v", err)
	}

	req := newAuthorizedProfileRequestWithSession(
		http.MethodPost,
		"/profiles",
		`{"name":"Kids"}`,
		"user",
		"profile-1",
		"sess-1",
	)
	req.Header.Set("X-Profile-Token", token)
	rr := httptest.NewRecorder()

	handler.HandleCreateProfile(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleCreateProfile_BlocksAdminOnlyFieldsForNonAdmin(t *testing.T) {
	store := newEmptyProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{
		user: &models.User{ID: 1, MaxProfiles: 5},
	}

	req := newAuthorizedProfileRequest(`{"name":"Kids","is_child":true}`)
	rr := httptest.NewRecorder()

	handler.HandleCreateProfile(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profiles, err := store.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("profile count = %d, want 0", len(profiles))
	}
}

func TestHandleCreateProfile_AllowsFirstProfileBootstrapForNonAdmin(t *testing.T) {
	store := newEmptyProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequest(`{"name":"Main"}`)
	rr := httptest.NewRecorder()

	handler.HandleCreateProfile(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profiles, err := store.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profile count = %d, want 1", len(profiles))
	}
}

func ptr[T any](value T) *T {
	return &value
}

func TestHandleUpdateProfile_AllowsPrimaryToSetAccessFields(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut,
		"/profiles/profile-2",
		`{"max_content_rating":"PG-13"}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-2"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profile, err := store.GetProfile(context.Background(), "profile-2")
	if err != nil || profile == nil {
		t.Fatalf("get profile: %v, %v", profile, err)
	}
	if profile.MaxContentRating != "PG-13" {
		t.Fatalf("max_content_rating = %q, want PG-13", profile.MaxContentRating)
	}
}

func TestHandleUpdateProfile_BlocksAccessFieldsForNonPrimary(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut,
		"/profiles/profile-2",
		`{"max_content_rating":"PG-13"}`,
		"user",
		"profile-2",
	)
	rr := httptest.NewRecorder()

	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-2"))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profile, err := store.GetProfile(context.Background(), "profile-2")
	if err != nil || profile == nil {
		t.Fatalf("get profile: %v, %v", profile, err)
	}
	if profile.MaxContentRating != "" {
		t.Fatalf("max_content_rating = %q, want empty", profile.MaxContentRating)
	}
}

func TestHandleUpdateProfile_AllowsSelfServiceFieldsForNonAdmin(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut,
		"/profiles/profile-1",
		`{"subtitle_mode":"always","pin":"1234"}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-1"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profile, err := store.GetProfile(context.Background(), "profile-1")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile == nil {
		t.Fatal("profile missing")
	}
	if profile.SubtitleMode != "always" {
		t.Fatalf("subtitle_mode = %q, want %q", profile.SubtitleMode, "always")
	}
	if profile.PINHash == "" {
		t.Fatal("expected pin hash to be set")
	}
}

func TestHandleUpdateProfile_AllowsNonAdminToUpdateAnyOwnedProfile(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut,
		"/profiles/profile-2",
		`{"subtitle_mode":"always"}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-2"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleUpdateProfile_RejectsDuplicateNameOnRename(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut,
		"/profiles/profile-2",
		`{"name":"MAIN"}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-2"))

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "name_conflict" {
		t.Fatalf("error = %q, want %q", resp.Error, "name_conflict")
	}

	profile, err := store.GetProfile(context.Background(), "profile-2")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile == nil || profile.Name != "Kids" {
		t.Fatalf("profile name changed despite conflict: %+v", profile)
	}
}

func TestHandleUpdateProfile_AllowsKeepingOwnNameOnUpdate(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	// Saving a profile without renaming resubmits its own name; the
	// conflict check must exclude the profile being updated.
	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut,
		"/profiles/profile-1",
		`{"name":"Main","subtitle_mode":"always"}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-1"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleUpdateProfile_RejectsWhitespaceOnlyName(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut,
		"/profiles/profile-1",
		`{"name":"   "}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-1"))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profile, err := store.GetProfile(context.Background(), "profile-1")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile == nil || profile.Name != "Main" {
		t.Fatalf("profile name changed despite rejection: %+v", profile)
	}
}

func TestHandleUpdateProfile_TrimsStoredNameOnRename(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut,
		"/profiles/profile-1",
		`{"name":" Laura "}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-1"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profile, err := store.GetProfile(context.Background(), "profile-1")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile == nil || profile.Name != "Laura" {
		t.Fatalf("stored name not trimmed: %+v", profile)
	}
}

func TestHandleDeleteProfile_AllowsPrimaryToDeleteOther(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodDelete,
		"/profiles/profile-2",
		"",
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleDeleteProfile(rr, withProfileRouteParam(req, "id", "profile-2"))

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profile, err := store.GetProfile(context.Background(), "profile-2")
	if err == nil && profile != nil {
		t.Fatal("expected profile to be deleted")
	}
}

func TestHandleDeleteProfile_BlocksPrimaryDeletion(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodDelete,
		"/profiles/profile-1",
		"",
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleDeleteProfile(rr, withProfileRouteParam(req, "id", "profile-1"))

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	profile, err := store.GetProfile(context.Background(), "profile-1")
	if err != nil || profile == nil {
		t.Fatalf("expected primary profile to be preserved: %v", err)
	}
}

type stubPlaybackSessionsLoader struct {
	sessions []playbackSessionRow
	userID   int
	err      error
}

func (s *stubPlaybackSessionsLoader) Load(
	_ context.Context,
	_ *http.Request,
	query PlaybackSessionsQuery,
) ([]playbackSessionRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	if query.UserID != s.userID {
		return []playbackSessionRow{}, nil
	}
	return s.sessions, nil
}

func TestHandleListHouseholdSessions_ForbiddenForNonPrimary(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.SessionsReader = &stubPlaybackSessionsLoader{userID: 1}

	req := newAuthorizedProfileRequestWithRole(
		http.MethodGet,
		"/profiles/household/sessions",
		"",
		"user",
		"profile-2",
	)
	rr := httptest.NewRecorder()

	handler.HandleListHouseholdSessions(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleListHouseholdSessions_ReturnsAccountSessionsForPrimary(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.SessionsReader = &stubPlaybackSessionsLoader{
		userID: 1,
		sessions: []playbackSessionRow{
			{
				SessionID:   "sess-1",
				UserID:      1,
				ProfileID:   "profile-2",
				ProfileName: "Kids",
				MediaTitle:  "Example Movie",
				PlayMethod:  "direct",
			},
		},
	}

	req := newAuthorizedProfileRequestWithRole(
		http.MethodGet,
		"/profiles/household/sessions",
		"",
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleListHouseholdSessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp []playbackSessionRow
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) != 1 || resp[0].SessionID != "sess-1" || resp[0].ProfileName != "Kids" {
		t.Fatalf("sessions = %#v", resp)
	}
}
