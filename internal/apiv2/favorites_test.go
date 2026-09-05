package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
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
	watchlist []userstore.WatchlistEntry
	missing   map[string]bool
	err       error
	viewers   []handlers.PersonalListViewer
	limits    []int
}

// listKeyAfter reports whether the row's key sorts strictly after the
// cursor key in (added_at DESC, media_item_id DESC) order, i.e. is smaller.
func listKeyAfter(row, after userstore.ListKey) bool {
	if row.AddedAt != after.AddedAt {
		return row.AddedAt < after.AddedAt
	}
	return row.MediaItemID < after.MediaItemID
}

// keysetPage models the store's ORDER BY added_at DESC, media_item_id DESC
// LIMIT over rows of one profile, resuming strictly after the key.
func keysetPage[E any](rows []E, profileID string, keyOf func(E) userstore.ListKey, rowProfile func(E) string, after *userstore.ListKey, limit int) []E {
	var matches []E
	for _, e := range rows {
		if rowProfile(e) != profileID {
			continue
		}
		if after != nil && !listKeyAfter(keyOf(e), *after) {
			continue
		}
		matches = append(matches, e)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return listKeyAfter(keyOf(matches[j]), keyOf(matches[i]))
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func (f *fakePersonalLists) ListFavoritesPage(_ context.Context, viewer handlers.PersonalListViewer, after *userstore.ListKey, limit int) ([]userstore.Favorite, []handlers.CollectionItemView, error) {
	f.viewers = append(f.viewers, viewer)
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, nil, f.err
	}
	page := keysetPage(f.favorites, viewer.ProfileID,
		func(e userstore.Favorite) userstore.ListKey {
			return userstore.ListKey{AddedAt: e.AddedAt, MediaItemID: e.MediaItemID}
		},
		func(e userstore.Favorite) string { return e.ProfileID }, after, limit)
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

func (f *fakePersonalLists) ListWatchlistPage(_ context.Context, viewer handlers.PersonalListViewer, after *userstore.ListKey, limit int) ([]userstore.WatchlistEntry, []handlers.CollectionItemView, error) {
	f.viewers = append(f.viewers, viewer)
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, nil, f.err
	}
	page := keysetPage(f.watchlist, viewer.ProfileID,
		func(e userstore.WatchlistEntry) userstore.ListKey {
			return userstore.ListKey{AddedAt: e.AddedAt, MediaItemID: e.MediaItemID}
		},
		func(e userstore.WatchlistEntry) string { return e.ProfileID }, after, limit)
	cards := make([]handlers.CollectionItemView, 0, len(page))
	for _, e := range page {
		if f.missing[e.MediaItemID] {
			continue
		}
		cards = append(cards, handlers.CollectionItemView{ContentID: e.MediaItemID, Type: "series", Title: "Title " + e.MediaItemID, Status: "matched"})
	}
	return page, cards, nil
}

func (f *fakePersonalLists) GetWatchlistEntry(_ context.Context, viewer handlers.PersonalListViewer, itemID string) (userstore.WatchlistEntry, bool, error) {
	if f.err != nil {
		return userstore.WatchlistEntry{}, false, f.err
	}
	if f.missing[itemID] {
		return userstore.WatchlistEntry{}, false, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
	}
	for _, e := range f.watchlist {
		if e.ProfileID == viewer.ProfileID && e.MediaItemID == itemID {
			return e, true, nil
		}
	}
	return userstore.WatchlistEntry{}, false, nil
}

func (f *fakePersonalLists) AddToWatchlist(_ context.Context, viewer handlers.PersonalListViewer, itemID string) error {
	if f.err != nil {
		return f.err
	}
	if f.missing[itemID] {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
	}
	for _, e := range f.watchlist {
		if e.ProfileID == viewer.ProfileID && e.MediaItemID == itemID {
			return nil
		}
	}
	f.watchlist = append([]userstore.WatchlistEntry{{ProfileID: viewer.ProfileID, MediaItemID: itemID, AddedAt: "2026-01-03T00:00:00Z"}}, f.watchlist...)
	return nil
}

func (f *fakePersonalLists) RemoveFromWatchlist(_ context.Context, viewer handlers.PersonalListViewer, itemID string) error {
	if f.err != nil {
		return f.err
	}
	kept := f.watchlist[:0]
	for _, e := range f.watchlist {
		if e.ProfileID != viewer.ProfileID || e.MediaItemID != itemID {
			kept = append(kept, e)
		}
	}
	f.watchlist = kept
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

// TestListFavoritesKeysetStable walks three pages while the list changes
// underneath: an entry added after page 1 (newer than the key) is neither
// repeated nor lets an older row slip past, an entry removed after page 2
// does not skip its neighbor, and equal added_at values are ordered by item
// id.
func TestListFavoritesKeysetStable(t *testing.T) {
	lists := &fakePersonalLists{favorites: []userstore.Favorite{
		{ProfileID: "p-owner", MediaItemID: "movie:f", AddedAt: "2026-01-03T00:00:00Z"},
		// e and d share added_at: d before e is wrong; e (greater id) first.
		{ProfileID: "p-owner", MediaItemID: "movie:d", AddedAt: "2026-01-02T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "movie:e", AddedAt: "2026-01-02T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "movie:c", AddedAt: "2026-01-01T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "movie:b", AddedAt: "2025-12-31T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "movie:a", AddedAt: "2025-12-30T00:00:00Z"},
	}}
	h := newTestHandler(t, favoritesDeps(lists))
	page := func(cursor string) cardPage {
		url := "/api/v2/favorites?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		rec := do(t, h, http.MethodGet, url, "", viewerHeaders())
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", url, rec.Code, rec.Body.String())
		}
		return decodeCards(t, rec.Body.String())
	}
	ids := func(p cardPage) string {
		var out []string
		for _, it := range p.Items {
			out = append(out, it.ContentID)
		}
		return strings.Join(out, ",")
	}
	first := page("")
	if ids(first) != "movie:f,movie:e" || !first.Page.HasMore {
		t.Fatalf("page 1 = %v", first)
	}
	// A new favorite lands at the head between pages: an offset cursor would
	// now answer movie:e again.
	lists.favorites = append(lists.favorites, userstore.Favorite{ProfileID: "p-owner", MediaItemID: "movie:g", AddedAt: "2026-01-04T00:00:00Z"})
	second := page(first.Page.NextCursor)
	if ids(second) != "movie:d,movie:c" || !second.Page.HasMore {
		t.Fatalf("page 2 = %v", second)
	}
	// An entry of page 1 is removed before page 3: an offset cursor would now
	// skip movie:b.
	lists.favorites = lists.favorites[1:]
	third := page(second.Page.NextCursor)
	if ids(third) != "movie:b,movie:a" || third.Page.HasMore || third.Page.NextCursor != "" {
		t.Fatalf("page 3 = %v", third)
	}
	// The tie: page 1 ends on movie:e (added_at equal to movie:d), and the
	// keyset (added_at, item_id) resumes at movie:d instead of repeating or
	// skipping it.
	single := page("")
	single = page(single.Page.NextCursor)
	if ids(single) != "movie:d,movie:c" {
		t.Fatalf("after tie = %v", single)
	}
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
	// Demo mode lets the mutations through, as v1's demo guard does.
	demo := favoritesDeps(lists)
	demo.DemoSettings = fakeSettings{demo: true}
	dh := newTestHandler(t, demo)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPut, "/api/v2/favorites/movie:new"}, {http.MethodDelete, "/api/v2/favorites/movie:c"},
	} {
		if rec := do(t, dh, tc.method, tc.path, "", viewerHeaders()); rec.Code != http.StatusNoContent {
			t.Fatalf("demo %s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
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
