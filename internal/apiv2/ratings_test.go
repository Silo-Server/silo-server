package apiv2

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeRatings struct {
	ratings []catalogpkg.UserRating
	hidden  map[string]bool
	err     error
	limits  []int
	access  []catalogpkg.AccessFilter
}

// ratingKeyAfter reports whether the row sorts strictly after the key in
// (rated_at DESC, media_item_id DESC) order.
func ratingKeyAfter(row catalogpkg.UserRating, after catalogpkg.RatingKey) bool {
	if !row.RatedAt.Equal(after.RatedAt) {
		return row.RatedAt.Before(after.RatedAt)
	}
	return row.MediaItemID < after.MediaItemID
}

func (f *fakeRatings) ListRatingsPage(_ context.Context, userID int, profileID string, _ catalogpkg.AccessFilter, after *catalogpkg.RatingKey, limit int) ([]catalogpkg.UserRating, map[string]bool, error) {
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, nil, f.err
	}
	var page []catalogpkg.UserRating
	for _, r := range f.ratings {
		if r.UserID != userID || r.ProfileID != profileID {
			continue
		}
		if after != nil && !ratingKeyAfter(r, *after) {
			continue
		}
		page = append(page, r)
	}
	sort.SliceStable(page, func(i, j int) bool {
		return ratingKeyAfter(page[j], catalogpkg.RatingKey{RatedAt: page[i].RatedAt, MediaItemID: page[i].MediaItemID})
	})
	if len(page) > limit {
		page = page[:limit]
	}
	visible := make(map[string]bool, len(page))
	for _, r := range page {
		visible[r.MediaItemID] = !f.hidden[r.MediaItemID]
	}
	return page, visible, nil
}

func (f *fakeRatings) GetRating(_ context.Context, userID int, profileID, itemID string, _ catalogpkg.AccessFilter) (catalogpkg.UserRating, bool, error) {
	if f.err != nil {
		return catalogpkg.UserRating{}, false, f.err
	}
	if f.hidden[itemID] {
		return catalogpkg.UserRating{}, false, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
	}
	for _, r := range f.ratings {
		if r.UserID == userID && r.ProfileID == profileID && r.MediaItemID == itemID {
			return r, true, nil
		}
	}
	return catalogpkg.UserRating{}, false, nil
}

func (f *fakeRatings) SetRating(_ context.Context, userID int, profileID, itemID string, access catalogpkg.AccessFilter, rating int) error {
	f.access = append(f.access, access)
	if f.err != nil {
		return f.err
	}
	if f.hidden[itemID] {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
	}
	for i, r := range f.ratings {
		if r.UserID == userID && r.ProfileID == profileID && r.MediaItemID == itemID {
			f.ratings[i].Rating = rating
			f.ratings[i].RatedAt = fixedTime().Add(time.Hour)
			return nil
		}
	}
	f.ratings = append([]catalogpkg.UserRating{{UserID: userID, ProfileID: profileID, MediaItemID: itemID, Rating: rating, RatedAt: fixedTime().Add(time.Hour)}}, f.ratings...)
	return nil
}

func (f *fakeRatings) DeleteRating(_ context.Context, userID int, profileID, itemID string) error {
	if f.err != nil {
		return f.err
	}
	kept := f.ratings[:0]
	for _, r := range f.ratings {
		if r.UserID != userID || r.ProfileID != profileID || r.MediaItemID != itemID {
			kept = append(kept, r)
		}
	}
	f.ratings = kept
	return nil
}

func ratingsDeps(ratings *fakeRatings) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.Ratings = ratings
	return deps
}

func ratingRows() []catalogpkg.UserRating {
	return []catalogpkg.UserRating{
		{UserID: 1, ProfileID: "p-owner", MediaItemID: "movie:c", Rating: 5, RatedAt: fixedTime()},
		{UserID: 1, ProfileID: "p-owner", MediaItemID: "movie:b", Rating: 3, RatedAt: fixedTime().Add(-time.Hour)},
		{UserID: 1, ProfileID: "p-owner", MediaItemID: "movie:a", Rating: 1, RatedAt: fixedTime().Add(-2 * time.Hour)},
		{UserID: 1, ProfileID: "p-other", MediaItemID: "movie:z", Rating: 2, RatedAt: fixedTime()},
	}
}

