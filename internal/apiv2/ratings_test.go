package apiv2

import (
	"context"
	"encoding/base64"
	"net/http"
	"slices"
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

// ListRatingsPage answers the visible rows only, as the real store query
// does.
func (f *fakeRatings) ListRatingsPage(_ context.Context, userID int, profileID string, access catalogpkg.AccessFilter, after *catalogpkg.RatingKey, limit int) ([]catalogpkg.UserRating, error) {
	f.limits = append(f.limits, limit)
	f.access = append(f.access, access)
	if f.err != nil {
		return nil, f.err
	}
	var page []catalogpkg.UserRating
	for _, r := range f.ratings {
		if r.UserID != userID || r.ProfileID != profileID || f.hidden[r.MediaItemID] {
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
	return page, nil
}

func (f *fakeRatings) GetRating(_ context.Context, userID int, profileID, itemID string, access catalogpkg.AccessFilter) (catalogpkg.UserRating, bool, error) {
	f.access = append(f.access, access)
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
// page's item ids and the decoded payload of every cursor it was handed.
func ratingPages(t *testing.T, h http.Handler, limit int) (pages [][]string, payloads []string) {
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
		if !page.Page.HasMore || len(pages) > 10 {
			return pages, payloads
		}
		cursor = page.Page.NextCursor
		payload, err := base64.RawURLEncoding.DecodeString(strings.SplitN(cursor, ".", 2)[0])
		if err != nil {
			t.Fatalf("cursor %q: %v", cursor, err)
		}
		payloads = append(payloads, string(payload))
	}
}

// Ratings of items the viewer may not see are left out of the list and read
// as 404. Paging carries on past them, and no cursor names a hidden title:
// a cursor payload is signed, not encrypted.
func TestRatingsHideItemsTheViewerMayNotSee(t *testing.T) {
	ratings := &fakeRatings{hidden: map[string]bool{"movie:e": true, "movie:d": true, "movie:c": true}}
	for i, id := range []string{"movie:f", "movie:e", "movie:d", "movie:c", "movie:b", "movie:a"} {
		ratings.ratings = append(ratings.ratings, catalogpkg.UserRating{UserID: 1, ProfileID: "p-owner", MediaItemID: id, Rating: 5 - i%5, RatedAt: fixedTime().Add(-time.Duration(i) * time.Hour)})
	}
	deps := ratingsDeps(ratings)
	deps.CursorSecret = []byte("ratings-cursor-test-key")
	h := newTestHandler(t, deps)

	pages, payloads := ratingPages(t, h, 2)
	if len(pages) != 2 || strings.Join(pages[0], ",") != "movie:f,movie:b" || strings.Join(pages[1], ",") != "movie:a" {
		t.Fatalf("pages = %v", pages)
	}
	if len(payloads) != 1 || !strings.Contains(payloads[0], "movie:b") {
		t.Fatalf("cursor payloads = %v", payloads)
	}

	// A hidden title's rating reads as 404, a visible one as before.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/ratings/movie:d", "", viewerHeaders()), TypeNotFound)
	if rec := do(t, h, http.MethodGet, "/api/v2/ratings/movie:f", "", viewerHeaders()); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"rating":5`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	// The viewer's restriction reaches the seam, and a cursor minted under
	// one access policy does not page under another.
	restricted := deps
	restricted.ViewerAccess = apimw.NewViewerAccessMiddleware(policyResolver{scope: &access.Scope{LibrariesRestricted: true, AllowedLibraryIDs: []int{1}}})
	rh := newTestHandler(t, restricted)
	for _, path := range []string{"/api/v2/ratings", "/api/v2/ratings/movie:f"} {
		ratings.access = nil
		do(t, rh, http.MethodGet, path, "", viewerHeaders())
		if len(ratings.access) != 1 || !slices.Equal(ratings.access[0].AllowedLibraryIDs, []int{1}) {
			t.Fatalf("%s: access = %+v", path, ratings.access)
		}
	}
	first := do(t, h, http.MethodGet, "/api/v2/ratings?limit=1", "", viewerHeaders())
	var page struct {
		Page struct {
			NextCursor string `json:"next_cursor"`
		} `json:"page"`
	}
	decodeJSON(t, first.Body, &page)
	requireProblem(t, do(t, rh, http.MethodGet, "/api/v2/ratings?limit=1&cursor="+page.Page.NextCursor, "", viewerHeaders()), TypeInvalidCursor)
}
