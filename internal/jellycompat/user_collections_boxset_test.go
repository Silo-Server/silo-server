package jellycompat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/audiobooks"
	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// fakeUserCollection is one row in fakeUserCollectionSource, carrying the
// ownership and sharing facts the real store enforces in SQL: its creator
// sees it, and so does every profile on the login when it is shared.
type fakeUserCollection struct {
	usercollections.ServerVisibleCollection
	userID     int
	shared     bool
	libraryIDs []int // empty means library-agnostic
}

// fakeUserCollectionSource is an in-memory userCollectionSource mirroring the
// store's privacy rules. It records the identity it was asked about, so tests
// can pin that handlers pass the session's own user and profile through rather
// than widening the query.
type fakeUserCollectionSource struct {
	rows []fakeUserCollection

	gotUserID     int
	gotProfileID  string
	gotLibraryIDs []int

	// counts answers CountVisible; a missing ID mimics a count that failed,
	// and an ID in unavailable an owner that could not be resolved.
	counts         map[string]int
	unavailable    map[string]bool
	gotCountIDs    []string
	gotCountAccess catalog.AccessFilter
	gotCountViewer string
}

type fakePersonalCollectionResolver struct {
	result    *catalog.CatalogResult
	gotReq    catalog.CatalogRequest
	gotAccess catalog.AccessFilter
}

func (f *fakePersonalCollectionResolver) Resolve(_ context.Context, req catalog.CatalogRequest, access catalog.AccessFilter) (*catalog.CatalogResult, error) {
	f.gotReq, f.gotAccess = req, access
	return f.result, nil
}

func (f *fakeUserCollectionSource) visible(userID int, profileID string, row fakeUserCollection) bool {
	return row.userID == userID && (row.CreatorProfileID == profileID || row.shared)
}

func (f *fakeUserCollectionSource) List(_ context.Context, userID int, profileID string, libraryIDs []int) ([]usercollections.ServerVisibleCollection, error) {
	f.gotUserID, f.gotProfileID, f.gotLibraryIDs = userID, profileID, append([]int(nil), libraryIDs...)
	var out []usercollections.ServerVisibleCollection
	for _, row := range f.rows {
		if !f.visible(userID, profileID, row) {
			continue
		}
		if len(row.libraryIDs) > 0 && !slices.ContainsFunc(row.libraryIDs, func(id int) bool { return slices.Contains(libraryIDs, id) }) {
			continue
		}
		out = append(out, row.ServerVisibleCollection)
	}
	return out, nil
}

func (f *fakeUserCollectionSource) Get(_ context.Context, userID int, profileID, id string, libraryIDs []int) (*usercollections.ServerVisibleCollection, error) {
	f.gotUserID, f.gotProfileID = userID, profileID
	for _, row := range f.rows {
		sum := sha256.Sum256([]byte(row.ID))
		if fmt.Sprintf("%x", sum[:14]) == id && f.visible(userID, profileID, row) &&
			(len(row.libraryIDs) == 0 || slices.ContainsFunc(row.libraryIDs, func(id int) bool { return slices.Contains(libraryIDs, id) })) {
			found := row.ServerVisibleCollection
			return &found, nil
		}
	}
	return nil, nil
}

func (f *fakeUserCollectionSource) AnyVisible(_ context.Context, userID int, profileID string, libraryIDs []int) (bool, error) {
	f.gotUserID, f.gotProfileID = userID, profileID
	for _, row := range f.rows {
		if f.visible(userID, profileID, row) &&
			(len(row.libraryIDs) == 0 || slices.ContainsFunc(row.libraryIDs, func(id int) bool { return slices.Contains(libraryIDs, id) })) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeUserCollectionSource) CountVisible(_ context.Context, _ catalog.PersonalCollectionAccess, userID int, viewerProfileID string, collections []usercollections.ServerVisibleCollection, viewer catalog.AccessFilter) (map[string]int, map[string]bool) {
	f.gotUserID, f.gotCountViewer, f.gotCountAccess = userID, viewerProfileID, viewer
	f.gotCountIDs = f.gotCountIDs[:0]
	out := make(map[string]int, len(collections))
	unavailable := make(map[string]bool)
	for _, c := range collections {
		f.gotCountIDs = append(f.gotCountIDs, c.ID)
		if f.unavailable[c.ID] {
			unavailable[c.ID] = true
			continue
		}
		if n, ok := f.counts[c.ID]; ok {
			out[c.ID] = n
		}
	}
	return out, unavailable
}

func (f *fakeUserCollectionSource) ImageCandidates(_ context.Context, id string) ([]usercollections.ServerVisibleCollection, error) {
	var out []usercollections.ServerVisibleCollection
	for _, row := range f.rows {
		sum := sha256.Sum256([]byte(row.ID))
		if fmt.Sprintf("%x", sum[:14]) == id {
			out = append(out, row.ServerVisibleCollection)
		}
	}
	return out, nil
}

// ownedUserCollection builds an opted-in personal collection owned by the
// identity collectionsTestSession() carries (user 1, profile-1).
func ownedUserCollection(id, name string) fakeUserCollection {
	return fakeUserCollection{
		ServerVisibleCollection: usercollections.ServerVisibleCollection{
			ID:               id,
			Name:             name,
			CreatorProfileID: "profile-1",
			CollectionType:   "mdblist",
		},
		userID: 1,
	}
}

func newUserCollectionsTestHandler(admin *fakeCollectionSource, personal *fakeUserCollectionSource, libraries []upstreamUserLibrary, itemRepo itemRepoForBatchLoader) *ItemsHandler {
	h := newCollectionsTestHandler(admin, libraries, itemRepo)
	h.userCollections = personal
	return h
}

func boxSetNames(items []baseItemDTO) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	slices.Sort(names)
	return names
}

func TestHandleItems_BoxSetListingIncludesOwnPersonalCollections(t *testing.T) {
	admin := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Studio Picks", Visibility: "visible", ItemCount: 3},
		},
	}
	watchlist := ownedUserCollection("u-1", "My Watchlist")
	watchlist.ItemCount = 7
	personal := &fakeUserCollectionSource{rows: []fakeUserCollection{watchlist}}
	h := newUserCollectionsTestHandler(admin, personal, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	result := performItemsRequest(t, h, "/Items?ParentId="+collectionsViewID)
	if got := boxSetNames(result.Items); !slices.Equal(got, []string{"My Watchlist", "Studio Picks"}) {
		t.Fatalf("expected library and personal collections listed together, got %v", got)
	}
	if personal.gotUserID != 1 || personal.gotProfileID != "profile-1" {
		t.Fatalf("expected the listing scoped to the session identity, got user %d profile %q",
			personal.gotUserID, personal.gotProfileID)
	}
	if !slices.Equal(personal.gotLibraryIDs, []int{1}) {
		t.Fatalf("expected the visible library set [1], got %v", personal.gotLibraryIDs)
	}
	for _, item := range result.Items {
		switch item.Name {
		case "My Watchlist":
			// No live count in the fake, so the stored count is the fallback.
			if item.Type != "BoxSet" || !item.IsFolder || item.ChildCount != 7 {
				t.Fatalf("unexpected personal BoxSet DTO: %+v", item)
			}
		case "Studio Picks":
			if item.ChildCount != 3 {
				t.Fatalf("library collection lost its stored count: %+v", item)
			}
		}
	}
}

