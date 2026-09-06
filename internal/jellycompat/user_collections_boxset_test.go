package jellycompat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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
// ownership and sharing facts the real store enforces in SQL.
type fakeUserCollection struct {
	usercollections.ServerVisibleCollection
	userID     int
	profileIDs []string
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
	return row.userID == userID && slices.Contains(row.profileIDs, profileID)
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
		userID:     1,
		profileIDs: []string{"profile-1"},
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
			if item.Type != "BoxSet" || !item.IsFolder || item.ChildCount != 0 {
				t.Fatalf("unexpected personal BoxSet DTO: %+v", item)
			}
		case "Studio Picks":
			if item.ChildCount != 3 {
				t.Fatalf("library collection lost its stored count: %+v", item)
			}
		}
	}
}

// TestHandleItems_BoxSetListingHidesOtherProfilesPersonalCollections is the
// privacy pin: personal collections are private, so neither another user's rows
// nor rows their owner shared with a different profile may reach this session.
func TestHandleItems_BoxSetListingHidesOtherProfilesPersonalCollections(t *testing.T) {
	otherUser := ownedUserCollection("u-2", "Someone Else's List")
	otherUser.userID = 2
	otherProfile := ownedUserCollection("u-3", "Not My Profile")
	otherProfile.profileIDs = []string{"profile-2"}
	personal := &fakeUserCollectionSource{rows: []fakeUserCollection{otherUser, otherProfile}}
	h := newUserCollectionsTestHandler(&fakeCollectionSource{}, personal,
		[]upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	result := performItemsRequest(t, h, "/Items?ParentId="+collectionsViewID)
	if len(result.Items) != 0 {
		t.Fatalf("expected no collections, got %v", boxSetNames(result.Items))
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
	hidden.profileIDs = []string{"profile-2"}
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
	performItemsRequest(t, h, "/Items?ParentId="+parentID+"&SortBy=Random&PersonIds="+personID+"&ImageTypes=Backdrop")
	if !resolver.gotReq.Randomize || !resolver.gotReq.UseSourceOrder || resolver.gotReq.Query.Sort != (catalog.QuerySort{}) {
		t.Fatalf("random sort was not preserved: %+v", resolver.gotReq)
	}
	if resolver.gotReq.PersonID != 42 || !resolver.gotReq.RequireBackdrop {
		t.Fatalf("existing Jellyfin filters were not forwarded: %+v", resolver.gotReq)
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

func TestPersonalBoxSetABSCollectionSurvivesFreshCodec(t *testing.T) {
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
	optIn := true
	profiles := []string{session.ProfileID}
	if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
		ID: id, RequestProfileID: session.ProfileID, IncludeInServerCollections: &optIn, AllowedProfileIDs: &profiles,
	}); err != nil {
		t.Fatal(err)
	}
	source := usercollections.NewStore(pool)
	h := newCollectionsTestHandler(&fakeCollectionSource{}, nil, nil)
	h.userCollections = source
	h.mapper.imageTagSigner = newImageTagSigner("image-secret")
	listing := performItemsRequest(t, h, "/Items?IncludeItemTypes=BoxSet", session)
	if len(listing.Items) != 1 || listing.Items[0].Name != "Test collection" {
		t.Fatalf("ABS collection not listed: %+v", listing)
	}
	routeID, tag := listing.Items[0].ID, listing.Items[0].ImageTags["Primary"]
	if tag == "" {
		t.Fatal("listed collection has no signed artwork tag")
	}
	resolver := &fakePersonalCollectionResolver{result: &catalog.CatalogResult{
		Items: []*models.MediaItem{{ContentID: "movie-tmdb-123", Type: "movie", Title: "Test movie"}}, Total: 1,
	}}
	h.collectionResolver = resolver
	for _, raw := range []string{routeID, strings.ReplaceAll(routeID, "-", ""), strings.ToUpper(routeID)} {
		t.Run(raw, func(t *testing.T) {
			for _, path := range []string{"/Items?Ids=" + raw, "/Items?ParentId=" + raw} {
				h.codec = NewResourceIDCodec()
				result := performItemsRequest(t, h, path, session)
				if len(result.Items) != 1 {
					t.Fatalf("cold %s: %+v", path, result)
				}
				if strings.Contains(path, "?Ids=") && (result.Items[0].ID != routeID || result.Items[0].Type != "BoxSet") {
					t.Fatalf("cold Ids resolved the wrong item: %+v", result.Items[0])
				}
				if strings.Contains(path, "?ParentId=") && (result.Items[0].Name != "Test movie" || result.Items[0].ParentID != routeID) {
					t.Fatalf("cold ParentId resolved the wrong children: %+v", result.Items[0])
				}
			}
			if resolver.gotReq.CollectionID != id {
				t.Fatalf("resolver received %q, want original ULID %q", resolver.gotReq.CollectionID, id)
			}
			for _, viewer := range []*Session{session, {StreamAppUserID: userID, ProfileID: "unshared"}, {StreamAppUserID: userID + 10000, ProfileID: session.ProfileID}} {
				h.codec = NewResourceIDCodec()
				req := httptest.NewRequest(http.MethodGet, "/Items/"+raw, nil)
				rctx := chi.NewRouteContext()
				rctx.URLParams.Add("id", raw)
				req = req.WithContext(context.WithValue(context.WithValue(req.Context(), compatSessionKey, viewer), chi.RouteCtxKey, rctx))
				rec := httptest.NewRecorder()
				h.HandleItem(rec, req)
				want := http.StatusNotFound
				if viewer == session {
					want = http.StatusOK
				}
				if rec.Code != want {
					t.Fatalf("cold detail: status=%d, want=%d, body=%s", rec.Code, want, rec.Body.String())
				}
			}
			for _, imageTag := range []string{tag, "0123456789abcdef", ""} {
				images := &ImagesHandler{codec: NewResourceIDCodec(), userCollections: source, imageTags: h.mapper.imageTagSigner}
				req := httptest.NewRequest(http.MethodGet, "/Items/"+raw+"/Images/Primary?tag="+imageTag, nil)
				rec := httptest.NewRecorder()
				images.HandleItemImage(rec, withImageRouteParams(req, raw, "Primary"))
				want := http.StatusNotFound
				if imageTag == tag {
					want = http.StatusOK
				}
				if rec.Code != want {
					t.Fatalf("cold signed image: status=%d, want=%d, body=%s", rec.Code, want, rec.Body.String())
				}
			}
		})
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
	for _, query := range []string{"", "&IncludeItemTypes=Episode", "&SortBy=SortName"} {
		t.Run("smart episodes"+query, func(t *testing.T) {
			result := performItemsRequest(t, h, "/Items?ParentId="+parentID+query, session)
			if len(result.Items) != 1 || result.Items[0].Type != "Episode" || result.TotalRecordCount != 1 {
				t.Fatalf("smart episode collection lost its members: %+v", result)
			}
		})
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
