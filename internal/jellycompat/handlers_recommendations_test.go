package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// fakeRowReader answers fixed rows and records the read.
type fakeRowReader struct {
	rows       []recommendations.ForYouRow
	err        error
	gotLimit   int
	gotFilter  catalog.AccessFilter
	gotProfile string
}

func (f *fakeRowReader) GetForYouPage(_ context.Context, _ int, profileID string, limit int, filter catalog.AccessFilter) ([]recommendations.ForYouRow, error) {
	f.gotLimit, f.gotFilter, f.gotProfile = limit, filter, profileID
	return f.rows, f.err
}

// fakeItemLoader loads every requested item.
type fakeItemLoader struct{ gotFilter catalog.AccessFilter }

func (f *fakeItemLoader) GetByIDsWithAccess(_ context.Context, ids []string, filter catalog.AccessFilter) ([]*models.MediaItem, error) {
	f.gotFilter = filter
	items := make([]*models.MediaItem, len(ids))
	for i, id := range ids {
		items[i] = &models.MediaItem{ContentID: id, Type: "movie", Title: "Title " + id}
	}
	return items, nil
}

func recommendationsRequest(t *testing.T, query string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/Movies/Recommendations"+query, nil)
	session := &Session{StreamAppUserID: 7, ProfileID: "kid", PseudoUserID: uuid.New()}
	return req.WithContext(context.WithValue(req.Context(), compatSessionKey, session))
}

func newTestRecommendationsHandler(reader RecommendationRowReader, items recommendationItemLoader, filter catalog.AccessFilter) *RecommendationsHandler {
	h := NewRecommendationsHandler(reader, nil, nil, nil, NewResourceIDCodec(), nil,
		func(context.Context, int, string) catalog.AccessFilter { return filter })
	h.items = items
	return h
}

func scoredRow(rowType, label string, ids ...string) recommendations.ForYouRow {
	row := recommendations.ForYouRow{Type: rowType, Label: label}
	for _, id := range ids {
		row.Items = append(row.Items, recommendations.ScoredItem{MediaItemID: id})
	}
	return row
}

// The categories come from the cached rows read with the session's access
// filter, itemLimit titles each, so a restricted profile gets full rows of
// what it can see.
func TestCompatRecommendationsReadTheCachedRowsForTheViewer(t *testing.T) {
	restricted := catalog.AccessFilter{UserID: 7, ProfileID: "kid", AllowedLibraryIDs: []int{4}}
	reader := &fakeRowReader{rows: []recommendations.ForYouRow{
		scoredRow("cluster", "For You", "kids-1", "kids-2"),
		scoredRow("popular", "Popular on This Server", "kids-3"),
	}}
	items := &fakeItemLoader{}
	h := newTestRecommendationsHandler(reader, items, restricted)

	rec := httptest.NewRecorder()
	h.HandleRecommendations(rec, recommendationsRequest(t, "?itemLimit=6&categoryLimit=5"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if reader.gotLimit != 12 || reader.gotProfile != "kid" || !slices.Equal(reader.gotFilter.AllowedLibraryIDs, []int{4}) {
		t.Fatalf("read limit=%d profile=%q filter=%+v, want twice itemLimit, kid and the session filter", reader.gotLimit, reader.gotProfile, reader.gotFilter)
	}
	if !slices.Equal(items.gotFilter.AllowedLibraryIDs, []int{4}) {
		t.Fatalf("hydration filter = %+v, want the session filter", items.gotFilter)
	}
	var categories []recommendationDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &categories); err != nil {
		t.Fatal(err)
	}
	if len(categories) != 2 || categories[0].BaselineItemName != "For You" || len(categories[0].Items) != 2 || len(categories[1].Items) != 1 {
		t.Fatalf("categories = %s", rec.Body.String())
	}
	if categories[0].Items[0].Name != "Title kids-1" {
		t.Fatalf("first item = %+v, want kids-1 first", categories[0].Items[0])
	}

	// Each category holds at most itemLimit titles.
	reader.rows = []recommendations.ForYouRow{scoredRow("cluster", "For You", "a", "b", "c", "d")}
	rec = httptest.NewRecorder()
	h.HandleRecommendations(rec, recommendationsRequest(t, "?itemLimit=3"))
	if err := json.Unmarshal(rec.Body.Bytes(), &categories); err != nil {
		t.Fatal(err)
	}
	if len(categories) != 1 || len(categories[0].Items) != 3 {
		t.Fatalf("categories = %s, want one of 3 items", rec.Body.String())
	}
}

func TestCompatRecommendationsEmptyAndFailedReads(t *testing.T) {
	// Nothing cached: an empty list.
	rec := httptest.NewRecorder()
	newTestRecommendationsHandler(&fakeRowReader{}, &fakeItemLoader{}, catalog.AccessFilter{}).HandleRecommendations(rec, recommendationsRequest(t, ""))
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("empty cache: %d %q, want 200 []", rec.Code, rec.Body.String())
	}

	// Recommendations not wired: an empty list.
	rec = httptest.NewRecorder()
	newTestRecommendationsHandler(nil, &fakeItemLoader{}, catalog.AccessFilter{}).HandleRecommendations(rec, recommendationsRequest(t, ""))
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("no reader: %d %q, want 200 []", rec.Code, rec.Body.String())
	}

	// A failed read is an error, not an empty list.
	rec = httptest.NewRecorder()
	newTestRecommendationsHandler(&fakeRowReader{err: errors.New("db down")}, &fakeItemLoader{}, catalog.AccessFilter{}).HandleRecommendations(rec, recommendationsRequest(t, ""))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed read: status %d, want 500", rec.Code)
	}
}