// TestPersonalBoxSetChildCountIsVisibleCount pins that personal BoxSets report
// the items they show this viewer, as the native collection routes do, on the
// listing, the detail route and Ids re-hydration. The stored item_count is
// written only by import syncs, so it only stands in when a count fails.
func TestPersonalBoxSetChildCountIsVisibleCount(t *testing.T) {
	const collectionID = "731d3da2-4f4b-4a71-8f2f-38e1d34775b0"
	counted := ownedUserCollection(collectionID, "Hand Picked")
	counted.CollectionType = "manual"
	uncounted := ownedUserCollection("u-2", "Imported")
	uncounted.ItemCount = 9
	personal := &fakeUserCollectionSource{
		rows:   []fakeUserCollection{counted, uncounted},
		counts: map[string]int{collectionID: 4},
	}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{}, personal,
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	wantCounts := map[string]int{"Hand Picked": 4, "Imported": 9}
	checkCounts := func(t *testing.T, items []baseItemDTO) {
		t.Helper()
		if len(items) == 0 {
			t.Fatal("expected BoxSets")
		}
		for _, item := range items {
			if want := wantCounts[item.Name]; item.ChildCount != want || item.RecursiveItemCount != want {
				t.Errorf("%s: ChildCount=%d RecursiveItemCount=%d, want %d", item.Name, item.ChildCount, item.RecursiveItemCount, want)
			}
		}
		if personal.gotUserID != 1 {
			t.Errorf("counted for user %d, want the session's user 1", personal.gotUserID)
		}
		// Children list only video types, so the count must leave the others out.
		for _, excluded := range []string{"audiobook", "podcast", "ebook", "manga"} {
			if !slices.Contains(personal.gotCountAccess.ExcludedMediaTypes, excluded) {
				t.Errorf("count access does not exclude %s: %+v", excluded, personal.gotCountAccess)
			}
		}
	}

	t.Run("listing", func(t *testing.T) {
		result := performItemsRequest(t, h, "/Items?ParentId="+collectionsViewID)
		checkCounts(t, result.Items)
	})
	t.Run("detail", func(t *testing.T) {
		rec := requestBoxSetItem(t, h, collectionID)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var item baseItemDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		checkCounts(t, []baseItemDTO{item})
	})
	t.Run("ids", func(t *testing.T) {
		routeID := NewResourceIDCodec().EncodeStringID(EncodedIDUserCollection, collectionID)
		result := performItemsRequest(t, h, "/Items?Ids="+routeID)
		checkCounts(t, result.Items)
	})
}

// TestHandleItems_BoxSetListingHidesOtherProfilesPersonalCollections is the
// privacy pin: neither another login's rows nor another profile's unshared
// collections may reach this session, while a collection another profile on
// the login shares does.
func TestHandleItems_BoxSetListingHidesOtherProfilesPersonalCollections(t *testing.T) {
	otherUser := ownedUserCollection("u-2", "Someone Else's List")
	otherUser.userID, otherUser.shared = 2, true
	otherProfile := ownedUserCollection("u-3", "Not My Profile")
	otherProfile.CreatorProfileID = "profile-2"
	sharedByOther := ownedUserCollection("u-4", "Shared With Me")
	sharedByOther.CreatorProfileID, sharedByOther.shared = "profile-2", true
	personal := &fakeUserCollectionSource{rows: []fakeUserCollection{otherUser, otherProfile, sharedByOther}}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{}, personal,
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	result := performItemsRequest(t, h, "/Items?ParentId="+collectionsViewID)
	if got := boxSetNames(result.Items); !slices.Equal(got, []string{"Shared With Me"}) {
		t.Fatalf("expected only the collection shared on this login, got %v", got)
	}
}

func TestHandleItems_BoxSetListingHidesCollectionsOutsideVisibleLibraries(t *testing.T) {
	hiddenLibrary := ownedUserCollection("u-hidden", "Hidden Library List")
	hiddenLibrary.libraryIDs = []int{2}
	personal := &fakeUserCollectionSource{rows: []fakeUserCollection{hiddenLibrary}}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{}, personal,
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	result := performItemsRequest(t, h, "/Items?ParentId="+collectionsViewID)
	if len(result.Items) != 0 {
		t.Fatalf("expected hidden-library collection to stay off Jellyfin, got %v", boxSetNames(result.Items))
	}
}

