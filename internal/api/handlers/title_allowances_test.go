package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// The Postgres store is the one that keeps allowances.
var _ titleAllowanceStore = (*pgstore.TitleAllowances)(nil)

// memoryAllowances is an in-memory titleAllowanceStore for one account.
type memoryAllowances struct {
	titles     map[string]bool // profileID + "/" + contentID
	candidates []string        // what UnallowedCollectionTitles pages through
	copied     []string        // what AllowTitles was asked to allow
}

func (s *memoryAllowances) ListTitleAllowances(_ context.Context, _ int, profileID string) ([]userstore.TitleAllowance, error) {
	var out []userstore.TitleAllowance
	for key := range s.titles {
		if id, ok := strings.CutPrefix(key, profileID+"/"); ok {
			out = append(out, userstore.TitleAllowance{ContentID: id})
		}
	}
	return out, nil
}

func (s *memoryAllowances) SetTitleAllowance(_ context.Context, _ userstore.AllowanceManager, profileID, contentID string, allowed bool) (bool, error) {
	key := profileID + "/" + contentID
	if s.titles[key] == allowed {
		return false, nil
	}
	if allowed {
		s.titles[key] = true
	} else {
		delete(s.titles, key)
	}
	return true, nil
}

func (s *memoryAllowances) ListCollectionAllowances(context.Context, int, string) ([]userstore.CollectionAllowance, error) {
	return nil, nil
}

func (s *memoryAllowances) SetCollectionAllowance(context.Context, userstore.AllowanceManager, string, string, bool) (bool, error) {
	return true, nil
}

func (s *memoryAllowances) UnallowedCollectionTitles(_ context.Context, _ int, _, _ string, offset, limit int) ([]string, error) {
	if offset >= len(s.candidates) {
		return nil, nil
	}
	return s.candidates[offset:min(offset+limit, len(s.candidates))], nil
}

func (s *memoryAllowances) AllowTitles(_ context.Context, _ userstore.AllowanceManager, _ string, ids []string) (int, error) {
	s.copied = append(s.copied, ids...)
	return len(ids), nil
}

// libraryScopes resolves every profile to one library, and the kid to a PG
// ceiling.
type libraryScopes struct{ libraries map[string][]int }

func (s libraryScopes) Resolve(_ context.Context, in access.ResolveInput) (access.Scope, error) {
	scope := access.Scope{ProfileID: in.ProfileID, AllowedLibraryIDs: s.libraries[in.ProfileID]}
	if in.ProfileID == "kid" {
		scope.MaxContentRating = "PG"
	}
	return scope, nil
}

// libraryItemRepo is a markItemRepo that answers access by library and by an
// "R" rating the PG ceiling hides.
type libraryItemRepo struct {
	markItemRepo
	library map[string]int
}

func (r libraryItemRepo) EnsureAccessible(ctx context.Context, id string, filter catalog.AccessFilter) error {
	if err := r.markItemRepo.EnsureAccessible(ctx, id, filter); err != nil {
		return err
	}
	if filter.AllowedLibraryIDs != nil {
		found := false
		for _, lib := range filter.AllowedLibraryIDs {
			found = found || lib == r.library[id]
		}
		if !found {
			return catalog.ErrItemNotFound
		}
	}
	if filter.MaxContentRating != "" && r.items[id].ContentRating == "R" {
		return catalog.ErrItemNotFound
	}
	return nil
}

