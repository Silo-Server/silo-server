package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestProfileScopeChanged(t *testing.T) {
	current := &userstore.Profile{MaxContentRating: "PG", MaxAdvisoryAge: 10, LibraryRestrictionsEnabled: true, AllowedLibraryIDs: []int{2, 5}}
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	flag := func(v bool) *bool { return &v }
	ids := func(v ...int) *[]int { return &v }
	for _, tc := range []struct {
		name string
		req  ProfileUpdateRequest
		want bool
	}{
		{"name only", ProfileUpdateRequest{Name: str("Kid")}, false},
		{"same values sent back", ProfileUpdateRequest{MaxContentRating: str("PG"), MaxAdvisoryAge: num(10), LibraryRestrictionsEnabled: flag(true), AllowedLibraryIDs: ids(5, 2)}, false},
		{"rating ceiling", ProfileUpdateRequest{MaxContentRating: str("PG-13")}, true},
		{"advisory age", ProfileUpdateRequest{MaxAdvisoryAge: num(0)}, true},
		{"require advisory age", ProfileUpdateRequest{RequireAdvisoryAge: flag(true)}, true},
		{"restrictions off", ProfileUpdateRequest{LibraryRestrictionsEnabled: flag(false)}, true},
		{"allowed libraries", ProfileUpdateRequest{AllowedLibraryIDs: ids(2)}, true},
	} {
		if got := profileScopeChanged(current, tc.req); got != tc.want {
			t.Errorf("%s: changed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAccountScopeChanged(t *testing.T) {
	role := models.RoleUser
	email := "a@example.com"
	if accountScopeChanged(models.UpdateUserInput{Email: &email}) {
		t.Fatal("an email change does not change access scope")
	}
	for _, input := range []models.UpdateUserInput{
		{LibraryIDs: models.SetValue([]int{1})},
		{AccessGroupID: models.Optional[int64]{Set: true}},
		{Role: &role},
	} {
		if !accountScopeChanged(input) {
			t.Fatalf("input %+v changes access scope", input)
		}
	}
}

type recordingScopeNotifier struct {
	profiles []string
	accounts [][]int
}

func (n *recordingScopeNotifier) NotifySignalsChanged(_ context.Context, _ int, profileID string) {
	n.profiles = append(n.profiles, profileID)
}

func (n *recordingScopeNotifier) NotifyAccountsScopeChanged(_ context.Context, userIDs []int) {
	n.accounts = append(n.accounts, userIDs)
}

func TestHiddenLibrariesSettingRebuildsTheProfile(t *testing.T) {
	n := &recordingScopeNotifier{}
	notifyScopeSettingChanged(t.Context(), n, 7, "p1", settingskeys.UiDisabledLibraryIds)
	notifyScopeSettingChanged(t.Context(), n, 7, "p1", settingskeys.UiLibraryOrder)
	if !slices.Equal(n.profiles, []string{"p1"}) {
		t.Fatalf("rebuilt %v, want p1 once, for the hidden-libraries key only", n.profiles)
	}
}

// fakeGroupStore is an access group store whose group 3 has members 11 and
// 12 and libraries [1 2].
type fakeGroupStore struct {
	AccessGroupStore
	deleted bool
}

func (f *fakeGroupStore) Get(context.Context, int64) (*access.Group, error) {
	return &access.Group{ID: 3, LibraryIDs: []int{1, 2}}, nil
}

func (f *fakeGroupStore) ListPage(context.Context, *access.GroupPageKey, int) ([]access.Group, bool, error) {
	return nil, false, nil
}

func (f *fakeGroupStore) UpdateConditional(_ context.Context, id int64, _ access.UpdateGroupInput, _ access.GroupPrecondition) (*access.Group, error) {
	return &access.Group{ID: id}, nil
}

func (f *fakeGroupStore) DeleteConditional(context.Context, int64, access.GroupPrecondition) error {
	return nil
}

func (f *fakeGroupStore) DeleteMovingMembers(context.Context, int64, access.GroupPrecondition) error {
	f.deleted = true
	return nil
}

func (f *fakeGroupStore) MemberIDs(context.Context, int64) ([]int, error) {
	if f.deleted {
		return nil, nil // the delete moved them out
	}
	return []int{11, 12}, nil
}

// An update that sets an access group's libraries, or its deletion, rebuilds
// its members' recommendations; other group edits do not. A save of the same
// libraries rebuilds too: the stored set could only be compared outside the
// update's writer lock, where an overlapping update can change it.
func TestAccessGroupLibraryChangesRebuildMembers(t *testing.T) {
	ctx := t.Context()
	guard := access.GroupPrecondition{Any: true}
	libs := func(v ...int) *[]int { return &v }
	name := "Renamed"

	for _, tc := range []struct {
		name  string
		input access.UpdateGroupInput
		want  int
	}{
		{"name only", access.UpdateGroupInput{Name: &name}, 0},
		{"same libraries", access.UpdateGroupInput{LibraryIDs: libs(2, 1)}, 1},
		{"new libraries", access.UpdateGroupInput{LibraryIDs: libs(1)}, 1},
	} {
		n := &recordingScopeNotifier{}
		h := NewAccessGroupHandler(&fakeGroupStore{})
		h.RecWorker = n
		if _, err := h.UpdateAdminAccessGroup(ctx, 3, tc.input, guard); err != nil {
			t.Fatal(err)
		}
		if len(n.accounts) != tc.want {
			t.Fatalf("%s: notified %v, want %d notices", tc.name, n.accounts, tc.want)
		}
		if tc.want == 1 && !slices.Equal(n.accounts[0], []int{11, 12}) {
			t.Fatalf("%s: notified %v, want the members [11 12]", tc.name, n.accounts[0])
		}
	}

	n := &recordingScopeNotifier{}
	h := NewAccessGroupHandler(&fakeGroupStore{})
	h.RecWorker = n
	if err := h.DeleteAdminAccessGroup(ctx, 3, guard); err != nil {
		t.Fatal(err)
	}
	if len(n.accounts) != 1 || !slices.Equal(n.accounts[0], []int{11, 12}) {
		t.Fatalf("delete notified %v, want the members listed before the delete", n.accounts)
	}
}

// A profile update that changes the profile's restrictions rebuilds its
// recommendations; one that sends the same values back does not.
func TestProfileRestrictionChangeRebuildsRecommendations(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "kid", Name: "Kid", MaxContentRating: "PG"}); err != nil {
		t.Fatal(err)
	}
	n := &recordingScopeNotifier{}
	h := NewProfileHandler(testUserStoreProvider{store: store})
	h.RecWorker = n
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{UserID: 1, Role: "admin", TokenType: auth.TokenTypeAccess})
	update := func(rating string) {
		t.Helper()
		if _, err := h.UpdateProfile(ctx, ProfileUpdateCommand{UserID: 1, ProfileID: "kid", Request: ProfileUpdateRequest{MaxContentRating: &rating}}); err != nil {
			t.Fatal(err)
		}
	}

	update("PG")
	if len(n.profiles) != 0 {
		t.Fatalf("unchanged rating rebuilt %v", n.profiles)
	}
	update("G")
	if !slices.Equal(n.profiles, []string{"kid"}) {
		t.Fatalf("rebuilt %v, want kid after its ceiling changed", n.profiles)
	}
}

// Deleting a profile purges its recommendation rows through the purger the
// SQLite user store relies on, since its delete cannot reach Postgres.
func TestDeleteProfilePurgesRecommendations(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatal(err)
	}
	h := NewProfileHandler(testUserStoreProvider{store: store})
	purger := &recordingProfilePurger{}
	h.RecommendationsPurger = purger

	req := newAuthorizedProfileRequestWithRole(http.MethodDelete, "/profiles/profile-2", "", "user", "profile-1")
	rr := httptest.NewRecorder()
	h.HandleDeleteProfile(rr, withProfileRouteParam(req, "id", "profile-2"))

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !slices.Equal(purger.calls, []string{"1/profile-2"}) {
		t.Fatalf("purge calls = %v, want one for profile-2", purger.calls)
	}
}
