package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
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

// fakeItemLoader loads every requested item except the hidden ones and
// those of a type the filter excludes. An item missing from types is a movie.
type fakeItemLoader struct {
	gotFilter catalog.AccessFilter
	gotIDs    []string
	hidden    map[string]bool
	types     map[string]string
}

func (f *fakeItemLoader) GetByIDsWithAccess(_ context.Context, ids []string, filter catalog.AccessFilter) ([]*models.MediaItem, error) {
	f.gotFilter, f.gotIDs = filter, ids
	items := make([]*models.MediaItem, 0, len(ids))
	for _, id := range ids {
		mediaType := f.types[id]
		if mediaType == "" {
			mediaType = "movie"
		}
		if !f.hidden[id] && !slices.Contains(filter.ExcludedMediaTypes, mediaType) {
			items = append(items, &models.MediaItem{ContentID: id, Type: mediaType, Title: "Title " + id})
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

type countingRecommender struct {
	recommendations.Recommender
	calls int
}

func (c *countingRecommender) SimilarItems(context.Context, int, string, string, int, catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
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

func (f *fixedRecommender) SimilarItems(_ context.Context, _ int, _, _ string, limit int, _ catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	f.gotLimit = limit
	return f.items, nil
}

// Oversized limits are capped before they size anything: a category holds
// at most a cached row's titles.
func TestCompatRecommendationsCapTheirLimits(t *testing.T) {
	reader := &fakeRowReader{rows: []recommendations.ForYouRow{clusterRow(0, "Drama", "movie-1")}}
	h := newTestRecommendationsHandler(reader, &fakeItemLoader{}, catalog.AccessFilter{UserID: 7, ProfileID: "kid"})
	rec := httptest.NewRecorder()
	h.HandleRecommendations(rec, recommendationsRequest(t, "?itemLimit=1000000000&categoryLimit=1000000000"))
	if rec.Code != http.StatusOK || reader.gotLimit != recommendations.MaxRowReadLimit {
		t.Fatalf("status %d, read limit %d; want 200 and the most a row read returns", rec.Code, reader.gotLimit)
	}
}

// genreFallbackContent answers a drama anchor and a page of dramas.
type genreFallbackContent struct{ ContentService }

func (genreFallbackContent) GetItemDetail(_ context.Context, _ *Session, id string, _ *int) (*upstreamItemDetail, error) {
	return &upstreamItemDetail{ContentID: id, Type: "movie", Title: "Anchor", Genres: []string{"Drama"}}, nil
}

// genreFallbackDepth is how many marked dramas genreFallbackContent puts
// between A and B.
var genreFallbackDepth = 2

// BrowseItems answers the first limit dramas, best first: A, then
// genreFallbackDepth marked titles, then B and C.
func (genreFallbackContent) BrowseItems(_ context.Context, _ *Session, params url.Values) (*upstreamBrowseResponse, error) {
	all := []upstreamListItem{{ContentID: "movie-a", Type: "movie", Title: "A"}}
	for i := range genreFallbackDepth {
		all = append(all, upstreamListItem{ContentID: fmt.Sprintf("movie-marked-%d", i), Type: "movie", Title: "Marked"})
	}
	all = append(all,
		upstreamListItem{ContentID: "movie-b", Type: "movie", Title: "B"},
		upstreamListItem{ContentID: "movie-c", Type: "movie", Title: "C"},
	)
	limit, _ := strconv.Atoi(params.Get("limit"))
	return &upstreamBrowseResponse{Items: all[:min(limit, len(all))]}, nil
}

// markingUserData reports movie-marked as marked Not interested, or fails.
type markingUserData struct {
	*mockUserDataService
	err error
}

func (m markingUserData) ListNotInterestedByMediaItems(_ context.Context, _ *Session, ids []string) (map[string]bool, error) {
	if m.err != nil {
		return nil, m.err
	}
	marked := map[string]bool{}
	for _, id := range ids {
		if strings.HasPrefix(id, "movie-marked") {
			marked[id] = true
		}
	}
	return marked, nil
}
