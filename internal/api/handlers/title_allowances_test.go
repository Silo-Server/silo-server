package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
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

// UnallowedCollectionTitles pages the candidates by their index, used as the
// position.
func (s *memoryAllowances) UnallowedCollectionTitles(_ context.Context, _ int, _, _ string, after userstore.CollectionPosition, limit int) ([]userstore.CollectionPosition, error) {
	start := 0
	if after.MediaItemID != "" {
		start = after.Position + 1
	}
	var out []userstore.CollectionPosition
	for i := start; i < len(s.candidates) && len(out) < limit; i++ {
		out = append(out, userstore.CollectionPosition{Position: i, MediaItemID: s.candidates[i]})
	}
	return out, nil
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

func (r libraryItemRepo) EnsureAccessibleIDs(ctx context.Context, ids []string, filter catalog.AccessFilter) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = r.EnsureAccessible(ctx, id, filter) == nil
	}
	return out, nil
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

// A copy keeps only titles in a library the target profile can reach, the
// rule a single allowance applies, so a later library change cannot turn on
// an above-limit allowance nobody granted for that library.
func TestAllowCollectionTitlesSkipsUnreachableLibraries(t *testing.T) {
	ctx := context.Background()
	base := newHouseholdTestStore(t)
	for _, p := range []userstore.Profile{{ID: "parent", Name: "Parent"}, {ID: "kid", Name: "Kid", MaxContentRating: "PG"}} {
		if err := base.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	collection, err := base.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "parent", Name: "List", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	repo := libraryItemRepo{
		markItemRepo: markItemRepo{items: map[string]*models.MediaItem{
			"movie:r":     {ContentID: "movie:r", Type: "movie", ContentRating: "R"},
			"movie:other": {ContentID: "movie:other", Type: "movie", ContentRating: "R"},
		}},
		library: map[string]int{"movie:r": 1, "movie:other": 2},
	}
	store := &memoryAllowances{titles: map[string]bool{}, candidates: []string{"movie:r", "movie:other"}}
	h := NewTitleAllowanceHandler(
		NewPersonalDataHandler(testUserStoreProvider{store: base}, repo),
		libraryScopes{libraries: map[string][]int{"kid": {1}}}, store,
	)
	actor := AllowanceActor{UserID: 1, ActiveProfileID: "parent", VerifyProfile: func(*userstore.Profile) error { return nil }}
	added, err := h.AllowCollectionTitles(ctx, actor, "kid", collection.ID)
	if err != nil || added != 1 || !slices.Equal(store.copied, []string{"movie:r"}) {
		t.Fatalf("AllowCollectionTitles = %d, %v; copied %v; want only movie:r", added, err, store.copied)
	}
}

// Titles the target profile cannot reach are filtered while a copy pages, not
// after it fills its budget, so they cannot keep it from the reachable titles
// that follow them.
func TestAllowCollectionTitlesPagesPastUnreachableLibraries(t *testing.T) {
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
	repo := libraryItemRepo{markItemRepo: markItemRepo{items: map[string]*models.MediaItem{}}, library: map[string]int{}}
	store := &memoryAllowances{titles: map[string]bool{}}
	add := func(id string, library int) {
		repo.items[id] = &models.MediaItem{ContentID: id, Type: "movie"}
		repo.library[id] = library
		store.candidates = append(store.candidates, id)
	}
	for i := range maxCopiedTitles {
		add(fmt.Sprintf("movie:other-library-%d", i), 2)
	}
	add("movie:reachable", 1)
	h := NewTitleAllowanceHandler(
		NewPersonalDataHandler(testUserStoreProvider{store: base}, repo),
		libraryScopes{libraries: map[string][]int{"kid": {1}}}, store,
	)
	actor := AllowanceActor{UserID: 1, ActiveProfileID: "parent", VerifyProfile: func(*userstore.Profile) error { return nil }}
	added, err := h.AllowCollectionTitles(ctx, actor, "kid", collection.ID)
	if err != nil || added != 1 || !slices.Equal(store.copied, []string{"movie:reachable"}) {
		t.Fatalf("AllowCollectionTitles = %d, %v; copied %d titles; want only movie:reachable", added, err, len(store.copied))
	}
}

// multiLibraryRepo holds each item in several libraries, as
// media_item_libraries can, and answers access by any of them.
type multiLibraryRepo struct {
	markItemRepo
	libraries map[string][]int
}

func (r multiLibraryRepo) EnsureAccessible(_ context.Context, id string, filter catalog.AccessFilter) error {
	if _, ok := r.items[id]; !ok {
		return catalog.ErrItemNotFound
	}
	if filter.AllowedLibraryIDs == nil {
		return nil
	}
	for _, lib := range r.libraries[id] {
		if slices.Contains(filter.AllowedLibraryIDs, lib) {
			return nil
		}
	}
	return catalog.ErrItemNotFound
}

func (r multiLibraryRepo) EnsureAccessibleIDs(ctx context.Context, ids []string, filter catalog.AccessFilter) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = r.EnsureAccessible(ctx, id, filter) == nil
	}
	return out, nil
}

// orderedPreview evaluates a smart query as the catalog would: the items in
// order that the access filter admits, up to the limit.
type orderedPreview struct {
	repo  multiLibraryRepo
	order []string
}

func (p orderedPreview) Preview(ctx context.Context, _ catalog.QueryDefinition, access catalog.AccessFilter, limit int) ([]*models.MediaItem, int, error) {
	var out []*models.MediaItem
	for _, id := range p.order {
		if len(out) < limit && p.repo.EnsureAccessible(ctx, id, access) == nil {
			out = append(out, p.repo.items[id])
		}
	}
	return out, len(out), nil
}

