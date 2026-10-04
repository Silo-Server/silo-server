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

// fakeRowReader answers fixed rows and records the reads.
type fakeRowReader struct {
	rows       []recommendations.ForYouRow
	watched    []recommendations.ForYouRow
	err        error
	watchedErr error
	gotLimit   int
	gotFilter  catalog.AccessFilter
	gotProfile string
	gotWatched struct {
		maxRows, limit int
		filter         catalog.AccessFilter
	}
}

func (f *fakeRowReader) GetForYouPage(_ context.Context, _ int, profileID string, limit int, filter catalog.AccessFilter) ([]recommendations.ForYouRow, error) {
	f.gotLimit, f.gotFilter, f.gotProfile = limit, filter, profileID
	return f.rows, f.err
}

func (f *fakeRowReader) GetBecauseYouWatchedRows(_ context.Context, _ int, _ string, maxRows, limit int, filter catalog.AccessFilter) ([]recommendations.ForYouRow, error) {
	f.gotWatched.maxRows, f.gotWatched.limit, f.gotWatched.filter = maxRows, limit, filter
	return f.watched, f.watchedErr
}

// fakeItemLoader loads every requested item except the hidden ones.
type fakeItemLoader struct {
	gotFilter catalog.AccessFilter
	gotIDs    []string
	hidden    map[string]bool
}