func TestHandleItem_PersonalCollectionResolvesForOwnerOnly(t *testing.T) {
	hidden := ownedUserCollection("u-3", "Not My Profile")
	hidden.CreatorProfileID = "profile-2"
	personal := &fakeUserCollectionSource{rows: []fakeUserCollection{
		ownedUserCollection("u-1", "My Watchlist"),
		hidden,
	}}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{}, personal, nil, nil)

	t.Run("owned", func(t *testing.T) {
		rec := requestBoxSetItem(t, h, "u-1")
		if rec.Code != 200 {
			t.Fatalf("expected 200 for an owned collection, got %d: %s", rec.Code, rec.Body.String())
		}
		var item baseItemDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if item.Type != "BoxSet" || item.Name != "My Watchlist" {
			t.Fatalf("unexpected BoxSet: %+v", item)
		}
	})

	t.Run("not shared with this profile", func(t *testing.T) {
		if rec := requestBoxSetItem(t, h, "u-3"); rec.Code != 404 {
			t.Fatalf("expected 404 for a collection this profile cannot see, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func requestBoxSetItem(t *testing.T, h *ItemsHandler, collectionID string) *httptest.ResponseRecorder {
	t.Helper()
	routeID := NewResourceIDCodec().EncodeStringID(EncodedIDUserCollection, collectionID)
	req := httptest.NewRequest("GET", "/Items/"+routeID, nil)
	ctx := context.WithValue(req.Context(), compatSessionKey, collectionsTestSession())
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", routeID)
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.HandleItem(rec, req)
	return rec
}

func TestHandleItems_PersonalBoxSetChildrenUseCatalogResolver(t *testing.T) {
	const collectionID = "731d3da2-4f4b-4a71-8f2f-38e1d34775b0"
	list := ownedUserCollection(collectionID, "Release Order")
	resolver := &fakePersonalCollectionResolver{result: &catalog.CatalogResult{
		Items: []*models.MediaItem{
			{ContentID: "m-new", Type: "movie", Title: "New Release"},
			{ContentID: "m-old", Type: "movie", Title: "Old Release"},
		},
		Total: 2,
	}}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{},
		&fakeUserCollectionSource{rows: []fakeUserCollection{list}},
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)
	h.collectionResolver = resolver

	parentID := NewResourceIDCodec().EncodeStringID(EncodedIDUserCollection, collectionID)
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID)
	if got := boxSetNames(result.Items); len(got) != 2 {
		t.Fatalf("expected resolver items, got %v", got)
	}
	if result.Items[0].Name != "New Release" || result.Items[1].Name != "Old Release" {
		t.Fatalf("expected resolver order, got %q, %q", result.Items[0].Name, result.Items[1].Name)
	}
	if resolver.gotReq.Source != catalog.CatalogSourceUserCollection || resolver.gotReq.CollectionID != collectionID || !resolver.gotReq.UseSourceOrder {
		t.Fatalf("unexpected resolver request: %+v", resolver.gotReq)
	}

	performItemsRequest(t, h, "/Items?ParentId="+parentID+"&SortBy=DateCreated&SortOrder=Descending")
	if resolver.gotReq.UseSourceOrder || resolver.gotReq.Query.Sort != (catalog.QuerySort{Field: "added_at", Order: "desc"}) {
		t.Fatalf("explicit Jellyfin sort was not mapped to the catalog sort: %+v", resolver.gotReq)
	}

	personID := h.codec.EncodeIntID(EncodedIDPerson, 42)
	performItemsRequest(t, h, "/Items?ParentId="+parentID+"&SortBy=Random&PersonIds="+personID+
		"&ImageTypes=Backdrop&Genres=Drama|Mystery&Years=2025,2026&AudioLanguages=sv&SubtitleLanguages=en"+
		"&OfficialRatings=PG-13&MinCommunityRating=7.5&MinPremiereDate=2025-01-01&MaxPremiereDate=2026-12-31")
	if !resolver.gotReq.Randomize || !resolver.gotReq.UseSourceOrder || resolver.gotReq.Query.Sort != (catalog.QuerySort{}) {
		t.Fatalf("random sort was not preserved: %+v", resolver.gotReq)
	}
	if resolver.gotReq.PersonID != 42 || !resolver.gotReq.RequireBackdrop {
		t.Fatalf("existing Jellyfin filters were not forwarded: %+v", resolver.gotReq)
	}
	filters := resolver.gotReq.BrowseOverlay
	if filters == nil || !slices.Equal(filters.Genres, []string{"Drama", "Mystery"}) ||
		!slices.Equal(filters.Years, []int{2025, 2026}) ||
		!slices.Equal(filters.AudioLanguages, []string{"sv"}) ||
		!slices.Equal(filters.SubtitleLanguages, []string{"en"}) ||
		!slices.Equal(filters.OfficialRatings, []string{"PG-13"}) ||
		filters.MinCommunityRating != 7.5 || filters.MinPremiereDate != "2025-01-01" ||
		filters.MaxPremiereDate != "2026-12-31" {
		t.Fatalf("compat browse filters were not forwarded: %+v", filters)
	}
}

func TestHandleItems_PersonalBoxSetRouteSurvivesFreshCodec(t *testing.T) {
	for _, collectionID := range []string{
		"731d3da2-4f4b-4a71-8f2f-38e1d34775b0",
		"01K3M9K0R7D6Y9T7F1P6W2H8ZX", // ABS creates ULIDs, including after migration 156.
		"731d3da2-4f4b-5a71-8f2f-38e1d34775b0",
		"legacy-collection",
	} {
		t.Run(collectionID, func(t *testing.T) {
			list := ownedUserCollection(collectionID, "Restart Safe")
			h := newUserCollectionsTestHandler(&fakeCollectionSource{},
				&fakeUserCollectionSource{rows: []fakeUserCollection{list}},
				[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)
			h.collectionResolver = &fakePersonalCollectionResolver{result: &catalog.CatalogResult{
				Items: []*models.MediaItem{{ContentID: "m-1", Type: "movie", Title: "Still Here"}},
				Total: 1,
			}}

			parentID := NewResourceIDCodec().EncodeStringID(EncodedIDUserCollection, collectionID)
			result := performItemsRequest(t, h, "/Items?ParentId="+parentID)
			if len(result.Items) != 1 || result.Items[0].Name != "Still Here" {
				t.Fatalf("fresh codec failed to resolve cached personal route: %+v", result.Items)
			}
		})
	}
}

// TestPersonalBoxSetSkipsAudiobookshelfCollectionsDB pins that an
// Audiobookshelf (beta) collection never becomes a BoxSet: such rows share the
// personal collection table but are not native, even when a legacy row is
// opted into server collections.
func TestPersonalBoxSetSkipsAudiobookshelfCollectionsDB(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, "boxset-test-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{StreamAppUserID: userID, ProfileID: uuid.NewString()}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: session.ProfileID, Name: "Test profile"}); err != nil {
		t.Fatal(err)
	}
	// Use the current ABS write path, not just a hand-inserted legacy row.
	id := ulid.Make().String()
	if err := (&audiobooks.ABSCollectionStore{Pool: pool}).CreateCollection(ctx, abs.Collection{
		ID: id, UserID: strconv.Itoa(userID), ProfileID: session.ProfileID, Name: "Test collection",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_personal_collections SET include_in_server_collections = TRUE WHERE user_id = $1 AND id = $2`, userID, id); err != nil {
		t.Fatal(err)
	}
	h := newCollectionsTestHandler(&fakeCollectionSource{}, nil, nil)
	h.userCollections = usercollections.NewStore(pool)
	if listing := performItemsRequest(t, h, "/Items?IncludeItemTypes=BoxSet", session); len(listing.Items) != 0 {
		t.Fatalf("Audiobookshelf collection listed as a BoxSet: %+v", listing.Items)
	}
	routeID := h.codec.EncodeStringID(EncodedIDUserCollection, id)
	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", routeID)
	req = req.WithContext(context.WithValue(context.WithValue(req.Context(), compatSessionKey, session), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.HandleItem(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Audiobookshelf collection detail: status=%d, want 404", rec.Code)
	}
}

// TestUserViews_ShowsCollectionsViewForPersonalCollectionsOnly guards the case
// that made the bug total: without the personal probe, a user whose only
// collections are personal never gets the Collections tab, so nothing below it
// is reachable either.
func TestUserViews_ShowsCollectionsViewForPersonalCollectionsOnly(t *testing.T) {
	personal := &fakeUserCollectionSource{rows: []fakeUserCollection{ownedUserCollection("u-1", "My Watchlist")}}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{}, personal,
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	result := performItemsRequest(t, h, "/Items")
	if len(result.Items) != 2 || result.Items[0].ID != collectionsViewID {
		t.Fatalf("expected the Collections view ahead of the library, got %+v", result.Items)
	}
}

// TestServeCollectionImage_PersonalCollectionOwnerOnly pins that personal
// artwork follows the collection's privacy: the owner or a signed capability
// resolves it, while a stranger or anonymous request without that tag does not.
func TestServeCollectionImage_PersonalCollectionOwnerOnly(t *testing.T) {
	const collectionID = "731d3da2-4f4b-4a71-8f2f-38e1d34775b0"
	codec := NewResourceIDCodec()
	routeID := codec.EncodeStringID(EncodedIDUserCollection, collectionID)
	h := &ImagesHandler{
		content:         &librariesContentService{libraries: []upstreamUserLibrary{{ID: 1, Type: "movies"}}},
		codec:           codec,
		images:          NewImageCache(time.Hour, time.Now),
		imageTags:       newImageTagSigner("image-secret"),
		collections:     &fakeCollectionSource{},
		userCollections: &fakeUserCollectionSource{rows: []fakeUserCollection{ownedUserCollection(collectionID, "My Watchlist")}},
	}

	serve := func(requestID string, session *Session, tag string) *httptest.ResponseRecorder {
		path := "/Items/" + requestID + "/Images/Primary"
		if tag != "" {
			path += "?tag=" + tag
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if session != nil {
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, session))
		}
		req = withImageRouteParams(req, requestID, "Primary")
		rec := httptest.NewRecorder()
		h.HandleItemImage(rec, req)
		return rec
	}

	if rec := serve(routeID, collectionsTestSession(), ""); rec.Code != http.StatusOK {
		t.Fatalf("owner: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec := serve(routeID, &Session{StreamAppUserID: 2, ProfileID: "profile-2"}, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("other user: status = %d, want 404", rec.Code)
	}
	if rec := serve(routeID, nil, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("anonymous: status = %d, want 404", rec.Code)
	}
	candidate := libraryCollectionFromUser(ownedUserCollection(collectionID, "My Watchlist").ServerVisibleCollection)
	seed, _ := collectionImageTagSeed(routeID, "Primary", candidate)
	// Anonymous candidates skip the profile ACL, so every UUID representation
	// must still reject a forged capability.
	for _, requestID := range []string{routeID, strings.ReplaceAll(routeID, "-", ""), strings.ToUpper(routeID)} {
		if rec := serve(requestID, nil, h.imageTags.Tag(seed, "")); rec.Code != http.StatusOK {
			t.Errorf("signed capability for %s: status = %d, want 200; body=%s", requestID, rec.Code, rec.Body.String())
		}
		if rec := serve(requestID, nil, "0123456789abcdef"); rec.Code != http.StatusNotFound {
			t.Errorf("forged tag for %s: status = %d, want 404", requestID, rec.Code)
		}
	}
	if rec := serve(routeID, &Session{StreamAppUserID: 2, ProfileID: "profile-2"}, "0123456789abcdef"); rec.Code != http.StatusNotFound {
		t.Fatalf("stranger with a forged tag: status = %d, want 404", rec.Code)
	}
}

func TestPersonalCollectionPosterNetworkBoundary(t *testing.T) {
	var hits atomic.Int32
	const body = "poster-fixture"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		name, poster string
		wantStatus   int
		wantFetch    bool
	}{
		{"loopback URL", server.URL, http.StatusBadGateway, false},
		{"loopback hostname", strings.Replace(server.URL, "127.0.0.1", "localhost", 1), http.StatusBadGateway, false},
		{"stored poster", "collections/test/poster.jpg", http.StatusOK, true},
		{"bundled poster", "/images/test.jpg", http.StatusOK, false},
	} {
		for _, signed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/signed=%t", tc.name, signed), func(t *testing.T) {
				codec := NewResourceIDCodec()
				row := ownedUserCollection(uuid.NewString(), "My Watchlist")
				row.PosterPath = tc.poster
				routeID := codec.EncodeStringID(EncodedIDUserCollection, row.ID)
				h := &ImagesHandler{
					codec: codec, images: NewImageCache(time.Hour, time.Now),
					content:         &librariesContentService{},
					imageTags:       newImageTagSigner("image-secret"),
					userCollections: &fakeUserCollectionSource{rows: []fakeUserCollection{row}},
					posterSigner:    fakeLibraryPosterPresigner{url: server.URL},
					frontendFS:      fstest.MapFS{"images/test.jpg": {Data: []byte(body)}},
				}
				path := "/Items/" + routeID + "/Images/Primary"
				if signed {
					seed, _ := collectionImageTagSeed(routeID, "Primary", libraryCollectionFromUser(row.ServerVisibleCollection))
					path += "?tag=" + compatImageProxyTag(h.imageTags.Tag(seed, ""))
				}
				req := httptest.NewRequest(http.MethodGet, path, nil)
				if !signed {
					req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
					req.Header.Set("User-Agent", "Infuse")
				}
				rec := httptest.NewRecorder()
				before := hits.Load()
				h.HandleItemImage(rec, withImageRouteParams(req, routeID, "Primary"))
				if tc.wantStatus == http.StatusBadGateway &&
					(rec.Header().Get("Content-Security-Policy") != branding.AssetContentSecurityPolicy || rec.Header().Get("X-Content-Type-Options") != "nosniff") {
					t.Error("untrusted poster response lacks sandbox/nosniff headers")
				}
				if rec.Code != tc.wantStatus {
					t.Errorf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
				}
				if fetched := hits.Load() != before; fetched != tc.wantFetch {
					t.Errorf("server-side fetch = %t, want %t", fetched, tc.wantFetch)
				}
				if gotBody := rec.Body.String() == body; gotBody != (tc.wantStatus == http.StatusOK) {
					t.Errorf("returned fixture bytes = %t", gotBody)
				}
			})
		}
	}
}

func TestPersonalCollectionPosterCannotUseLegacyTagFallback(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("internal-marker"))
	}))
	t.Cleanup(server.Close)
	row := ownedUserCollection(uuid.NewString(), "My Watchlist")
	row.PosterPath = server.URL
	items := newUserCollectionsTestHandler(&fakeCollectionSource{},
		&fakeUserCollectionSource{rows: []fakeUserCollection{row}},
		[]upstreamUserLibrary{{ID: 1, Type: "movies"}}, nil)
	listing := performItemsRequest(t, items, "/Items?ParentId="+collectionsViewID)
	if len(listing.Items) != 1 || listing.Items[0].ImageTags["Primary"] == "" {
		t.Fatalf("missing personal BoxSet poster: %+v", listing)
	}
	h := &ImagesHandler{codec: items.codec, images: items.images}
	// A different item ID must not bypass personal-image authorization or the
	// public-network guard via the global, URL-derived legacy cache tag.
	routeID := uuid.NewString()
	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?tag="+tagValue(server.URL), nil)
	req.Header.Set("User-Agent", "Infuse")
	rec := httptest.NewRecorder()
	h.HandleItemImage(rec, withImageRouteParams(req, routeID, "Primary"))
	if rec.Code != http.StatusNotFound || hits.Load() != 0 {
		t.Fatalf("legacy cache bypass: status=%d backend requests=%d body=%s", rec.Code, hits.Load(), rec.Body.String())
	}
}

func TestHandleItems_PersonalBoxSetMediaTypeFilters(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, "boxset-test-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', 'Test Library', true) RETURNING id`).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, libraryID) })
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{StreamAppUserID: userID, ProfileID: "profile-1"}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: session.ProfileID, Name: "Test profile"}); err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: session.ProfileID, Name: "Mixed collection", IncludeInServerCollections: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	episodeID := uuid.NewString()
	for position, item := range []struct{ kind, title string }{
		{"audiobook", "Audio Book"}, {"movie", "First Movie"}, {"podcast", "Podcast"},
		{"series", "Series"}, {"movie", "Second Movie"},
	} {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, created_at) VALUES ($1, $2, $3, NOW() - INTERVAL '1 day')`, id, item.kind, item.title); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, id) })
		if _, err := pool.Exec(ctx, `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, position) VALUES ($1, $2, $3, $4)`, userID, collection.ID, id, position); err != nil {
			t.Fatal(err)
		}
		if item.kind == "series" {
			if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, created_at) VALUES ($1, $2, 1, 1, 'Episode', NOW() - INTERVAL '1 day')`, episodeID, id); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at) VALUES ($1, $2, NOW() - INTERVAL '1 day')`, episodeID, libraryID); err != nil {
				t.Fatal(err)
			}
		}
	}
	h := newCollectionsTestHandler(&fakeCollectionSource{}, []upstreamUserLibrary{{ID: libraryID, Type: "series"}}, nil)
	h.userCollections = usercollections.NewStore(pool)
	h.collectionResolver = catalog.NewCatalogResolver(catalog.NewBrowseRepository(pool), catalog.NewItemRepository(pool)).WithUserStoreProvider(provider)
	h.accessFilter = func(_ context.Context, userID int, profileID string) catalog.AccessFilter {
		return catalog.AccessFilter{UserID: userID, ProfileID: profileID}
	}
	parentID := h.codec.EncodeStringID(EncodedIDUserCollection, collection.ID)
	for _, tc := range []struct {
		query string
		want  []string
		total int
	}{
		{"", []string{"First Movie", "Second Movie", "Series"}, 3},
		{"&MediaTypes=Video", []string{"First Movie", "Second Movie", "Series"}, 3},
		{"&SortBy=SortName", []string{"First Movie", "Second Movie", "Series"}, 3},
		{"&Limit=1&StartIndex=1", []string{"Series"}, 3},
		{"&IncludeItemTypes=Movie", []string{"First Movie", "Second Movie"}, 2},
		{"&IncludeItemTypes=Movie,Series", []string{"First Movie", "Second Movie", "Series"}, 3},
		{"&IncludeItemTypes=Episode", nil, 0},
		{"&IncludeItemTypes=Season", nil, 0},
		{"&ExcludeItemTypes=Movie,Series", nil, 0},
		{"&MediaTypes=Audio", nil, 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			result := performItemsRequest(t, h, "/Items?ParentId="+parentID+tc.query, session)
			if got := boxSetNames(result.Items); !slices.Equal(got, tc.want) || result.TotalRecordCount != tc.total {
				t.Fatalf("got %v (total %d), want %v (total %d)", got, result.TotalRecordCount, tc.want, tc.total)
			}
		})
	}
	smart, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: session.ProfileID, Name: "Episodes", CollectionType: "smart",
		IncludeInServerCollections: true, QueryDefinition: `{"media_scope":"episode"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.accessFilter = func(_ context.Context, userID int, profileID string) catalog.AccessFilter {
		return catalog.AccessFilter{UserID: userID, ProfileID: profileID, AllowedContentIDs: []string{episodeID}}
	}
	base, err := h.collectionResolver.Resolve(ctx, catalog.CatalogRequest{
		Source: catalog.CatalogSourceUserCollection, CollectionID: smart.ID, UseSourceOrder: true,
	}, h.resolveAccessFilter(ctx, session))
	if err != nil || len(base.Items) != 1 {
		t.Fatalf("episode fixture must resolve before any overlay: result=%+v err=%v", base, err)
	}
	parentID = h.codec.EncodeStringID(EncodedIDUserCollection, smart.ID)
	for _, query := range []string{"", "&IncludeItemTypes=Episode", "&SortBy=SortName", "&SortBy=ParentIndexNumber,IndexNumber"} {
		t.Run("smart episodes"+query, func(t *testing.T) {
			result := performItemsRequest(t, h, "/Items?ParentId="+parentID+query, session)
			if len(result.Items) != 1 || result.Items[0].Type != "Episode" || result.TotalRecordCount != 1 {
				t.Fatalf("smart episode collection lost its members: %+v", result)
			}
			// Clients label and link episodes by these fields.
			episode := result.Items[0]
			if episode.SeriesID == "" || episode.SeriesName == "" || episode.IndexNumber == nil || *episode.IndexNumber != 1 ||
				episode.ParentIndexNumber == nil || *episode.ParentIndexNumber != 1 {
				t.Fatalf("episode lost its series and index context: %+v", episode)
			}
		})
	}
	// Jellyfin browse filters narrow an episode-scoped collection on the
	// episode relation, before its page and total are taken. As in the compat
	// episode listing, Years is the air year and PersonIds the series' credits.
	if _, err := pool.Exec(ctx, `UPDATE episodes SET air_date = '2020-05-01' WHERE content_id = $1`, episodeID); err != nil {
		t.Fatal(err)
	}
	uncredited := time.Now().UnixNano()
	credited := uncredited + 1
	if _, err := pool.Exec(ctx, `INSERT INTO people (id, name) VALUES ($1, $2), ($3, $4)`, uncredited, "Uncredited "+uuid.NewString(), credited, "Credited "+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM people WHERE id = ANY($1)`, []int64{uncredited, credited}) })
	if _, err := pool.Exec(ctx, `INSERT INTO item_people (id, content_id, person_id, kind) VALUES ($1, (SELECT series_id FROM episodes WHERE content_id = $2), $1, 1)`, credited, episodeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM item_people WHERE id = $1`, credited) })
	episodeRouteID := performItemsRequest(t, h, "/Items?ParentId="+parentID, session).Items[0].ID
	for _, tc := range []struct {
		name, query string
		want        int
	}{
		{"matching year", "&Years=2020", 1},
		{"other year", "&Years=1900", 0},
		{"credited person", "&PersonIds=" + h.codec.EncodeIntID(EncodedIDPerson, credited), 1},
		{"uncredited person", "&PersonIds=" + h.codec.EncodeIntID(EncodedIDPerson, uncredited), 0},
		{"excluded episode", "&ExcludeItemIds=" + episodeRouteID, 0},
		{"absent audio language", "&AudioLanguages=fi", 0},
	} {
		for _, sortQuery := range []string{"", "&SortBy=SortName"} {
			t.Run("smart episodes "+tc.name+sortQuery, func(t *testing.T) {
				result := performItemsRequest(t, h, "/Items?ParentId="+parentID+tc.query+sortQuery, session)
				if len(result.Items) != tc.want || result.TotalRecordCount != tc.want {
					t.Fatalf("got %d items of %d, want %d", len(result.Items), result.TotalRecordCount, tc.want)
				}
			})
		}
	}
	for _, tc := range []struct {
		name     string
		backdrop string
		want     int
	}{
		{"inherited backdrop", "test/backdrop.jpg", 1},
		{"missing backdrop", "", 0},
		{"blank backdrop", "   ", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `UPDATE media_items SET backdrop_path = $1 WHERE content_id = (SELECT series_id FROM episodes WHERE content_id = $2)`, tc.backdrop, episodeID); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{"", "&SortBy=SortName"} {
				path := "/Items?ParentId=" + parentID + query
				unfiltered := performItemsRequest(t, h, path, session)
				if len(unfiltered.Items) != 1 || unfiltered.Items[0].Type != "Episode" {
					t.Fatal("episode must remain visible without the image filter")
				}
				filtered := performItemsRequest(t, h, path+"&ImageTypes=Backdrop&Limit=1", session)
				if len(filtered.Items) != tc.want || filtered.TotalRecordCount != tc.want {
					t.Errorf("%s: got %d items (total %d), want %d", query, len(filtered.Items), filtered.TotalRecordCount, tc.want)
				}
			}
		})
	}
}

