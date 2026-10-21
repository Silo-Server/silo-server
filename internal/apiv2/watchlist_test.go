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

func TestListWatchlistCursor(t *testing.T) {
	lists := &fakePersonalLists{watchlist: watchlistRows()}
	// The first page ends inside a timestamp tie, so continuation must keep the item ID.
	lists.watchlist[2].AddedAt = lists.watchlist[1].AddedAt
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
