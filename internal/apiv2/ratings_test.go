package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeRatings struct {
	ratings []catalogpkg.UserRating
	hidden  map[string]bool
	err     error
	limits  []int
	access  []catalogpkg.AccessFilter
}

func (f *fakeRatings) ListRatings(_ context.Context, userID int, profileID string, limit, offset int) ([]catalogpkg.UserRating, error) {
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, f.err
	}
	var page []catalogpkg.UserRating
	for i := offset; i < len(f.ratings) && len(page) < limit; i++ {
		if f.ratings[i].UserID == userID && f.ratings[i].ProfileID == profileID {
			page = append(page, f.ratings[i])
		}
	}
	return page, nil
}

func (f *fakeRatings) GetRating(_ context.Context, userID int, profileID, itemID string) (catalogpkg.UserRating, bool, error) {
	if f.err != nil {
		return catalogpkg.UserRating{}, false, f.err
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
	h := newTestHandler(t, ratingsDeps(ratings))
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
}

func TestDeleteRating(t *testing.T) {
	ratings := &fakeRatings{ratings: ratingRows()}
	h := newTestHandler(t, ratingsDeps(ratings))
	for i := 0; i < 2; i++ {
		rec := do(t, h, http.MethodDelete, "/api/v2/ratings/movie:c", "", viewerHeaders())
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("delete %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if len(ratings.ratings) != 3 {
		t.Fatalf("ratings = %+v", ratings.ratings)
	}
	demo := ratingsDeps(ratings)
	demo.DemoSettings = fakeSettings{demo: true}
	requireProblem(t, do(t, newTestHandler(t, demo), http.MethodDelete, "/api/v2/ratings/movie:b", "", viewerHeaders()), TypePermissionDenied)
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

func TestSetRating(t *testing.T) {
	ratings := &fakeRatings{ratings: ratingRows(), hidden: map[string]bool{"movie:hidden": true}}
	h := newTestHandler(t, ratingsDeps(ratings))
	for i := 0; i < 2; i++ {
		rec := do(t, h, http.MethodPut, "/api/v2/ratings/movie:new", `{"rating":4}`, viewerHeaders())
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("put %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if len(ratings.ratings) != 5 || ratings.ratings[0].MediaItemID != "movie:new" || ratings.ratings[0].Rating != 4 {
		t.Fatalf("ratings = %+v", ratings.ratings)
	}
	// The access filter is the viewer's, without a device id.
	if a := ratings.access[0]; a.UserID != 1 || a.ProfileID != "p-owner" || a.DeviceID != "" {
		t.Fatalf("access = %+v", a)
	}
	// Re-rating replaces.
	if rec := do(t, h, http.MethodPut, "/api/v2/ratings/movie:c", `{"rating":1}`, viewerHeaders()); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := ratings.GetRating(context.Background(), 1, "p-owner", "movie:c"); !ok || ratings.ratings[1].Rating != 1 {
		t.Fatalf("ratings = %+v", ratings.ratings)
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/ratings/movie:hidden", `{"rating":3}`, viewerHeaders()), TypeNotFound)
	// The range and the strict body are typed validation.
	for _, tc := range []struct{ body, location, code string }{
		{`{"rating":0}`, "body.rating", codeOutOfRange},
		{`{"rating":6}`, "body.rating", codeOutOfRange},
		{`{}`, "body.rating", codeRequired},
		{`{"rating":3,"stars":3}`, "body.stars", codeUnknownField},
	} {
		p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/ratings/movie:c", tc.body, viewerHeaders()), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || p.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.body, p.Errors)
		}
	}
	demo := ratingsDeps(ratings)
	demo.DemoSettings = fakeSettings{demo: true}
	requireProblem(t, do(t, newTestHandler(t, demo), http.MethodPut, "/api/v2/ratings/movie:b", `{"rating":2}`, viewerHeaders()), TypePermissionDenied)
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
