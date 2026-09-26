package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Wholphin and other Jellyfin clients send these filters from library grids;
// each must reach the catalog rather than returning the unfiltered library.
func TestCompatBrowseFiltersReachCatalog(t *testing.T) {
	codec := NewResourceIDCodec()
	studioA := codec.EncodeStringID(EncodedIDStudio, "A24")
	studioB := codec.EncodeStringID(EncodedIDStudio, "Neon")
	excluded := codec.EncodeStringID(EncodedIDItem, "movie-9")
	path := "/Items?IncludeItemTypes=Movie&Recursive=true" +
		"&NameLessThan=M&NameStartsWithOrGreater=C" +
		"&ExcludeItemIds=" + excluded +
		"&StudioIds=" + studioA + "|" + studioB +
		"&OfficialRatings=PG-13|R&MinCommunityRating=7.5" +
		"&MinPremiereDate=2020-01-02T00:00:00.0000000Z&MaxPremiereDate=2024-12-31"
	query := parseItemsQuery(httptest.NewRequest(http.MethodGet, path, nil), codec)
	if !query.hasIntersectingFilters() {
		t.Fatal("compat filters must route to the catalog browse path")
	}

	browse := &stubBrowseSource{}
	svc := newDirectContentServiceForTest(browse, nil)
	if _, err := svc.BrowseItems(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, buildBrowseParams(query)); err != nil {
		t.Fatal(err)
	}
	if len(browse.calls) != 1 {
		t.Fatalf("catalog calls: %+v", browse.calls)
	}
	got := browse.calls[0].filters
	if got.NameLessThan != "M" || got.NameStartsWithOrGreater != "C" ||
		!slices.Equal(got.ExcludeContentIDs, []string{"movie-9"}) ||
		!slices.Equal(got.Studios, []string{"A24", "Neon"}) ||
		!slices.Equal(got.OfficialRatings, []string{"PG-13", "R"}) ||
		got.MinCommunityRating != 7.5 || got.MinPremiereDate != "2020-01-02" || got.MaxPremiereDate != "2024-12-31" {
		t.Fatalf("catalog filters: %+v", got)
	}
}

// Studio IDs that name no known studio match nothing, like GenreIds.
func TestUnknownStudioIDsMatchNothing(t *testing.T) {
	query := parseItemsQuery(httptest.NewRequest(http.MethodGet, "/Items?StudioIds=00000000-0000-0000-0000-000000000001", nil), NewResourceIDCodec())
	if !query.unmatchedIDFilter {
		t.Fatal("unknown StudioIds must not fall back to an unfiltered browse")
	}
}

// Limit=0 asks for TotalRecordCount only; Wholphin's letter jump uses it as a
// grid position.
func TestBrowseLimitZeroReturnsCountOnly(t *testing.T) {
	codec := NewResourceIDCodec()
	browse := &stubBrowseSource{items: []*models.MediaItem{{ContentID: "movie-1", Type: "movie", Title: "Alpha"}}, total: 41}
	h := &ItemsHandler{
		content:  newDirectContentServiceForTest(browse, nil),
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
		userData: &mockUserDataService{},
		images:   NewImageCache(time.Hour, time.Now),
	}
	req := httptest.NewRequest(http.MethodGet, "/Items?IncludeItemTypes=Movie&Recursive=true&NameLessThan=M&Limit=0", nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "p1"}))
	rec := httptest.NewRecorder()
	h.HandleItems(rec, req)

	var result struct {
		Items            json.RawMessage
		TotalRecordCount int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || string(result.Items) != "[]" || result.TotalRecordCount != 41 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(browse.calls) != 1 || browse.calls[0].filters.NameLessThan != "M" || !browse.calls[0].includeTotal {
		t.Fatalf("catalog calls: %+v", browse.calls)
	}
}

type manyStudiosContent struct{ countingContentService }

func (*manyStudiosContent) ListItemFilters(context.Context, *Session, url.Values) (*upstreamItemFiltersResponse, error) {
	studios := make([]string, 30)
	for i := range studios {
		studios[i] = fmt.Sprintf("Studio %02d", i)
	}
	return &upstreamItemFiltersResponse{Studios: studios}, nil
}