func TestUserViews_HidesCollectionsViewWhenPersonalCollectionsBelongToOthers(t *testing.T) {
	otherUser := ownedUserCollection("u-2", "Someone Else's List")
	otherUser.userID = 2
	personal := &fakeUserCollectionSource{rows: []fakeUserCollection{otherUser}}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{}, personal,
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	result := performItemsRequest(t, h, "/Items")
	if len(result.Items) != 1 || result.Items[0].ID == collectionsViewID {
		t.Fatalf("expected only the library view, got %+v", result.Items)
	}
}

// TestPersonalBoxSetChildCountMatchesChildrenDB pins, against the real store
// and catalog resolver, that a personal BoxSet's ChildCount equals the
// TotalRecordCount of its children: members in hidden libraries, audiobooks
// and ebooks (never listed as BoxSet children) and rows for missing items
// count in neither.
func TestPersonalBoxSetChildCountMatchesChildrenDB(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	var userID, shownLib, hiddenLib int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, "boxset-count-"+suffix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	for i, target := range []*int{&shownLib, &hiddenLib} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
			fmt.Sprintf("boxset-count-%s-%d", suffix, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	shownA, shownB := "boxset-count-a-"+suffix, "boxset-count-b-"+suffix
	hidden, book, missing := "boxset-count-hidden-"+suffix, "boxset-count-book-"+suffix, "boxset-count-missing-"+suffix
	ebook := "boxset-count-ebook-" + suffix
	for _, seed := range []struct {
		id, kind string
		library  int
	}{{shownA, "movie", shownLib}, {shownB, "series", shownLib}, {hidden, "movie", hiddenLib}, {book, "audiobook", shownLib}, {ebook, "ebook", shownLib}} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title) VALUES ($1, $2, $1)`, seed.id, seed.kind); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, seed.id, seed.library); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_collection_revisions WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{shownA, shownB, hidden, book, ebook})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{shownLib, hiddenLib})
	})

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{StreamAppUserID: userID, ProfileID: uuid.NewString()}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: session.ProfileID, Name: "Test profile"}); err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: session.ProfileID, Name: "Hand Picked", CollectionType: "manual",
		IncludeInServerCollections: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, member := range []string{shownA, shownB, hidden, book, ebook, missing} {
		if err := store.AddCollectionItem(ctx, collection.ID, member, i); err != nil {
			t.Fatal(err)
		}
	}

	h := newCollectionsTestHandler(&fakeCollectionSource{}, []upstreamUserLibrary{{ID: shownLib, Name: "Movies", Type: "movies"}}, nil)
	h.userCollections = usercollections.NewStore(pool)
	h.collectionResolver = catalog.NewCatalogResolver(catalog.NewBrowseRepository(pool), catalog.NewItemRepository(pool)).
		WithUserStoreProvider(provider)
	h.accessFilter = func(_ context.Context, userID int, profileID string) catalog.AccessFilter {
		return catalog.AccessFilter{UserID: userID, ProfileID: profileID, AllowedLibraryIDs: []int{shownLib}}
	}

	listing := performItemsRequest(t, h, "/Items?IncludeItemTypes=BoxSet", session)
	if len(listing.Items) != 1 {
		t.Fatalf("expected the personal BoxSet, got %+v", listing.Items)
	}
	children := performItemsRequest(t, h, "/Items?ParentId="+listing.Items[0].ID, session)
	if children.TotalRecordCount != 2 || len(children.Items) != 2 {
		t.Fatalf("children: total=%d items=%d, want 2 visible video members", children.TotalRecordCount, len(children.Items))
	}
	if got := listing.Items[0].ChildCount; got != children.TotalRecordCount {
		t.Fatalf("ChildCount=%d, children TotalRecordCount=%d", got, children.TotalRecordCount)
	}
}

// pagingPersonalResolver serves a fixed member list in pages, as the catalog
// resolver does, and records each request.
type pagingPersonalResolver struct {
	items []*models.MediaItem
	reqs  []catalog.CatalogRequest
}

func (f *pagingPersonalResolver) Resolve(_ context.Context, req catalog.CatalogRequest, _ catalog.AccessFilter) (*catalog.CatalogResult, error) {
	f.reqs = append(f.reqs, req)
	start := min(req.Offset, len(f.items))
	end := len(f.items)
	if req.Limit > 0 {
		end = min(start+req.Limit, end)
	}
	return &catalog.CatalogResult{Items: f.items[start:end], Total: len(f.items)}, nil
}

// newPersonalPlayAllFixture exposes newPlayAllFixture's members [movie m-1,
// series s-1, movie m-2] as a personal collection, in the order the catalog
// resolver returns them.
func newPersonalPlayAllFixture(t *testing.T) (*ItemsHandler, *pagingPersonalResolver, string) {
	t.Helper()
	const collectionID = "731d3da2-4f4b-4a71-8f2f-38e1d34775b0"
	h, _ := newPlayAllFixture(t)
	h.userCollections = &fakeUserCollectionSource{rows: []fakeUserCollection{ownedUserCollection(collectionID, "Mine")}}
	resolver := &pagingPersonalResolver{items: []*models.MediaItem{
		{ContentID: "m-1", Type: "movie", Title: "Movie One"},
		{ContentID: "s-1", Type: "series", Title: "Show"},
		{ContentID: "m-2", Type: "movie", Title: "Movie Two"},
	}}
	h.collectionResolver = resolver
	return h, resolver, h.codec.EncodeStringID(EncodedIDUserCollection, collectionID)
}

// Play all and Shuffle on a personal BoxSet return its playable leaves, as for
// library collections: series expand to their playable episodes with season
// and series context, in the order the collection lists its members.
func TestHandleItems_PersonalBoxSetPlayAllExpandsSeries(t *testing.T) {
	h, resolver, parentID := newPersonalPlayAllFixture(t)
	result := performItemsRequest(t, h, "/Users/u/Items?ParentId="+parentID+
		"&Filters=IsNotFolder&Recursive=true&MediaTypes=Audio,Video&Fields=Chapters,MediaSources,Trickplay&Limit=300")
	assertNames(t, result.Items, "Movie One", "S1E1", "S1E2", "S2E1", "Special", "Movie Two")
	if result.TotalRecordCount != 6 {
		t.Fatalf("expected TotalRecordCount 6, got %d", result.TotalRecordCount)
	}
	seriesID := h.codec.EncodeStringID(EncodedIDItem, "s-1")
	for _, item := range result.Items {
		if item.IsFolder || item.ParentID == "" || len(item.MediaSources) != 1 {
			t.Fatalf("%q: expected a playable leaf under the BoxSet, got %+v", item.Name, item)
		}
		if item.Type == "Episode" && (item.SeriesID != seriesID || item.SeasonID == "") {
			t.Fatalf("%q: expected series and season context, got SeriesId=%q SeasonId=%q", item.Name, item.SeriesID, item.SeasonID)
		}
	}
	if got := resolver.reqs[0]; got.Source != catalog.CatalogSourceUserCollection || !got.UseSourceOrder || got.Randomize {
		t.Fatalf("members must come in the collection's listing order: %+v", got)
	}

	episodesOnly := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&Recursive=true&IncludeItemTypes=Episode")
	assertNames(t, episodesOnly.Items, "S1E1", "S1E2", "S2E1", "Special")

	shuffled := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&Filters=IsNotFolder&Recursive=true&SortBy=Random&Limit=300")
	if len(shuffled.Items) != 6 || shuffled.TotalRecordCount != 6 {
		t.Fatalf("expected 6 of 6 shuffled leaves, got %d of %d", len(shuffled.Items), shuffled.TotalRecordCount)
	}

	members := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&Recursive=true")
	assertNames(t, members.Items, "Movie One", "Show", "Movie Two")
}

// Play all reads every member in one resolver request, however large the
// collection, and pages the leaves itself.
func TestHandleItems_PersonalBoxSetPlayAllReadsWholeCollection(t *testing.T) {
	const members = 1202
	h, resolver, parentID := newPersonalPlayAllFixture(t)
	repo := h.itemRepo.(*fakeBatchItemRepo)
	resolver.items = nil
	for i := range members {
		id := fmt.Sprintf("m-%04d", i)
		item := &models.MediaItem{ContentID: id, Type: "movie", Title: id}
		resolver.items = append(resolver.items, item)
		repo.items[id] = item
	}
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&Filters=IsNotFolder&Recursive=true&StartIndex="+
		strconv.Itoa(members-2)+"&Limit=5")
	assertNames(t, result.Items, fmt.Sprintf("m-%04d", members-2), fmt.Sprintf("m-%04d", members-1))
	if result.TotalRecordCount != members {
		t.Fatalf("expected every member counted, got %d", result.TotalRecordCount)
	}
	if len(resolver.reqs) != 1 || resolver.reqs[0].Offset != 0 {
		t.Fatalf("expected one resolver request from the start, got %+v", resolver.reqs)
	}
}

// An owner-supplied remote poster is capped: a body over the limit answers 502
// instead of streaming, whether the server announces its length or not.
func TestPersonalCollectionPosterSizeLimit(t *testing.T) {
	size := maxPersonalPosterBytes + 1
	chunked := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		if !chunked {
			w.Header().Set("Content-Length", strconv.Itoa(size))
		}
		_, _ = w.Write(make([]byte, size))
	}))
	t.Cleanup(server.Close)
	previous := personalCollectionImageClient
	personalCollectionImageClient = server.Client()
	t.Cleanup(func() { personalCollectionImageClient = previous })

	codec := NewResourceIDCodec()
	row := ownedUserCollection(uuid.NewString(), "My Watchlist")
	row.PosterPath = server.URL + "/poster.jpg"
	routeID := codec.EncodeStringID(EncodedIDUserCollection, row.ID)
	h := &ImagesHandler{
		codec: codec, images: NewImageCache(time.Hour, time.Now),
		content:         &librariesContentService{},
		imageTags:       newImageTagSigner("image-secret"),
		userCollections: &fakeUserCollectionSource{rows: []fakeUserCollection{row}},
	}
	serve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary", nil)
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
		rec := httptest.NewRecorder()
		h.HandleItemImage(rec, withImageRouteParams(req, routeID, "Primary"))
		return rec
	}
	for _, tc := range []struct {
		name    string
		size    int
		chunked bool
		want    int
	}{
		{"at the limit", maxPersonalPosterBytes, false, http.StatusOK},
		{"announced over the limit", maxPersonalPosterBytes + 1, false, http.StatusBadGateway},
		{"streamed over the limit", maxPersonalPosterBytes + 1, true, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			size, chunked = tc.size, tc.chunked
			rec := serve()
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusOK && rec.Body.Len() != tc.size {
				t.Fatalf("body = %d bytes, want %d", rec.Body.Len(), tc.size)
			}
		})
	}
}

// An unfiltered page of a personal smart BoxSet sends no type rules, so it
// keeps paging in SQL; the non-video types stay out through the access filter.
// A stored collection still gets the video type rules, because its member
// reload does not apply that filter, and a client type filter always becomes
// exact rules.
func TestHandleItems_PersonalBoxSetTypeRules(t *testing.T) {
	const smartID, storedID = "731d3da2-4f4b-4a71-8f2f-38e1d34775b0", "5b0f0f6e-2c1f-4a8e-9d38-3c4f5a6b7c8d"
	smart := ownedUserCollection(smartID, "Smart")
	smart.CollectionType = "smart"
	resolver := &fakePersonalCollectionResolver{result: &catalog.CatalogResult{}}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{},
		&fakeUserCollectionSource{rows: []fakeUserCollection{smart, ownedUserCollection(storedID, "Stored")}},
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)
	h.collectionResolver = resolver
	codec := NewResourceIDCodec()
	smartParent := codec.EncodeStringID(EncodedIDUserCollection, smartID)
	storedParent := codec.EncodeStringID(EncodedIDUserCollection, storedID)
	typeRules := func(types ...string) []catalog.QueryGroup {
		rules := make([]catalog.QueryRule, 0, len(types))
		for _, itemType := range types {
			rules = append(rules, catalog.QueryRule{Field: "type", Op: "is", Value: itemType})
		}
		return []catalog.QueryGroup{{Match: "any", Rules: rules}}
	}

	for _, tc := range []struct {
		name, path string
		want       []catalog.QueryGroup
	}{
		{"smart page", "/Items?ParentId=" + smartParent + "&Limit=24", nil},
		{"smart play all", "/Items?ParentId=" + smartParent + "&Filters=IsNotFolder&Recursive=true", nil},
		{"stored page", "/Items?ParentId=" + storedParent + "&Limit=24", typeRules("movie", "series", "episode")},
		{"smart type filter", "/Items?ParentId=" + smartParent + "&IncludeItemTypes=Movie", typeRules("movie")},
		{"stored type filter", "/Items?ParentId=" + storedParent + "&IncludeItemTypes=Movie", typeRules("movie")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			performItemsRequest(t, h, tc.path)
			if got := resolver.gotReq.Query.Groups; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("type rules = %+v, want %+v", got, tc.want)
			}
			for _, excluded := range []string{"audiobook", "podcast", "ebook", "manga"} {
				if !slices.Contains(resolver.gotAccess.ExcludedMediaTypes, excluded) {
					t.Errorf("access does not exclude %s: %+v", excluded, resolver.gotAccess.ExcludedMediaTypes)
				}
			}
		})
	}
}

// rowCountTracer records how many rows the largest SELECT returned.
type rowCountTracer struct{ largest atomic.Int64 }

func (*rowCountTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (t *rowCountTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.CommandTag.Select() {
		for rows := data.CommandTag.RowsAffected(); ; {
			current := t.largest.Load()
			if rows <= current || t.largest.CompareAndSwap(current, rows) {
				break
			}
		}
	}
}

// TestPersonalSmartBoxSetPageStaysPagedDB pins that one page of a large
// personal smart collection reads about a page of rows, not the whole
// membership, as the native catalog route does.
func TestPersonalSmartBoxSetPageStaysPagedDB(t *testing.T) {
	const members, pageSize = 60, 5
	base := newCompatTestPool(t)
	ctx := context.Background()
	config := base.Config().Copy()
	tracer := &rowCountTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := uuid.NewString()
	var userID, library int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, "boxset-page-"+suffix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, "boxset-page-"+suffix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, members)
	for i := range members {
		id := fmt.Sprintf("boxset-page-%s-%03d", suffix, i)
		ids = append(ids, id)
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $1)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = base.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = base.Exec(ctx, `DELETE FROM user_collection_revisions WHERE user_id = $1`, userID)
		_, _ = base.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		_, _ = base.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, library)
	})

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{StreamAppUserID: userID, ProfileID: uuid.NewString()}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: session.ProfileID, Name: "Test profile"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: session.ProfileID, Name: "Every movie", CollectionType: "smart",
		QueryDefinition:            fmt.Sprintf(`{"media_scope":"movie","library_ids":[%d]}`, library),
		IncludeInServerCollections: true,
	}); err != nil {
		t.Fatal(err)
	}

	h := newCollectionsTestHandler(&fakeCollectionSource{}, []upstreamUserLibrary{{ID: library, Name: "Movies", Type: "movies"}}, nil)
	h.userCollections = usercollections.NewStore(pool)
	h.collectionResolver = catalog.NewCatalogResolver(catalog.NewBrowseRepository(pool), catalog.NewItemRepository(pool)).
		WithUserStoreProvider(provider)
	h.accessFilter = func(_ context.Context, userID int, profileID string) catalog.AccessFilter {
		return catalog.AccessFilter{UserID: userID, ProfileID: profileID, AllowedLibraryIDs: []int{library}}
	}
	listing := performItemsRequest(t, h, "/Items?IncludeItemTypes=BoxSet", session)
	if len(listing.Items) != 1 {
		t.Fatalf("expected the personal BoxSet, got %+v", listing.Items)
	}

	tracer.largest.Store(0)
	page := performItemsRequest(t, h, "/Items?ParentId="+listing.Items[0].ID+"&Limit="+strconv.Itoa(pageSize), session)
	if len(page.Items) != pageSize || page.TotalRecordCount != members {
		t.Fatalf("page: %d items of %d, want %d of %d", len(page.Items), page.TotalRecordCount, pageSize, members)
	}
	if largest := tracer.largest.Load(); largest > 2*pageSize {
		t.Fatalf("one page read a %d-row SELECT; want at most %d", largest, 2*pageSize)
	}
}

// A shared personal collection whose owner cannot be resolved fails closed on
// the BoxSet surface, as on the native routes: listings and Ids re-hydration
// leave it out, and its detail route errors instead of describing it under the
// viewer's access alone.
func TestPersonalBoxSetUnresolvableOwnerFailsClosed(t *testing.T) {
	const sharedID = "731d3da2-4f4b-4a71-8f2f-38e1d34775b0"
	shared := ownedUserCollection(sharedID, "Shared")
	shared.CreatorProfileID, shared.shared = "profile-2", true
	personal := &fakeUserCollectionSource{
		rows:        []fakeUserCollection{shared, ownedUserCollection("u-2", "Mine")},
		unavailable: map[string]bool{sharedID: true},
	}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{}, personal,
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	listing := performItemsRequest(t, h, "/Items?ParentId="+collectionsViewID)
	if got := boxSetNames(listing.Items); !slices.Equal(got, []string{"Mine"}) || listing.TotalRecordCount != 1 {
		t.Fatalf("listing = %v (total %d), want only Mine", got, listing.TotalRecordCount)
	}
	if personal.gotCountViewer != "profile-1" {
		t.Fatalf("counted for viewer %q, want the session profile", personal.gotCountViewer)
	}
	routeID := NewResourceIDCodec().EncodeStringID(EncodedIDUserCollection, sharedID)
	if ids := performItemsRequest(t, h, "/Items?Ids="+routeID); len(ids.Items) != 0 {
		t.Fatalf("Ids re-hydrated a collection whose owner is unresolved: %+v", ids.Items)
	}
	if rec := requestBoxSetItem(t, h, sharedID); rec.Code == http.StatusOK {
		t.Fatalf("detail served a collection whose owner is unresolved: %s", rec.Body.String())
	}
}

// fixedOwnerAccess limits every owner to the given libraries.
type fixedOwnerAccess struct{ libraries []int }

func (f fixedOwnerAccess) OwnerFilter(context.Context, int, string) (catalog.AccessFilter, error) {
	return catalog.AccessFilter{AllowedLibraryIDs: f.libraries}, nil
}

// TestPersonalBoxSetOwnerLimitDB pins that a personal BoxSet shared with
// another profile shows that profile only the titles the owner can access too,
// in ChildCount and in its children, while the owner still sees every member.
func TestPersonalBoxSetOwnerLimitDB(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	var userID, ownerLib, otherLib int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, "boxset-owner-"+suffix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	for i, target := range []*int{&ownerLib, &otherLib} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
			fmt.Sprintf("boxset-owner-%s-%d", suffix, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	shared, private := "boxset-owner-shared-"+suffix, "boxset-owner-private-"+suffix
	for _, seed := range []struct {
		id      string
		library int
	}{{shared, ownerLib}, {private, otherLib}} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $1)`, seed.id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, seed.id, seed.library); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_collection_revisions WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{shared, private})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{ownerLib, otherLib})
	})

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	owner := &Session{StreamAppUserID: userID, ProfileID: uuid.NewString()}
	viewer := &Session{StreamAppUserID: userID, ProfileID: uuid.NewString()}
	for _, s := range []*Session{owner, viewer} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: s.ProfileID, Name: "Profile " + s.ProfileID}); err != nil {
			t.Fatal(err)
		}
	}
	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: owner.ProfileID, Name: "Shared picks", CollectionType: "manual", IsShared: true,
		IncludeInServerCollections: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, member := range []string{shared, private} {
		if err := store.AddCollectionItem(ctx, collection.ID, member, i); err != nil {
			t.Fatal(err)
		}
	}

	owners := fixedOwnerAccess{libraries: []int{ownerLib}}
	h := newCollectionsTestHandler(&fakeCollectionSource{}, []upstreamUserLibrary{{ID: ownerLib, Name: "A", Type: "movies"}, {ID: otherLib, Name: "B", Type: "movies"}}, nil)
	h.userCollections = usercollections.NewStore(pool)
	h.collectionOwners = owners
	h.collectionResolver = catalog.NewCatalogResolver(catalog.NewBrowseRepository(pool), catalog.NewItemRepository(pool)).
		WithUserStoreProvider(provider).WithPersonalCollectionAccess(owners)
	h.accessFilter = func(_ context.Context, userID int, profileID string) catalog.AccessFilter {
		return catalog.AccessFilter{UserID: userID, ProfileID: profileID, AllowedLibraryIDs: []int{ownerLib, otherLib}}
	}

	for _, tc := range []struct {
		name    string
		session *Session
		want    int
	}{
		{"viewer sees only the owner's titles", viewer, 1},
		{"owner sees every member", owner, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listing := performItemsRequest(t, h, "/Items?IncludeItemTypes=BoxSet", tc.session)
			if len(listing.Items) != 1 {
				t.Fatalf("expected the shared BoxSet, got %+v", listing.Items)
			}
			children := performItemsRequest(t, h, "/Items?ParentId="+listing.Items[0].ID, tc.session)
			if listing.Items[0].ChildCount != tc.want || children.TotalRecordCount != tc.want || len(children.Items) != tc.want {
				t.Fatalf("ChildCount=%d children=%d of %d, want %d", listing.Items[0].ChildCount, len(children.Items), children.TotalRecordCount, tc.want)
			}
		})
	}
}

