package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakePersonalLists keeps one profile's favorites in order, newest first,
// and resolves a card for every entry the catalog "has" (the entries whose
// id is not in missing).
type fakePersonalLists struct {
	favorites []userstore.Favorite
	missing   map[string]bool
	err       error
	viewers   []handlers.PersonalListViewer
	limits    []int
}

func (f *fakePersonalLists) ListFavorites(_ context.Context, viewer handlers.PersonalListViewer, limit, offset int) ([]userstore.Favorite, []handlers.CollectionItemView, error) {
	f.viewers = append(f.viewers, viewer)
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, nil, f.err
	}
	var page []userstore.Favorite
	for i := offset; i < len(f.favorites) && len(page) < limit; i++ {
		if f.favorites[i].ProfileID == viewer.ProfileID {
			page = append(page, f.favorites[i])
		}
	}
	cards := make([]handlers.CollectionItemView, 0, len(page))
	for _, e := range page {
		if f.missing[e.MediaItemID] {
			continue
		}
		cards = append(cards, handlers.CollectionItemView{ContentID: e.MediaItemID, Type: "movie", Title: "Title " + e.MediaItemID, Status: "matched"})
	}
	return page, cards, nil
}

func (f *fakePersonalLists) GetFavorite(_ context.Context, viewer handlers.PersonalListViewer, itemID string) (userstore.Favorite, bool, error) {
	if f.err != nil {
		return userstore.Favorite{}, false, f.err
	}
	if f.missing[itemID] {
		return userstore.Favorite{}, false, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
	}
	for _, e := range f.favorites {
		if e.ProfileID == viewer.ProfileID && e.MediaItemID == itemID {
			return e, true, nil
		}
	}
	return userstore.Favorite{}, false, nil
}

func (f *fakePersonalLists) AddFavorite(_ context.Context, viewer handlers.PersonalListViewer, itemID string) error {
	if f.err != nil {
		return f.err
	}
	if f.missing[itemID] {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
	}
	for _, e := range f.favorites {
		if e.ProfileID == viewer.ProfileID && e.MediaItemID == itemID {
			return nil
		}
	}
	f.favorites = append([]userstore.Favorite{{ProfileID: viewer.ProfileID, MediaItemID: itemID, AddedAt: "2026-01-03T00:00:00Z"}}, f.favorites...)
	return nil
}

func (f *fakePersonalLists) RemoveFavorite(_ context.Context, viewer handlers.PersonalListViewer, itemID string) error {
	if f.err != nil {
		return f.err
	}
	kept := f.favorites[:0]
	for _, e := range f.favorites {
		if e.ProfileID != viewer.ProfileID || e.MediaItemID != itemID {
			kept = append(kept, e)
		}
	}
	f.favorites = kept
	return nil
}

func favoriteRows() []userstore.Favorite {
	return []userstore.Favorite{
		{ProfileID: "p-owner", MediaItemID: "movie:c", AddedAt: "2026-01-02T03:04:05Z"},
		{ProfileID: "p-owner", MediaItemID: "movie:b", AddedAt: "2026-01-01T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "movie:a", AddedAt: "2025-12-31T00:00:00Z"},
		{ProfileID: "p-other", MediaItemID: "movie:z", AddedAt: "2025-12-30T00:00:00Z"},
	}
}

type cardPage struct {
	Items []struct {
		ContentID string `json:"content_id"`
	} `json:"items"`
	Page struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
}

func decodeCards(t *testing.T, body string) cardPage {
	t.Helper()
	var page cardPage
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	return page
}

// listCardIDs walks every page of the listing and returns the ids in order.
func listCardIDs(t *testing.T, h http.Handler, path string, headers map[string]string) ([]string, int) {
	t.Helper()
	var ids []string
	cursor, pages := "", 0
	for {
		url := path
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		rec := do(t, h, http.MethodGet, url, "", headers)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", url, rec.Code, rec.Body.String())
		}
		page := decodeCards(t, rec.Body.String())
		pages++
		for _, it := range page.Items {
			ids = append(ids, it.ContentID)
		}
		if !page.Page.HasMore {
			return ids, pages
		}
		cursor = page.Page.NextCursor
	}
}

func favoritesDeps(lists *fakePersonalLists) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.PersonalLists = lists
	return deps
}