type countingRecommender struct {
	recommendations.Recommender
	calls int
}

func (c *countingRecommender) SimilarItems(context.Context, string, int, catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	c.calls++
	return []recommendations.ScoredItem{{MediaItemID: "movie-neighbor"}}, nil
}

// A Similar list anchored on an item the viewer cannot see never asks the
// recommender: it takes the genre path, which answers it as an unknown item.
func TestCompatSimilarSkipsTheRecommenderForAHiddenAnchor(t *testing.T) {
	codec := NewResourceIDCodec()
	recommender := &countingRecommender{}
	// No item repository: the anchor check finds nothing, as for an item
	// outside the viewer's libraries.
	h := &ItemsHandler{codec: codec, recommender: recommender, content: unavailableSessionContent{}}

	req := httptest.NewRequest(http.MethodGet, "/Items/x/Similar", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", codec.EncodeStringID(EncodedIDItem, "movie-hidden"))
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{StreamAppUserID: 7, ProfileID: "kid"})
	rec := httptest.NewRecorder()
	h.HandleSimilar(rec, req.WithContext(ctx))

	if recommender.calls != 0 {
		t.Fatalf("recommender ran %d times for a hidden anchor", recommender.calls)
	}
	var result queryResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(result.Items) != 0 || result.TotalRecordCount != 0 {
		t.Fatalf("response = %d %s, want an empty result", rec.Code, rec.Body.String())
	}
}

// Oversized limits are capped before they size anything.
func TestCompatRecommendationsCapTheirLimits(t *testing.T) {
	reader := &fakeRowReader{}
	h := newTestRecommendationsHandler(reader, &fakeItemLoader{}, catalog.AccessFilter{UserID: 7, ProfileID: "kid"})
	rec := httptest.NewRecorder()
	h.HandleRecommendations(rec, recommendationsRequest(t, "?itemLimit=1000000000&categoryLimit=1000000000"))
	if rec.Code != http.StatusOK || reader.gotLimit != recommendations.MaxRowReadLimit {
		t.Fatalf("status %d, read limit %d; want 200 and the most a row read returns", rec.Code, reader.gotLimit)
	}
}