// TestPersonalBoxSetLanguageFiltersStayInVisibleLibrariesDB pins that a
// language filter on a personal BoxSet matches only files in libraries the
// viewer can access, also when the viewer can access a single library: a
// French version in a hidden library must not make the movie match.
func TestPersonalBoxSetLanguageFiltersStayInVisibleLibrariesDB(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	var userID, visibleLib, hiddenLib int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, "boxset-lang-"+suffix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	for i, target := range []*int{&visibleLib, &hiddenLib} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
			fmt.Sprintf("boxset-lang-%s-%d", suffix, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	movie := "boxset-lang-movie-" + suffix
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $1)`, movie)
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2), ($1, $3)`, movie, visibleLib, hiddenLib)
	exec(`INSERT INTO media_files (content_id, media_folder_id, file_path, audio_language_codes, subtitle_language_codes) VALUES ($1, $2, $1 || '-en.mkv', '{en}', '{en}'), ($1, $3, $1 || '-fr.mkv', '{fr}', '{fr}')`, movie, visibleLib, hiddenLib)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_collection_revisions WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE content_id = $1`, movie)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, movie)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{visibleLib, hiddenLib})
	})

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{StreamAppUserID: userID, ProfileID: uuid.NewString()}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: session.ProfileID, Name: "Test profile"}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: session.ProfileID, Name: "Stored", CollectionType: "manual",
		IncludeInServerCollections: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddCollectionItem(ctx, stored.ID, movie, 0); err != nil {
		t.Fatal(err)
	}
	smart, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: session.ProfileID, Name: "Smart", CollectionType: "smart",
		QueryDefinition:            fmt.Sprintf(`{"media_scope":"movie","library_ids":[%d,%d]}`, visibleLib, hiddenLib),
		IncludeInServerCollections: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	h := newCollectionsTestHandler(&fakeCollectionSource{}, []upstreamUserLibrary{{ID: visibleLib, Name: "Movies", Type: "movies"}}, nil)
	h.userCollections = usercollections.NewStore(pool)
	h.collectionResolver = catalog.NewCatalogResolver(catalog.NewBrowseRepository(pool), catalog.NewItemRepository(pool)).
		WithUserStoreProvider(provider)
	h.accessFilter = func(_ context.Context, userID int, profileID string) catalog.AccessFilter {
		return catalog.AccessFilter{UserID: userID, ProfileID: profileID, AllowedLibraryIDs: []int{visibleLib}}
	}
	for _, collectionID := range []string{stored.ID, smart.ID} {
		parentID := h.codec.EncodeStringID(EncodedIDUserCollection, collectionID)
		for _, tc := range []struct {
			query string
			want  int
		}{
			{"&AudioLanguages=en", 1},
			{"&AudioLanguages=fr", 0},
			{"&SubtitleLanguages=en", 1},
			{"&SubtitleLanguages=fr", 0},
		} {
			for _, sortQuery := range []string{"", "&SortBy=SortName"} {
				t.Run(collectionID+tc.query+sortQuery, func(t *testing.T) {
					result := performItemsRequest(t, h, "/Items?ParentId="+parentID+tc.query+sortQuery, session)
					if len(result.Items) != tc.want || result.TotalRecordCount != tc.want {
						t.Fatalf("got %d items of %d, want %d", len(result.Items), result.TotalRecordCount, tc.want)
					}
				})
			}
		}
	}
}

// A request naming a personal BoxSet by Ids still returns it when the client
// adds filters or a sort, as it does for a library BoxSet.
func TestHandleItems_PersonalBoxSetIdsWithFilters(t *testing.T) {
	const collectionID = "731d3da2-4f4b-4a71-8f2f-38e1d34775b0"
	h := newUserCollectionsTestHandler(&fakeCollectionSource{},
		&fakeUserCollectionSource{rows: []fakeUserCollection{ownedUserCollection(collectionID, "Mine")}},
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)
	routeID := NewResourceIDCodec().EncodeStringID(EncodedIDUserCollection, collectionID)
	for _, extra := range []string{"&IncludeItemTypes=BoxSet", "&SortBy=SortName"} {
		t.Run(extra, func(t *testing.T) {
			result := performItemsRequest(t, h, "/Items?Ids="+routeID+extra)
			if len(result.Items) != 1 || result.Items[0].ID != routeID || result.Items[0].Type != "BoxSet" {
				t.Fatalf("personal BoxSet missing from Ids response: %+v", result.Items)
			}
		})
	}
}

// Episode-order sort keys map to no sort, so a personal BoxSet lists its
// members in its own order instead of rejecting the request.
func TestHandleItems_PersonalBoxSetEpisodeOrderSort(t *testing.T) {
	const collectionID = "731d3da2-4f4b-4a71-8f2f-38e1d34775b0"
	resolver := &fakePersonalCollectionResolver{result: &catalog.CatalogResult{}}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{},
		&fakeUserCollectionSource{rows: []fakeUserCollection{ownedUserCollection(collectionID, "Mine")}},
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)
	h.collectionResolver = resolver
	parentID := NewResourceIDCodec().EncodeStringID(EncodedIDUserCollection, collectionID)
	for _, sortBy := range []string{"IndexNumber", "ParentIndexNumber,IndexNumber", "AiredEpisodeOrder"} {
		t.Run(sortBy, func(t *testing.T) {
			performItemsRequest(t, h, "/Items?ParentId="+parentID+"&SortBy="+sortBy)
			if !resolver.gotReq.UseSourceOrder || resolver.gotReq.Query.Sort != (catalog.QuerySort{}) {
				t.Fatalf("episode-order sort did not keep the collection order: %+v", resolver.gotReq)
			}
		})
	}
}