// A copy checks the acting and the target profile's access separately: a
// title the actor sees through one library and the target reaches through
// another is copied, and a limited smart collection's membership is what the
// actor sees, so the target check never pulls in titles outside it.
func TestAllowCollectionTitlesChecksActorAndTargetSeparately(t *testing.T) {
	ctx := context.Background()
	base := newHouseholdTestStore(t)
	for _, p := range []userstore.Profile{{ID: "parent", Name: "Parent"}, {ID: "kid", Name: "Kid", MaxContentRating: "PG"}} {
		if err := base.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	repo := multiLibraryRepo{
		markItemRepo: markItemRepo{items: map[string]*models.MediaItem{
			"movie:both":        {ContentID: "movie:both", Type: "movie"},
			"movie:actor-only":  {ContentID: "movie:actor-only", Type: "movie"},
			"movie:also-shared": {ContentID: "movie:also-shared", Type: "movie"},
		}},
		// The actor sees library 1; the target reaches library 2.
		libraries: map[string][]int{"movie:both": {1, 2}, "movie:actor-only": {1}, "movie:also-shared": {1, 2}},
	}
	actor := AllowanceActor{UserID: 1, ActiveProfileID: "parent", VerifyProfile: func(*userstore.Profile) error { return nil },
		Viewer: PersonalListViewer{Access: catalog.AccessFilter{AllowedLibraryIDs: []int{1}}}}
	scopes := libraryScopes{libraries: map[string][]int{"kid": {2}}}

	t.Run("stored", func(t *testing.T) {
		collection, err := base.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "parent", Name: "Stored", CollectionType: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		store := &memoryAllowances{titles: map[string]bool{}, candidates: []string{"movie:both", "movie:actor-only"}}
		h := NewTitleAllowanceHandler(NewPersonalDataHandler(testUserStoreProvider{store: base}, repo), scopes, store)
		added, err := h.AllowCollectionTitles(ctx, actor, "kid", collection.ID)
		if err != nil || added != 1 || !slices.Equal(store.copied, []string{"movie:both"}) {
			t.Fatalf("AllowCollectionTitles = %d, %v; copied %v; want movie:both, seen and reached through different libraries", added, err, store.copied)
		}
	})

	t.Run("smart", func(t *testing.T) {
		collection, err := base.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "parent", Name: "Smart", CollectionType: "smart",
			QueryDefinition: `{"match":"all","groups":[{"match":"all","rules":[{"field":"content_rating","op":"is","value":"R"}]}],"limit":2}`})
		if err != nil {
			t.Fatal(err)
		}
		store := &memoryAllowances{titles: map[string]bool{}}
		h := NewTitleAllowanceHandler(NewPersonalDataHandler(testUserStoreProvider{store: base}, repo), scopes, store)
		h.smart = orderedPreview{repo: repo, order: []string{"movie:actor-only", "movie:both", "movie:also-shared"}}
		added, err := h.AllowCollectionTitles(ctx, actor, "kid", collection.ID)
		if err != nil || added != 1 || !slices.Equal(store.copied, []string{"movie:both"}) {
			t.Fatalf("AllowCollectionTitles = %d, %v; copied %v; want only movie:both, the reachable one of the collection's 2 members", added, err, store.copied)
		}
	})
}

// ownerLibraries resolves a collection owner's access as its allowed
// libraries.
type ownerLibraries map[string][]int

func (o ownerLibraries) OwnerFilter(_ context.Context, _ int, ownerProfileID string) (catalog.AccessFilter, error) {
	return catalog.AccessFilter{AllowedLibraryIDs: o[ownerProfileID]}, nil
}

// A copy of another profile's shared collection takes only what the actor
// sees in it, titles its owner can access too, so it never turns a member
// hidden from the owner into an allowance.
func TestAllowCollectionTitlesKeepsToTheOwnersAccess(t *testing.T) {
	ctx := context.Background()
	base := newHouseholdTestStore(t)
	for _, p := range []userstore.Profile{{ID: "parent", Name: "Parent"}, {ID: "kid", Name: "Kid", MaxContentRating: "PG"}, {ID: "teen", Name: "Teen"}} {
		if err := base.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	collection, err := base.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "teen", Name: "Teen's list", CollectionType: "manual", IsShared: true})
	if err != nil {
		t.Fatal(err)
	}
	repo := libraryItemRepo{
		markItemRepo: markItemRepo{items: map[string]*models.MediaItem{
			"movie:owner-hidden": {ContentID: "movie:owner-hidden", Type: "movie", ContentRating: "R"},
			"movie:shared":       {ContentID: "movie:shared", Type: "movie", ContentRating: "R"},
		}},
		library: map[string]int{"movie:owner-hidden": 1, "movie:shared": 2},
	}
	store := &memoryAllowances{titles: map[string]bool{}, candidates: []string{"movie:owner-hidden", "movie:shared"}}
	h := NewTitleAllowanceHandler(
		NewPersonalDataHandler(testUserStoreProvider{store: base}, repo),
		libraryScopes{libraries: map[string][]int{"kid": {1, 2}}}, store,
	).WithCollectionOwners(ownerLibraries{"teen": {2}})
	actor := AllowanceActor{UserID: 1, ActiveProfileID: "parent", VerifyProfile: func(*userstore.Profile) error { return nil }}
	added, err := h.AllowCollectionTitles(ctx, actor, "kid", collection.ID)
	if err != nil || added != 1 || !slices.Equal(store.copied, []string{"movie:shared"}) {
		t.Fatalf("AllowCollectionTitles = %d, %v; copied %v; want only movie:shared", added, err, store.copied)
	}
}