// TestTitleAllowanceAuthority pins who may allow what: only the household's
// primary profile, only movies and series, only for a profile that has a
// limit, and only titles in a library the profile can reach. The states the
// title page shows follow from the same checks.
func TestTitleAllowanceAuthority(t *testing.T) {
	ctx := context.Background()
	base := newHouseholdTestStore(t)
	for _, p := range []userstore.Profile{
		{ID: "parent", Name: "Parent"},
		{ID: "kid", Name: "Kid", MaxContentRating: "PG"},
		{ID: "teen", Name: "Teen"},
	} {
		if err := base.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	store := &memoryAllowances{titles: map[string]bool{}}
	repo := libraryItemRepo{
		markItemRepo: markItemRepo{items: map[string]*models.MediaItem{
			"movie:r":       {ContentID: "movie:r", Type: "movie", ContentRating: "R"},
			"movie:pg":      {ContentID: "movie:pg", Type: "movie", ContentRating: "PG"},
			"movie:other":   {ContentID: "movie:other", Type: "movie", ContentRating: "R"},
			"episode:r-1x1": {ContentID: "episode:r-1x1", Type: "episode", ContentRating: "R"},
		}},
		library: map[string]int{"movie:r": 1, "movie:pg": 1, "movie:other": 2, "episode:r-1x1": 1},
	}
	h := NewTitleAllowanceHandler(
		NewPersonalDataHandler(testUserStoreProvider{store: base}, repo),
		libraryScopes{libraries: map[string][]int{"kid": {1}, "teen": {1, 2}}},
		store,
	)
	actor := func(profileID string) AllowanceActor {
		return AllowanceActor{UserID: 1, ActiveProfileID: profileID, VerifyProfile: func(*userstore.Profile) error { return nil }}
	}
	status := func(err error) int {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return apiErr.Status
		}
		if err != nil {
			return -1
		}
		return http.StatusOK
	}

	cases := []struct {
		name    string
		actor   string
		profile string
		item    string
		want    int
	}{
		{"a non-primary profile cannot allow", "teen", "kid", "movie:r", http.StatusForbidden},
		{"an episode is refused", "parent", "kid", "episode:r-1x1", http.StatusUnprocessableEntity},
		{"a profile with no limit is refused", "parent", "teen", "movie:r", http.StatusUnprocessableEntity},
		{"a title outside the profile's libraries is refused", "parent", "kid", "movie:other", http.StatusUnprocessableEntity},
		{"an unknown title is not found", "parent", "kid", "movie:missing", http.StatusNotFound},
		{"the primary profile allows a reachable title", "parent", "kid", "movie:r", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := status(h.SetTitleAllowance(ctx, actor(tc.actor), tc.profile, tc.item, true)); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
	if !store.titles["kid/movie:r"] || len(store.titles) != 1 {
		t.Fatalf("allowances = %v, want only kid/movie:r", store.titles)
	}

	states := map[string]map[string]string{}
	for _, item := range []string{"movie:r", "movie:pg", "movie:other"} {
		got, err := h.TitleAllowanceStates(ctx, actor("parent"), item)
		if err != nil {
			t.Fatalf("states(%s): %v", item, err)
		}
		states[item] = map[string]string{}
		for _, s := range got {
			states[item][s.Profile.ID] = s.State
		}
	}
	want := map[string]map[string]string{
		"movie:r":     {"kid": AllowanceStateAllowed, "teen": AllowanceStateWithinLimits},
		"movie:pg":    {"kid": AllowanceStateWithinLimits, "teen": AllowanceStateWithinLimits},
		"movie:other": {"kid": AllowanceStateNoLibraryAccess, "teen": AllowanceStateWithinLimits},
	}
	for item, byProfile := range want {
		for profile, state := range byProfile {
			if states[item][profile] != state {
				t.Errorf("%s for %s = %q, want %q", item, profile, states[item][profile], state)
			}
		}
		if _, ok := states[item]["parent"]; ok {
			t.Errorf("%s lists the acting profile", item)
		}
	}
}

// A stored collection's copy reads its candidates page by page, so titles the
// household manager cannot see, which are never allowed and so stay
// candidates on every copy, cannot keep a copy from reaching the visible
// titles after them.
func TestAllowCollectionTitlesPagesPastHiddenTitles(t *testing.T) {
	ctx := context.Background()
	base := newHouseholdTestStore(t)
	for _, p := range []userstore.Profile{{ID: "parent", Name: "Parent"}, {ID: "kid", Name: "Kid", MaxContentRating: "PG"}} {
		if err := base.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	collection, err := base.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "parent", Name: "Big list", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	visible := map[string]*models.MediaItem{}
	store := &memoryAllowances{titles: map[string]bool{}}
	for i := range maxCopiedTitles {
		store.candidates = append(store.candidates, fmt.Sprintf("movie:hidden-%d", i))
	}
	for i := range 3 {
		id := fmt.Sprintf("movie:visible-%d", i)
		visible[id] = &models.MediaItem{ContentID: id, Type: "movie"}
		store.candidates = append(store.candidates, id)
	}
	h := NewTitleAllowanceHandler(
		NewPersonalDataHandler(testUserStoreProvider{store: base}, markItemRepo{items: visible}),
		libraryScopes{}, store,
	)
	actor := AllowanceActor{UserID: 1, ActiveProfileID: "parent", VerifyProfile: func(*userstore.Profile) error { return nil }}
	added, err := h.AllowCollectionTitles(ctx, actor, "kid", collection.ID)
	if err != nil || added != 3 || len(store.copied) != 3 {
		t.Fatalf("AllowCollectionTitles = %d, %v; copied %v; want the 3 visible titles past the hidden ones", added, err, store.copied)
	}
}