func TestListFavorites(t *testing.T) {
	lists := &fakePersonalLists{favorites: favoriteRows()}
	h := newTestHandler(t, favoritesDeps(lists))

	rec := do(t, h, http.MethodGet, "/api/v2/favorites", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	page := decodeCards(t, rec.Body.String())
	if len(page.Items) != 3 || page.Items[0].ContentID != "movie:c" || page.Page.HasMore || page.Page.NextCursor != "" {
		t.Fatalf("page = %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"title":"Title movie:c"`) || !strings.Contains(rec.Body.String(), `"genres":[]`) {
		t.Fatalf("card = %s", rec.Body.String())
	}
	// The seam is probed for one row past the page and sees the viewer's
	// profile and the requested artwork size.
	if lists.limits[0] != DefaultLimit+1 || lists.viewers[0].ProfileID != "p-owner" || lists.viewers[0].UserID != 1 {
		t.Fatalf("call = %d %+v", lists.limits[0], lists.viewers[0])
	}
	rec = do(t, h, http.MethodGet, "/api/v2/favorites?image_size=large", "", viewerHeaders())
	if rec.Code != 200 || lists.viewers[1].ImageSize != imagesize.Large {
		t.Fatalf("%d image size = %v", rec.Code, lists.viewers[1].ImageSize)
	}
}

func TestListFavoritesCursor(t *testing.T) {
	lists := &fakePersonalLists{favorites: favoriteRows()}
	h := newTestHandler(t, favoritesDeps(lists))
	ids, pages := listCardIDs(t, h, "/api/v2/favorites?limit=2", viewerHeaders())
	if pages != 2 || strings.Join(ids, ",") != "movie:c,movie:b,movie:a" {
		t.Fatalf("ids = %v pages = %d", ids, pages)
	}
	// An entry without a card still counts toward has_more: the page is one
	// card short but the next page follows.
	lists.missing = map[string]bool{"movie:b": true}
	ids, pages = listCardIDs(t, h, "/api/v2/favorites?limit=2", viewerHeaders())
	if pages != 2 || strings.Join(ids, ",") != "movie:c,movie:a" {
		t.Fatalf("ids = %v pages = %d", ids, pages)
	}
	// The probe row's own card never leaks into the page.
	lists.missing = map[string]bool{"movie:c": true}
	rec := do(t, h, http.MethodGet, "/api/v2/favorites?limit=1", "", viewerHeaders())
	page := decodeCards(t, rec.Body.String())
	if len(page.Items) != 0 || !page.Page.HasMore {
		t.Fatalf("page = %s", rec.Body.String())
	}
	// A cursor of another operation is refused.
	other := NewCursors(nil)
	foreign, _ := other.Encode(CursorScope{OperationID: opListProgress}, offsetPosition{Offset: 1})
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/favorites?cursor="+foreign, "", viewerHeaders()), TypeInvalidCursor)
}

func TestListFavoritesValidation(t *testing.T) {
	h := newTestHandler(t, favoritesDeps(&fakePersonalLists{}))
	for _, tc := range []struct{ query, location, code string }{
		{"offset=10", "query.offset", codeUnknownParameter},
		{"limit=0", "query.limit", codeOutOfRange},
		{"image_size=huge", "query.image_size", codeInvalidEnum},
	} {
		p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/favorites?"+tc.query, "", viewerHeaders()), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || p.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.query, p.Errors)
		}
	}
}

func TestGetFavorite(t *testing.T) {
	lists := &fakePersonalLists{favorites: favoriteRows(), missing: map[string]bool{"movie:hidden": true}}
	h := newTestHandler(t, favoritesDeps(lists))
	rec := do(t, h, http.MethodGet, "/api/v2/favorites/movie:c", "", viewerHeaders())
	if rec.Code != 200 || rec.Body.String() != `{"item_id":"movie:c","added_at":"2026-01-02T03:04:05.000Z"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Not a favorite, and an item the viewer may not see, are both 404.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/favorites/movie:nope", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/favorites/movie:hidden", "", viewerHeaders()), TypeNotFound)
	// Another profile's favorite is not this profile's.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/favorites/movie:z", "", viewerHeaders()), TypeNotFound)
}

func TestAddAndDeleteFavorite(t *testing.T) {
	lists := &fakePersonalLists{favorites: favoriteRows(), missing: map[string]bool{"movie:hidden": true}}
	h := newTestHandler(t, favoritesDeps(lists))
	for i := 0; i < 2; i++ {
		rec := do(t, h, http.MethodPut, "/api/v2/favorites/movie:new", "", viewerHeaders())
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("put %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if len(lists.favorites) != 5 || lists.favorites[0].MediaItemID != "movie:new" {
		t.Fatalf("favorites = %+v", lists.favorites)
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/favorites/movie:hidden", "", viewerHeaders()), TypeNotFound)
	for i := 0; i < 2; i++ {
		rec := do(t, h, http.MethodDelete, "/api/v2/favorites/movie:new", "", viewerHeaders())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if len(lists.favorites) != 4 {
		t.Fatalf("favorites = %+v", lists.favorites)
	}
	// Demo mode refuses the mutations to a non-admin.
	demo := favoritesDeps(lists)
	demo.DemoSettings = fakeSettings{demo: true}
	dh := newTestHandler(t, demo)
	requireProblem(t, do(t, dh, http.MethodPut, "/api/v2/favorites/movie:new", "", viewerHeaders()), TypePermissionDenied)
	requireProblem(t, do(t, dh, http.MethodDelete, "/api/v2/favorites/movie:c", "", viewerHeaders()), TypePermissionDenied)
}

func TestFavoritesDenied(t *testing.T) {
	h := newTestHandler(t, favoritesDeps(&fakePersonalLists{err: errStore}))
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v2/favorites"}, {http.MethodGet, "/api/v2/favorites/movie:c"},
		{http.MethodPut, "/api/v2/favorites/movie:c"}, {http.MethodDelete, "/api/v2/favorites/movie:c"},
	} {
		requireProblem(t, do(t, h, tc.method, tc.path, "", nil), TypeAuthenticationRequired)
		p := requireProblem(t, do(t, h, tc.method, tc.path, "", bearer(memberToken)), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != locationProfileHeader {
			t.Fatalf("%s %s: errors = %+v", tc.method, tc.path, p.Errors)
		}
		requireProblem(t, do(t, h, tc.method, tc.path, "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
		requireProblem(t, do(t, h, tc.method, tc.path, "", with(bearer(memberToken), "X-Profile-Id", "p-unknown")), TypeNotFound)
		// A store failure is the v1 decision (500) as a problem.
		requireProblem(t, do(t, h, tc.method, tc.path, "", viewerHeaders()), TypeInternalError)
	}
	// Without the service the operations fail closed.
	deps := pilotDeps(nil, nil)
	deps.PersonalLists = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/favorites", "", viewerHeaders()), TypeDependencyUnavailable)
}

// The v1 handler is the service the fake stands in for.
var _ PersonalListService = (*handlers.PersonalDataHandler)(nil)
