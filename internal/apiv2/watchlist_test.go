package apiv2

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func watchlistRows() []userstore.WatchlistEntry {
	return []userstore.WatchlistEntry{
		{ProfileID: "p-owner", MediaItemID: "series:c", AddedAt: "2026-01-02T03:04:05Z"},
		{ProfileID: "p-owner", MediaItemID: "series:b", AddedAt: "2026-01-01T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "series:a", AddedAt: "2025-12-31T00:00:00Z"},
		{ProfileID: "p-other", MediaItemID: "series:z", AddedAt: "2025-12-30T00:00:00Z"},
	}
}

func TestListWatchlist(t *testing.T) {
	lists := &fakePersonalLists{watchlist: watchlistRows()}
	h := newTestHandler(t, favoritesDeps(lists))

	rec := do(t, h, http.MethodGet, "/api/v2/watchlist", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	page := decodeCards(t, rec.Body.String())
	if len(page.Items) != 3 || page.Items[0].ContentID != "series:c" || page.Page.HasMore || page.Page.NextCursor != "" {
		t.Fatalf("page = %s", rec.Body.String())
	}
	if lists.limits[0] != DefaultLimit+1 {
		t.Fatalf("limit = %d", lists.limits[0])
	}
	if v := lists.viewers[0]; v.UserID != 1 || v.ProfileID != "p-owner" || v.Access.DeviceID != "" {
		t.Fatalf("viewer = %+v", v)
	}
}

func TestListWatchlistCursor(t *testing.T) {
	lists := &fakePersonalLists{watchlist: watchlistRows()}
	h := newTestHandler(t, favoritesDeps(lists))
	ids, pages := listCardIDs(t, h, "/api/v2/watchlist?limit=2", viewerHeaders())
	if pages != 2 || strings.Join(ids, ",") != "series:c,series:b,series:a" {
		t.Fatalf("ids = %v pages = %d", ids, pages)
	}
	// A hidden (fully watched) series has no card but still counts toward
	// has_more: the page is one card short and the next page follows.
	lists.missing = map[string]bool{"series:b": true}
	ids, pages = listCardIDs(t, h, "/api/v2/watchlist?limit=2", viewerHeaders())
	if pages != 2 || strings.Join(ids, ",") != "series:c,series:a" {
		t.Fatalf("ids = %v pages = %d", ids, pages)
	}
	// The probe row's own card never leaks into the page.
	lists.missing = map[string]bool{"series:c": true}
	rec := do(t, h, http.MethodGet, "/api/v2/watchlist?limit=1", "", viewerHeaders())
	page := decodeCards(t, rec.Body.String())
	if len(page.Items) != 0 || !page.Page.HasMore {
		t.Fatalf("page = %s", rec.Body.String())
	}
	// A favorites cursor is refused on the watchlist.
	lists.favorites = favoriteRows()
	rec = do(t, h, http.MethodGet, "/api/v2/favorites?limit=1", "", viewerHeaders())
	foreign := decodeCards(t, rec.Body.String()).Page.NextCursor
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watchlist?cursor="+foreign, "", viewerHeaders()), TypeInvalidCursor)
}

func TestListWatchlistValidation(t *testing.T) {
	h := newTestHandler(t, favoritesDeps(&fakePersonalLists{}))
	for _, tc := range []struct{ query, location, code string }{
		{"offset=10", "query.offset", codeUnknownParameter},
		{"limit=0", "query.limit", codeOutOfRange},
		{"image_size=huge", "query.image_size", codeInvalidEnum},
	} {
		p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watchlist?"+tc.query, "", viewerHeaders()), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || p.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.query, p.Errors)
		}
	}
}

func TestGetWatchlistEntry(t *testing.T) {
	lists := &fakePersonalLists{watchlist: watchlistRows(), missing: map[string]bool{"series:hidden": true}}
	h := newTestHandler(t, favoritesDeps(lists))
	rec := do(t, h, http.MethodGet, "/api/v2/watchlist/series:c", "", viewerHeaders())
	if rec.Code != 200 || rec.Body.String() != `{"item_id":"series:c","added_at":"2026-01-02T03:04:05.000Z"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Not on the watchlist, an item the viewer may not see, and another
	// profile's entry are all 404.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watchlist/series:nope", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watchlist/series:hidden", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watchlist/series:z", "", viewerHeaders()), TypeNotFound)
}

func TestAddAndDeleteWatchlistEntry(t *testing.T) {
	lists := &fakePersonalLists{watchlist: watchlistRows(), missing: map[string]bool{"series:hidden": true}}
	h := newTestHandler(t, favoritesDeps(lists))
	for i := 0; i < 2; i++ {
		rec := do(t, h, http.MethodPut, "/api/v2/watchlist/series:new", "", viewerHeaders())
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("put %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if len(lists.watchlist) != 5 || lists.watchlist[0].MediaItemID != "series:new" {
		t.Fatalf("watchlist = %+v", lists.watchlist)
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/watchlist/series:hidden", "", viewerHeaders()), TypeNotFound)
	for i := 0; i < 2; i++ {
		rec := do(t, h, http.MethodDelete, "/api/v2/watchlist/series:new", "", viewerHeaders())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if len(lists.watchlist) != 4 {
		t.Fatalf("watchlist = %+v", lists.watchlist)
	}
	demo := favoritesDeps(lists)
	demo.DemoSettings = fakeSettings{demo: true}
	dh := newTestHandler(t, demo)
	requireProblem(t, do(t, dh, http.MethodPut, "/api/v2/watchlist/series:new", "", viewerHeaders()), TypePermissionDenied)
	requireProblem(t, do(t, dh, http.MethodDelete, "/api/v2/watchlist/series:c", "", viewerHeaders()), TypePermissionDenied)
}

func TestWatchlistDenied(t *testing.T) {
	h := newTestHandler(t, favoritesDeps(&fakePersonalLists{err: errStore}))
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v2/watchlist"}, {http.MethodGet, "/api/v2/watchlist/series:c"},
		{http.MethodPut, "/api/v2/watchlist/series:c"}, {http.MethodDelete, "/api/v2/watchlist/series:c"},
	} {
		requireProblem(t, do(t, h, tc.method, tc.path, "", nil), TypeAuthenticationRequired)
		p := requireProblem(t, do(t, h, tc.method, tc.path, "", bearer(memberToken)), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != locationProfileHeader {
			t.Fatalf("%s %s: errors = %+v", tc.method, tc.path, p.Errors)
		}
		requireProblem(t, do(t, h, tc.method, tc.path, "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
		requireProblem(t, do(t, h, tc.method, tc.path, "", with(bearer(memberToken), "X-Profile-Id", "p-unknown")), TypeNotFound)
		requireProblem(t, do(t, h, tc.method, tc.path, "", viewerHeaders()), TypeInternalError)
	}
	deps := pilotDeps(nil, nil)
	deps.PersonalLists = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/watchlist", "", viewerHeaders()), TypeDependencyUnavailable)
}