func (f *fakeItemLoader) GetByIDsWithAccess(_ context.Context, ids []string, filter catalog.AccessFilter) ([]*models.MediaItem, error) {
	f.gotFilter, f.gotIDs = filter, ids
	items := make([]*models.MediaItem, 0, len(ids))
	for _, id := range ids {
		if !f.hidden[id] {
			items = append(items, &models.MediaItem{ContentID: id, Type: "movie", Title: "Title " + id})
		}
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

// clusterRow is a taste-cluster row about subject, as the Reader returns it.
func clusterRow(index int, subject string, ids ...string) recommendations.ForYouRow {
	row := scoredRow("cluster", "Because you enjoy "+subject, ids...)
	row.ClusterIndex, row.Subject = index, subject
	return row
}

// watchedRow is a Because You Watched row built from anchor.
func watchedRow(anchor string, ids ...string) recommendations.ForYouRow {
	row := scoredRow(recommendations.RecTypeBecauseWatched, "Because You Watched", ids...)
	row.AnchorItemID = anchor
	return row
}

func decodeCategories(t *testing.T, rec *httptest.ResponseRecorder) []recommendationDTO {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var categories []recommendationDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &categories); err != nil {
		t.Fatal(err)
	}
	return categories
}

// The categories come from the cached rows read with the session's access
// filter, itemLimit titles each, so a restricted profile gets full rows of
// what it can see.
func TestCompatRecommendationsReadTheCachedRowsForTheViewer(t *testing.T) {
	restricted := catalog.AccessFilter{UserID: 7, ProfileID: "kid", AllowedLibraryIDs: []int{4}}
	reader := &fakeRowReader{
		rows:    []recommendations.ForYouRow{clusterRow(0, "Animation", "kids-1", "kids-2")},
		watched: []recommendations.ForYouRow{watchedRow("kids-anchor", "kids-3")},
	}
	items := &fakeItemLoader{}
	h := newTestRecommendationsHandler(reader, items, restricted)

	rec := httptest.NewRecorder()
	h.HandleRecommendations(rec, recommendationsRequest(t, "?itemLimit=6&categoryLimit=5"))
	categories := decodeCategories(t, rec)
	if reader.gotLimit != 12 || reader.gotProfile != "kid" || !slices.Equal(reader.gotFilter.AllowedLibraryIDs, []int{4}) {
		t.Fatalf("read limit=%d profile=%q filter=%+v, want twice itemLimit, kid and the session filter", reader.gotLimit, reader.gotProfile, reader.gotFilter)
	}
	if reader.gotWatched.maxRows != compatBecauseWatchedRows || reader.gotWatched.limit != 12 || !slices.Equal(reader.gotWatched.filter.AllowedLibraryIDs, []int{4}) {
		t.Fatalf("because-watched read = %+v, want %d rows of twice itemLimit under the session filter", reader.gotWatched, compatBecauseWatchedRows)
	}
	if !slices.Equal(items.gotFilter.AllowedLibraryIDs, []int{4}) || !slices.Contains(items.gotIDs, "kids-anchor") {
		t.Fatalf("hydration ids=%v filter=%+v, want the anchor fetched under the session filter", items.gotIDs, items.gotFilter)
	}
	if len(categories) != 2 || len(categories[0].Items) != 1 || len(categories[1].Items) != 2 {
		t.Fatalf("categories = %s", rec.Body.String())
	}
	if categories[1].Items[0].Name != "Title kids-1" {
		t.Fatalf("first cluster item = %+v, want kids-1 first", categories[1].Items[0])
	}

	// Each category holds at most itemLimit titles.
	reader.rows = []recommendations.ForYouRow{clusterRow(0, "Animation", "a", "b", "c", "d")}
	reader.watched = nil
	rec = httptest.NewRecorder()
	h.HandleRecommendations(rec, recommendationsRequest(t, "?itemLimit=3"))
	if categories := decodeCategories(t, rec); len(categories) != 1 || len(categories[0].Items) != 3 {
		t.Fatalf("categories = %s, want one of 3 items", rec.Body.String())
	}
}

// Jellyfin clients head a category "Because you watched {BaselineItemName}"
// or "Because you like {BaselineItemName}" by its type. Only rows those words
// describe are sent: Because You Watched rows under their anchor's title,
// first, then taste-cluster rows under their genre label. The main For You
// row, an unlabeled cluster, Similar Users and the server-wide rows are left
// out, as is a row whose anchor the viewer cannot see. A title shows once,
// in the first category that has it, and each category has its own ID.
func TestCompatRecommendationsSendOnlyTruthfulHeadings(t *testing.T) {
	reader := &fakeRowReader{
		rows: []recommendations.ForYouRow{
			scoredRow("cluster", "For You", "main-1", "main-2"),
			clusterRow(0, "Science Fiction & Thriller", "scifi-1", "shared"),
			{Type: "cluster", Label: "Because you enjoy For You", ClusterIndex: 1, Items: []recommendations.ScoredItem{{MediaItemID: "unlabeled-1"}}},
			clusterRow(2, "Comedy", "comedy-1"),
			scoredRow("similar_users_liked", "Profiles Like You Enjoyed", "fans-1"),
			scoredRow("popular", "Popular on This Server", "popular-1"),
			scoredRow("recently_added", "Recently Added", "recent-1"),
			scoredRow("top_rated", "Top Rated", "top-1"),
			scoredRow("genre_sampler", "Top Drama", "drama-1"),
		},
		watched: []recommendations.ForYouRow{
			watchedRow("anchor-visible", "watched-1", "shared"),
			watchedRow("anchor-hidden", "hidden-anchor-1"),
		},
	}
	items := &fakeItemLoader{hidden: map[string]bool{"anchor-hidden": true}}
	rec := httptest.NewRecorder()
	newTestRecommendationsHandler(reader, items, catalog.AccessFilter{}).HandleRecommendations(rec, recommendationsRequest(t, "?categoryLimit=10&itemLimit=20"))
	categories := decodeCategories(t, rec)

	type heading struct{ kind, baseline string }
	var got []heading
	ids := map[string]bool{}
	var titles []string
	for _, c := range categories {
		got = append(got, heading{c.RecommendationType, c.BaselineItemName})
		ids[c.CategoryID] = true
		for _, item := range c.Items {
			titles = append(titles, item.Name)
		}
	}
	want := []heading{
		{"SimilarToRecentlyPlayed", "Title anchor-visible"},
		{"SimilarToLikedItem", "Science Fiction & Thriller"},
		{"SimilarToLikedItem", "Comedy"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("headings = %v, want %v", got, want)
	}
	if len(ids) != len(categories) {
		t.Fatalf("category IDs repeat: %s", rec.Body.String())
	}
	wantTitles := []string{"Title watched-1", "Title shared", "Title scifi-1", "Title comedy-1"}
	if !slices.Equal(titles, wantTitles) {
		t.Fatalf("titles = %v, want %v", titles, wantTitles)
	}
}

// A new profile has only server-wide rows and no history, so there is no
// category to send: the answer is an empty list, and the client shows its
// own Latest and Resume rows.
func TestCompatRecommendationsColdStartIsEmpty(t *testing.T) {
	reader := &fakeRowReader{rows: []recommendations.ForYouRow{
		scoredRow("popular", "Popular on This Server", "popular-1"),
		scoredRow("recently_added", "Recently Added", "recent-1"),
	}}
	rec := httptest.NewRecorder()
	newTestRecommendationsHandler(reader, &fakeItemLoader{}, catalog.AccessFilter{}).HandleRecommendations(rec, recommendationsRequest(t, ""))
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("cold start: %d %q, want 200 []", rec.Code, rec.Body.String())
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
	for name, reader := range map[string]*fakeRowReader{
		"page":                {err: errors.New("db down")},
		"because you watched": {watchedErr: errors.New("db down")},
	} {
		rec = httptest.NewRecorder()
		newTestRecommendationsHandler(reader, &fakeItemLoader{}, catalog.AccessFilter{}).HandleRecommendations(rec, recommendationsRequest(t, ""))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("failed %s read: status %d, want 500", name, rec.Code)
		}
	}
}

type countingRecommender struct {
	recommendations.Recommender
	calls int
}

func (c *countingRecommender) SimilarItems(context.Context, string, int) ([]recommendations.ScoredItem, error) {
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

// similarItemRepo loads the requested items the viewer may see, recording
// the access filter of each load.
type similarItemRepo struct {
	itemRepoForBatchLoader
	hidden  map[string]bool
	filters []catalog.AccessFilter
}

func (r *similarItemRepo) GetByIDsWithAccess(_ context.Context, ids []string, filter catalog.AccessFilter) ([]*models.MediaItem, error) {
	r.filters = append(r.filters, filter)
	var items []*models.MediaItem
	for _, id := range ids {
		if !r.hidden[id] {
			items = append(items, &models.MediaItem{ContentID: id, Type: "movie", Title: "Title " + id})
		}
	}
	return items, nil
}

type fixedRecommender struct {
	recommendations.Recommender
	items    []recommendations.ScoredItem
	gotLimit int
}

func (f *fixedRecommender) SimilarItems(_ context.Context, _ string, limit int) ([]recommendations.ScoredItem, error) {
	f.gotLimit = limit
	return f.items, nil
}

// A Similar list anchored on a visible item comes from the recommender, in
// its order, and holds only the titles the viewer may see.
func TestCompatSimilarServesTheRecommenderAccessFiltered(t *testing.T) {
	codec := NewResourceIDCodec()
	recommender := &fixedRecommender{items: []recommendations.ScoredItem{
		{MediaItemID: "movie-near"}, {MediaItemID: "movie-hidden"}, {MediaItemID: "movie-far"},
	}}
	repo := &similarItemRepo{hidden: map[string]bool{"movie-hidden": true}}
	restricted := catalog.AccessFilter{UserID: 7, ProfileID: "kid", AllowedLibraryIDs: []int{4}}
	h := &ItemsHandler{
		codec:        codec,
		mapper:       newMapper(codec, nil),
		recommender:  recommender,
		itemRepo:     repo,
		content:      unavailableSessionContent{},
		accessFilter: func(context.Context, int, string) catalog.AccessFilter { return restricted },
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/x/Similar?Limit=5", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", codec.EncodeStringID(EncodedIDItem, "movie-anchor"))
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{StreamAppUserID: 7, ProfileID: "kid"})
	rec := httptest.NewRecorder()
	h.HandleSimilar(rec, req.WithContext(ctx))

	var result queryResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, item := range result.Items {
		names = append(names, item.Name)
	}
	if rec.Code != http.StatusOK || !slices.Equal(names, []string{"Title movie-near", "Title movie-far"}) || result.TotalRecordCount != 2 {
		t.Fatalf("response = %d %v (%d), want the two visible neighbors in order", rec.Code, names, result.TotalRecordCount)
	}
	if recommender.gotLimit != 5 {
		t.Fatalf("recommender limit = %d, want 5", recommender.gotLimit)
	}
	// One load checks the anchor, one the neighbors; both under the session's access.
	if len(repo.filters) != 2 {
		t.Fatalf("item loads = %d, want 2", len(repo.filters))
	}
	for _, filter := range repo.filters {
		if !slices.Equal(filter.AllowedLibraryIDs, []int{4}) || filter.ProfileID != "kid" {
			t.Fatalf("load filter = %+v, want the session's", filter)
		}
	}
}