func TestListRatings(t *testing.T) {
	ratings := &fakeRatings{ratings: ratingRows()}
	// Continue inside a tie and retain submillisecond precision in the cursor.
	ratings.ratings[1].RatedAt = ratings.ratings[1].RatedAt.Add(333 * time.Nanosecond)
	ratings.ratings[2].RatedAt = ratings.ratings[1].RatedAt
	deps := ratingsDeps(ratings)
	deps.CursorSecret = []byte("ratings-cursor-test-key")
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/ratings", "", viewerHeaders())
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), `{"items":[{"item_id":"movie:c","rating":5,"rated_at":"2026-01-02T03:04:05.678Z"},`) || !strings.Contains(rec.Body.String(), `"has_more":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if ratings.limits[0] != DefaultLimit+1 {
		t.Fatalf("limit = %d", ratings.limits[0])
	}
	// Three rows page as 2+1 through the cursor.
	var ids []string
	cursor, pages := "", 0
	for {
		url := "/api/v2/ratings?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		rec := do(t, h, http.MethodGet, url, "", viewerHeaders())
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var page struct {
			Items []struct {
				ItemID string `json:"item_id"`
			} `json:"items"`
			Page struct {
				NextCursor string `json:"next_cursor"`
				HasMore    bool   `json:"has_more"`
			} `json:"page"`
		}
		decodeJSON(t, rec.Body, &page)
		pages++
		for _, it := range page.Items {
			ids = append(ids, it.ItemID)
		}
		if !page.Page.HasMore {
			break
		}
		cursor = page.Page.NextCursor
	}
	if pages != 2 || strings.Join(ids, ",") != "movie:c,movie:b,movie:a" {
		t.Fatalf("ids = %v pages = %d", ids, pages)
	}
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/ratings?offset=1", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.offset" || p.Errors[0].Code != codeUnknownParameter {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// A signed cursor whose timestamp cannot be parsed is still a client
	// error. The same scope with a good timestamp pages, so the refusal is the
	// timestamp's and not the scope's.
	scope := ratingsScope(access.SetScope(context.Background(), access.Scope{UserID: 1, ProfileID: "p-owner"}), 1, "p-owner")
	good, _ := NewCursors(deps.CursorSecret).Encode(scope, ratingPosition{RatedAt: fixedTime().Format(time.RFC3339Nano), MediaItemID: "movie:c"})
	if rec := do(t, h, http.MethodGet, "/api/v2/ratings?cursor="+good, "", viewerHeaders()); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"item_id":"movie:b"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	bad, _ := NewCursors(deps.CursorSecret).Encode(scope, ratingPosition{RatedAt: "yesterday", MediaItemID: "movie:a"})
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/ratings?cursor="+bad, "", viewerHeaders()), TypeInvalidCursor)
}

// ratingPages walks listRatings at the given page size and answers each
// page's item ids and whether it said more follow.
func ratingPages(t *testing.T, h http.Handler, limit int) (pages [][]string, more []bool, firstCursor string) {
	t.Helper()
	cursor := ""
	for {
		url := "/api/v2/ratings?limit=" + strconv.Itoa(limit)
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		rec := do(t, h, http.MethodGet, url, "", viewerHeaders())
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var page struct {
			Items []struct {
				ItemID string `json:"item_id"`
			} `json:"items"`
			Page struct {
				NextCursor string `json:"next_cursor"`
				HasMore    bool   `json:"has_more"`
			} `json:"page"`
		}
		decodeJSON(t, rec.Body, &page)
		ids := []string{}
		for _, it := range page.Items {
			ids = append(ids, it.ItemID)
		}
		pages = append(pages, ids)
		more = append(more, page.Page.HasMore)
		if firstCursor == "" {
			firstCursor = page.Page.NextCursor
		}
		if !page.Page.HasMore || len(pages) > 10 {
			return pages, more, firstCursor
		}
		cursor = page.Page.NextCursor
	}
}

// Ratings of items the viewer may not see are left out of the list and read
// as 404, without breaking the keyset paging: a hidden row as the cursor row
// or the probe row, and a page that is wholly hidden, still lead to the
// visible rows behind them.
func TestRatingsHideItemsTheViewerMayNotSee(t *testing.T) {
	ratings := &fakeRatings{
		ratings: []catalogpkg.UserRating{
			{UserID: 1, ProfileID: "p-owner", MediaItemID: "movie:e", Rating: 5, RatedAt: fixedTime()},
			{UserID: 1, ProfileID: "p-owner", MediaItemID: "movie:d", Rating: 4, RatedAt: fixedTime().Add(-time.Hour)},
			{UserID: 1, ProfileID: "p-owner", MediaItemID: "movie:c", Rating: 3, RatedAt: fixedTime().Add(-2 * time.Hour)},
			{UserID: 1, ProfileID: "p-owner", MediaItemID: "movie:b", Rating: 2, RatedAt: fixedTime().Add(-3 * time.Hour)},
			{UserID: 1, ProfileID: "p-owner", MediaItemID: "movie:a", Rating: 1, RatedAt: fixedTime().Add(-4 * time.Hour)},
		},
		hidden: map[string]bool{"movie:d": true, "movie:c": true, "movie:b": true},
	}
	deps := ratingsDeps(ratings)
	deps.CursorSecret = []byte("ratings-cursor-test-key")
	h := newTestHandler(t, deps)

	// Pages of two: [e, d*] [c*, b*] [a]. The first page's cursor row (d) and
	// probe row (c) are hidden, and the second page is hidden entirely.
	pages, more, cursor := ratingPages(t, h, 2)
	if len(pages) != 3 || strings.Join(pages[0], ",") != "movie:e" || len(pages[1]) != 0 || strings.Join(pages[2], ",") != "movie:a" ||
		!more[0] || !more[1] || more[2] {
		t.Fatalf("pages = %v, has_more = %v", pages, more)
	}
	rec := do(t, h, http.MethodGet, "/api/v2/ratings", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `{"item_id":"movie:e","rating":5,`) || !strings.Contains(rec.Body.String(), `{"item_id":"movie:a","rating":1,`) ||
		strings.Contains(rec.Body.String(), "movie:d") || strings.Contains(rec.Body.String(), "movie:c") || strings.Contains(rec.Body.String(), "movie:b") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	// A hidden title's rating reads as 404, a visible one as before.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/ratings/movie:d", "", viewerHeaders()), TypeNotFound)
	if rec := do(t, h, http.MethodGet, "/api/v2/ratings/movie:e", "", viewerHeaders()); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"rating":5`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	// A cursor minted under one access policy does not page under another.
	restricted := deps
	restricted.ViewerAccess = apimw.NewViewerAccessMiddleware(policyResolver{scope: &access.Scope{LibrariesRestricted: true, AllowedLibraryIDs: []int{1}}})
	requireProblem(t, do(t, newTestHandler(t, restricted), http.MethodGet, "/api/v2/ratings?limit=2&cursor="+cursor, "", viewerHeaders()), TypeInvalidCursor)
}

func TestGetRating(t *testing.T) {
	h := newTestHandler(t, ratingsDeps(&fakeRatings{ratings: ratingRows()}))
	rec := do(t, h, http.MethodGet, "/api/v2/ratings/movie:c", "", viewerHeaders())
	if rec.Code != 200 || rec.Body.String() != `{"item_id":"movie:c","rating":5,"rated_at":"2026-01-02T03:04:05.678Z"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Not rated, and another profile's rating, are both 404.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/ratings/movie:nope", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/ratings/movie:z", "", viewerHeaders()), TypeNotFound)
}

func TestRatingsDenied(t *testing.T) {
	h := newTestHandler(t, ratingsDeps(&fakeRatings{err: errStore}))
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v2/ratings", ""}, {http.MethodGet, "/api/v2/ratings/movie:c", ""},
		{http.MethodPut, "/api/v2/ratings/movie:c", `{"rating":3}`}, {http.MethodDelete, "/api/v2/ratings/movie:c", ""},
	} {
		requireProblem(t, do(t, h, tc.method, tc.path, tc.body, nil), TypeAuthenticationRequired)
		p := requireProblem(t, do(t, h, tc.method, tc.path, tc.body, bearer(memberToken)), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != locationProfileHeader {
			t.Fatalf("%s %s: errors = %+v", tc.method, tc.path, p.Errors)
		}
		requireProblem(t, do(t, h, tc.method, tc.path, tc.body, with(bearer(memberToken), "X-Profile-Id", "p-unknown")), TypeNotFound)
		requireProblem(t, do(t, h, tc.method, tc.path, tc.body, viewerHeaders()), TypeInternalError)
	}
	deps := pilotDeps(nil, nil)
	deps.Ratings = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/ratings", "", viewerHeaders()), TypeDependencyUnavailable)
}

var _ RatingService = (*handlers.RatingsHandler)(nil)