// Jellyfin returns every studio when Limit is absent, and Wholphin's studio
// letter jump counts studios before a letter.
func TestStudiosListsAllWithoutLimitAndHonorsNameBounds(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{content: &manyStudiosContent{}, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}}
	for _, tc := range []struct {
		query     string
		wantItems int
		wantTotal int
	}{
		{"", 30, 30},
		{"?Limit=5", 5, 30},
		{"?NameLessThan=Studio+10&Limit=0", 0, 10},
		{"?NameStartsWithOrGreater=Studio+25", 5, 5},
	} {
		req := httptest.NewRequest(http.MethodGet, "/Studios"+tc.query, nil)
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
		rec := httptest.NewRecorder()
		h.HandleStudios(rec, req)
		var result queryResultDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != tc.wantItems || result.TotalRecordCount != tc.wantTotal {
			t.Errorf("%q: items=%d total=%d, want %d and %d", tc.query, len(result.Items), result.TotalRecordCount, tc.wantItems, tc.wantTotal)
		}
	}
}

// People cannot be favorites in Silo, so a favorites listing is empty rather
// than every person in the catalog.
func TestFavoritePersonsListIsEmpty(t *testing.T) {
	for _, query := range []string{"?IsFavorite=true", "?Filters=IsFavorite"} {
		people := &personTotalsSource{}
		h := &PersonsHandler{personRepo: people, codec: NewResourceIDCodec()}
		req := httptest.NewRequest(http.MethodGet, "/Persons"+query, nil)
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
		rec := httptest.NewRecorder()
		h.HandleGetPersons(rec, req)
		var result queryResultDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 0 || result.TotalRecordCount != 0 || people.limit != 0 {
			t.Fatalf("%s: %+v (repo queried with limit %d)", query, result, people.limit)
		}
	}
}

// Jellyfin compares Min/MaxPremiereDate instants with premiere dates at
// midnight UTC.
func TestParsePremiereDateBound(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		isMin bool
		want  string
	}{
		{"2024-12-31", false, "2024-12-31"},
		{"2020-01-02T00:00:00.0000000Z", true, "2020-01-02"},
		{"2026-09-25T14:10:16.95-04:00", true, "2026-09-26"},
		{"2026-09-25T14:10:16.95-04:00", false, "2026-09-25"},
		{"2026-09-25T23:30:00-04:00", false, "2026-09-26"},
		{"2026-09-25T10:00:00+0000", false, "2026-09-25"},
		{"2026-09-25 10:00:00", true, "2026-09-26"},
		{"2026-09-25Tgarbage", true, "2026-09-25"},
		{"not a date", true, ""},
	} {
		if got := parsePremiereDateBound(tc.raw, tc.isMin); got != tc.want {
			t.Errorf("parsePremiereDateBound(%q, min=%v) = %q, want %q", tc.raw, tc.isMin, got, tc.want)
		}
	}
}

type countingBrowseSource struct {
	stubBrowseSource
	counts int
}

func (s *countingBrowseSource) BrowseCount(context.Context, catalog.BrowseFilters) (int, error) {
	s.counts++
	return 58490, nil
}

// Limit=0 runs only the catalog count, not a sorted page query.
func TestBrowseLimitZeroCountsWithoutPage(t *testing.T) {
	browse := &countingBrowseSource{}
	query := parseItemsQuery(httptest.NewRequest(http.MethodGet, "/Items?IncludeItemTypes=Movie&Recursive=true&NameLessThan=M&Limit=0", nil), NewResourceIDCodec())
	result, err := newDirectContentServiceForTest(browse, nil).BrowseItems(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, buildBrowseParams(query))
	if err != nil {
		t.Fatal(err)
	}
	if browse.counts != 1 || len(browse.calls) != 0 || result.Total != 58490 || len(result.Items) != 0 {
		t.Fatalf("counts=%d pages=%d result=%+v", browse.counts, len(browse.calls), result)
	}
}
