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
	err     error
	limits  []int
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

func TestRatingsDenied(t *testing.T) {
	h := newTestHandler(t, ratingsDeps(&fakeRatings{err: errStore}))
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v2/ratings"}, {http.MethodDelete, "/api/v2/ratings/movie:c"},
	} {
		requireProblem(t, do(t, h, tc.method, tc.path, "", nil), TypeAuthenticationRequired)
		p := requireProblem(t, do(t, h, tc.method, tc.path, "", bearer(memberToken)), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != locationProfileHeader {
			t.Fatalf("%s %s: errors = %+v", tc.method, tc.path, p.Errors)
		}
		requireProblem(t, do(t, h, tc.method, tc.path, "", with(bearer(memberToken), "X-Profile-Id", "p-unknown")), TypeNotFound)
		requireProblem(t, do(t, h, tc.method, tc.path, "", viewerHeaders()), TypeInternalError)
	}
	deps := pilotDeps(nil, nil)
	deps.Ratings = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/ratings", "", viewerHeaders()), TypeDependencyUnavailable)
}

var _ RatingService = (*handlers.RatingsHandler)(nil)
