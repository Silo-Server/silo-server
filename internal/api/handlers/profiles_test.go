package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// profileLimitGroups serves one access group's policy for every account.
type profileLimitGroups struct{ policy access.GroupPolicy }

func (g profileLimitGroups) GetPolicyForUser(context.Context, int) (*access.GroupPolicy, error) {
	policy := g.policy
	return &policy, nil
}

func TestHandleCreateProfile_EnforcesInheritedGroupProfileLimit(t *testing.T) {
	groupID := int64(4)
	tests := []struct {
		name     string
		override *int
		want     int
	}{
		{name: "inherits the group's limit", want: http.StatusConflict},
		{name: "an override above the group's limit wins", override: new(2), want: http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newProfileTestStore(t)
			handler := NewProfileHandler(testUserStoreProvider{store: store})
			handler.UserRepo = testProfileUserRepo{
				user: &models.User{ID: 1, Role: models.RoleUser, AccessGroupID: &groupID, MaxProfiles: tt.override},
			}
			handler.AccessGroups = profileLimitGroups{policy: access.GroupPolicy{ID: groupID, MaxProfiles: 1}}

			req := newAuthorizedProfileRequestWithRole(http.MethodPost, "/profiles", `{"name":"Kids"}`, "user", "profile-1")
			rr := httptest.NewRecorder()
			handler.HandleCreateProfile(rr, req)

			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d, body = %s", rr.Code, tt.want, rr.Body.String())
			}
			if tt.want == http.StatusConflict && !strings.Contains(rr.Body.String(), "profile limit (1)") {
				t.Fatalf("body = %s, want the group's limit in the message", rr.Body.String())
			}
		})
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

func ptr[T any](value T) *T {
	return &value
}

func TestHandleUpdateProfile_AllowsSelfServiceFieldsForNonAdmin(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut,
		"/profiles/profile-1",
		`{"name":"Main","subtitle_mode":"always","pin":"1234"}`,
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

func TestHandleDeleteProfile_AllowsPrimaryToDeleteOther(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	purger := &recordingProfilePurger{}
	withdrawer := &recordingWatchlistRequestWithdrawer{}
	handler.WatchlistTitlesPurger = purger
	handler.WatchlistRequestWithdrawer = withdrawer

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
	if err != nil || profile != nil {
		t.Fatal("expected profile to be deleted")
	}
	if len(purger.calls) != 1 || purger.calls[0] != "1/profile-2" {
		t.Fatalf("purge calls = %v, want [1/profile-2]", purger.calls)
	}
	if len(withdrawer.calls) != 1 || withdrawer.calls[0] != "1/profile-2" {
		t.Fatalf("withdraw calls = %v, want [1/profile-2]", withdrawer.calls)
	}

}

type recordingProfilePurger struct {
	calls []string
}

func (p *recordingProfilePurger) PurgeProfile(_ context.Context, userID int, profileID string) error {
	p.calls = append(p.calls, fmt.Sprintf("%d/%s", userID, profileID))
	return nil
}

type recordingWatchlistRequestWithdrawer struct {
	calls []string
}

func (w *recordingWatchlistRequestWithdrawer) WithdrawProfileWatchlistRequests(_ context.Context, userID int, profileID string) error {
	w.calls = append(w.calls, fmt.Sprintf("%d/%s", userID, profileID))
	return nil
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
