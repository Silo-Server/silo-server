package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
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

// TestCreateProfileWithSettingsSyncRecountsLimit pins the in-transaction
// re-count on the SQLite store: a household that filled up after the
// handler's early check is refused inside the creating transaction, and
// nothing is written. The Postgres store is pinned by
// TestCreateProfileLimitConcurrentDB.
func TestCreateProfileWithSettingsSyncRecountsLimit(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	err := handler.createProfileWithSettingsSync(context.Background(), store, 1,
		userstore.Profile{ID: "profile-2", Name: "Late"}, 1, nil)
	if !errors.Is(err, errProfileLimitReached) {
		t.Fatalf("err = %v, want errProfileLimitReached", err)
	}
	profiles, err := store.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profile count = %d, want 1", len(profiles))
	}
}

// profileLimitGroups serves one access group's policy for every account.
type profileLimitGroups struct{ policy access.GroupPolicy }

func (g profileLimitGroups) GetPolicyForUser(context.Context, int) (*access.GroupPolicy, error) {
	policy := g.policy
	return &policy, nil
}

func ptr[T any](value T) *T {
	return &value
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

// householdWithKids is the seeded store (profile-1, "Main", the primary) plus
// profile-2, "Kids".
func householdWithKids(t *testing.T) userstore.UserStore {
	t.Helper()
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return store
}

func TestCreateProfile_EnforcesInheritedGroupProfileLimit(t *testing.T) {
	groupID := int64(4)
	for _, tc := range []struct {
		name     string
		override *int
		limited  bool
	}{
		{name: "inherits the group's limit", limited: true},
		{name: "an override above the group's limit wins", override: new(2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewProfileHandler(testUserStoreProvider{store: newProfileTestStore(t)})
			handler.UserRepo = testProfileUserRepo{
				user: &models.User{ID: 1, Role: models.RoleUser, AccessGroupID: &groupID, MaxProfiles: tc.override},
			}
			handler.AccessGroups = profileLimitGroups{policy: access.GroupPolicy{ID: groupID, MaxProfiles: 1}}

			_, err := handler.CreateProfile(profileServiceContext("profile-1"), ProfileCreateCommand{
				UserID: 1, ActiveProfileID: "profile-1", Request: ProfileCreateRequest{Name: "Kids"},
				VerifyProfile: noProfileVerification,
			})
			if !tc.limited {
				if err != nil {
					t.Fatalf("create under the override: %v", err)
				}
				return
			}
			if apiErr := requireAPIStatus(t, err, http.StatusConflict); apiErr.Code != "profile_limit_reached" ||
				!strings.Contains(apiErr.Message, "profile limit (1)") {
				t.Fatalf("error = %+v, want the group's limit", apiErr)
			}
		})
	}
}

// A trimmed, case-insensitive name match within the account is a conflict on
// create and on rename, and nothing is written.
func TestProfileNameConflicts(t *testing.T) {
	store := householdWithKids(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	ctx := profileServiceContext("profile-1")

	_, err := handler.CreateProfile(ctx, ProfileCreateCommand{
		UserID: 1, ActiveProfileID: "profile-1", Request: ProfileCreateRequest{Name: " main "},
		VerifyProfile: noProfileVerification,
	})
	if apiErr := requireAPIStatus(t, err, http.StatusConflict); apiErr.Code != "name_conflict" {
		t.Fatalf("create error = %+v, want name_conflict", apiErr)
	}
	_, err = handler.UpdateProfile(ctx, ProfileUpdateCommand{
		UserID: 1, ProfileID: "profile-2", ActiveProfileID: "profile-1",
		Request: ProfileUpdateRequest{Name: new("MAIN")}, VerifyProfile: noProfileVerification,
	})
	if apiErr := requireAPIStatus(t, err, http.StatusConflict); apiErr.Code != "name_conflict" {
		t.Fatalf("rename error = %+v, want name_conflict", apiErr)
	}

	profiles, err := store.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("profile count = %d, want 2", len(profiles))
	}
	if kids, _ := store.GetProfile(context.Background(), "profile-2"); kids == nil || kids.Name != "Kids" {
		t.Fatalf("profile renamed despite the conflict: %+v", kids)
	}
}

// A household member edits its own playback preferences and PIN, but not
// another profile.
func TestUpdateProfile_SelfServiceForNonManager(t *testing.T) {
	store := householdWithKids(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	update := func(profileID string) error {
		_, err := handler.UpdateProfile(profileServiceContext("profile-2"), ProfileUpdateCommand{
			UserID: 1, ProfileID: profileID, ActiveProfileID: "profile-2",
			Request:       ProfileUpdateRequest{Name: new("Robin"), SubtitleMode: new("always"), PIN: new("1234")},
			VerifyProfile: noProfileVerification,
		})
		return err
	}

	if err := update("profile-2"); err != nil {
		t.Fatalf("self-service update: %v", err)
	}
	profile, err := store.GetProfile(context.Background(), "profile-2")
	if err != nil || profile == nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile.Name != "Robin" || profile.SubtitleMode != "always" || profile.PINHash == "" {
		t.Fatalf("profile = %+v, want the self-service fields applied", profile)
	}

	requireAPIStatus(t, update("profile-1"), http.StatusForbidden)
	if primary, _ := store.GetProfile(context.Background(), "profile-1"); primary == nil || primary.PINHash != "" || primary.Name != "Main" {
		t.Fatalf("a member changed the primary profile: %+v", primary)
	}
}

func TestDeleteProfile_PrimaryDeletesOthersButNotItself(t *testing.T) {
	store := householdWithKids(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	purger := &recordingProfilePurger{}
	withdrawer := &recordingWatchlistRequestWithdrawer{}
	handler.WatchlistTitlesPurger = purger
	handler.WatchlistRequestWithdrawer = withdrawer
	remove := func(profileID string) error {
		return handler.DeleteProfile(profileServiceContext("profile-1"), ProfileDeleteCommand{
			UserID: 1, ProfileID: profileID, ActiveProfileID: "profile-1", VerifyProfile: noProfileVerification,
		})
	}

	if apiErr := requireAPIStatus(t, remove("profile-1"), http.StatusConflict); apiErr.Code != "primary_profile_protected" {
		t.Fatalf("deleting the primary = %+v, want primary_profile_protected", apiErr)
	}
	if primary, err := store.GetProfile(context.Background(), "profile-1"); err != nil || primary == nil {
		t.Fatalf("primary profile not preserved: %v", err)
	}

	if err := remove("profile-2"); err != nil {
		t.Fatalf("delete profile-2: %v", err)
	}
	if kids, err := store.GetProfile(context.Background(), "profile-2"); err != nil || kids != nil {
		t.Fatal("expected profile-2 to be deleted")
	}
	if len(purger.calls) != 1 || purger.calls[0] != "1/profile-2" {
		t.Fatalf("purge calls = %v, want [1/profile-2]", purger.calls)
	}
	if len(withdrawer.calls) != 1 || withdrawer.calls[0] != "1/profile-2" {
		t.Fatalf("withdraw calls = %v, want [1/profile-2]", withdrawer.calls)
	}
}

func TestListHouseholdSessions_PrimaryOnly(t *testing.T) {
	handler := NewProfileHandler(testUserStoreProvider{store: householdWithKids(t)})
	handler.SessionsReader = &stubPlaybackSessionsLoader{
		userID: 1,
		sessions: []playbackSessionRow{{
			SessionID: "sess-1", UserID: 1, ProfileID: "profile-2", ProfileName: "Kids",
			MediaTitle: "Example Movie", PlayMethod: "direct",
		}},
	}
	list := func(active string) ([]PlaybackSessionView, error) {
		return handler.ListHouseholdSessions(profileServiceContext(active), HouseholdSessionsQuery{
			UserID: 1, ActiveProfileID: active, VerifyProfile: noProfileVerification,
		})
	}

	_, err := list("profile-2")
	requireAPIStatus(t, err, http.StatusForbidden)

	sessions, err := list("profile-1")
	if err != nil {
		t.Fatalf("primary list: %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "sess-1" || sessions[0].ProfileName != "Kids" {
		t.Fatalf("sessions = %#v", sessions)
	}
}
