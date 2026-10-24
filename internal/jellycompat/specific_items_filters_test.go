package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type mixedIDsContent struct {
	*librariesContentService
	browse  func(url.Values) (*upstreamBrowseResponse, error)
	details []string
}

func (s *mixedIDsContent) BrowseItems(_ context.Context, _ *Session, p url.Values) (*upstreamBrowseResponse, error) {
	return s.browse(p)
}
func (s *mixedIDsContent) GetItemDetail(_ context.Context, _ *Session, id string, _ *int) (*upstreamItemDetail, error) {
	s.details = append(s.details, id)
	return &upstreamItemDetail{ContentID: id, Type: "movie", Title: map[string]string{"a": "Alpha", "b": "Bravo"}[id]}, nil
}
func (s *mixedIDsContent) GetItemDetailsByIDs(ctx context.Context, session *Session, ids []string, libraryID *int) (map[string]*upstreamItemDetail, error) {
	out := make(map[string]*upstreamItemDetail, len(ids))
	for _, id := range ids {
		out[id], _ = s.GetItemDetail(ctx, session, id, libraryID)
	}
	return out, nil
}

func TestMixedSpecificIDsApplyFiltersBeforePaging(t *testing.T) {
	for _, tc := range []struct {
		name, query    string
		selected, want []string
		total          int
	}{
		{"favorite intersection", "IsFavorite=true&Genres=Drama&Years=2024", []string{"a"}, []string{"Alpha"}, 1},
		{"played", "IsPlayed=true", []string{"a"}, []string{"Alpha"}, 1},
		{"resume", "Filters=IsResumable", []string{"b"}, []string{"Bravo"}, 1},
		{"unplayed", "IsPlayed=false", []string{"b"}, []string{"Collections", "Bravo", "Cedar"}, 3},
		{"search", "SearchTerm=Cedar", nil, []string{"Cedar"}, 1},
		{"prefix", "NameStartsWith=Cedar", nil, []string{"Cedar"}, 1},
		{"type and page", "IncludeItemTypes=Movie&SortBy=SortName&SortOrder=Ascending&StartIndex=1&Limit=1", []string{"a", "b"}, []string{"Bravo"}, 2},
		{"collection type", "IncludeItemTypes=BoxSet", nil, []string{"Cedar"}, 1},
		{"view type", "IncludeItemTypes=CollectionFolder", nil, []string{"Collections"}, 1},
		{"no total", "IsPlayed=false&EnableTotalRecordCount=false&StartIndex=1&Limit=1", []string{"b"}, []string{"Bravo"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collections := &fakeCollectionSource{collections: []*models.LibraryCollection{{ID: "box", LibraryID: 1, Title: "Cedar", Visibility: "visible"}, {ID: "hidden", LibraryID: 9, Title: "Hidden", Visibility: "visible"}}}
			h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)
			content := &mixedIDsContent{librariesContentService: h.content.(*librariesContentService)}
			content.browse = func(p url.Values) (*upstreamBrowseResponse, error) {
				if p.Get("content_ids") != "a,b" || p.Get("offset") != "0" {
					t.Fatalf("unbounded or prematurely paged browse: %v", p)
				}
				if tc.name == "favorite intersection" && (p.Get("is_favorite") != "true" || p.Get("genres") != "Drama" || p.Get("years") != "2024" || p.Get("compose_state") != "true") {
					t.Fatalf("lost composed predicates: %v", p)
				}
				result := &upstreamBrowseResponse{}
				for _, id := range tc.selected {
					result.Items = append(result.Items, upstreamListItem{ContentID: id})
				}
				return result, nil
			}
			h.content = content
			ids := strings.Join([]string{h.codec.EncodeStringID(EncodedIDItem, "a"), h.codec.EncodeStringID(EncodedIDItem, "b"), h.codec.EncodeStringID(EncodedIDCollection, "box"), h.codec.EncodeStringID(EncodedIDCollection, "hidden"), collectionsViewID}, ",")
			result := performItemsRequest(t, h, "/Items?Ids="+url.QueryEscape(ids)+"&"+tc.query)
			names := make([]string, 0, len(result.Items))
			for _, item := range result.Items {
				names = append(names, item.Name)
			}
			if !slices.Equal(names, tc.want) || result.TotalRecordCount != tc.total {
				t.Fatalf("names=%v total=%d want=%v/%d", names, result.TotalRecordCount, tc.want, tc.total)
			}
			if !slices.Equal(content.details, tc.selected) {
				t.Fatalf("hydrated excluded media: %v", content.details)
			}
		})
	}
}

func TestMixedSpecificIDsBrowseFailureDoesNotReturnCollections(t *testing.T) {
	h := newCollectionsTestHandler(&fakeCollectionSource{}, nil, nil)
	content := &mixedIDsContent{librariesContentService: h.content.(*librariesContentService), browse: func(url.Values) (*upstreamBrowseResponse, error) { return nil, errors.New("catalog unavailable") }}
	h.content = content
	ids := h.codec.EncodeStringID(EncodedIDItem, "a") + "," + collectionsViewID
	req := httptest.NewRequest("GET", "/Items?Ids="+url.QueryEscape(ids)+"&IsFavorite=true", nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
	rec := httptest.NewRecorder()
	h.HandleItems(rec, req)
	if rec.Code != 500 || len(content.details) != 0 {
		t.Fatalf("status=%d details=%v", rec.Code, content.details)
	}
}

func TestMixedSpecificIDsFilterAllSelectedPages(t *testing.T) {
	selected := make([]string, 1002)
	for i := range selected {
		selected[i] = strconv.Itoa(i)
	}
	calls := 0
	content := &mixedIDsContent{browse: func(p url.Values) (*upstreamBrowseResponse, error) {
		calls++
		offset, _ := strconv.Atoi(p.Get("offset"))
		if p.Get("content_ids") != strings.Join(selected, ",") || p.Get("limit") != "1000" || p.Get("include_total") != "false" {
			t.Fatalf("lost bounded selected set: %v", p)
		}
		result := &upstreamBrowseResponse{}
		switch calls {
		case 1:
			if offset != 0 {
				t.Fatalf("first offset=%d", offset)
			}
			for _, id := range selected[:1000] {
				result.Items = append(result.Items, upstreamListItem{ContentID: id})
			}
			result.HasMore = true
		case 2:
			if offset != 1000 {
				t.Fatalf("second offset=%d", offset)
			}
			result.Items = []upstreamListItem{{ContentID: selected[1001]}}
		default:
			t.Fatal("unexpected extra catalog page")
		}
		return result, nil
	}}
	h := &ItemsHandler{content: content}
	got, err := h.filteredSpecificMediaIDs(context.Background(), collectionsTestSession(), itemsQuery{
		specificIDs: selected, isFavorite: true, startIndex: 50, limit: 1,
	})
	want := append(slices.Clone(selected[:1000]), selected[1001])
	if err != nil || calls != 2 || !slices.Equal(got, want) {
		t.Fatalf("membership: count=%d calls=%d err=%v", len(got), calls, err)
	}
}

func TestSpecificIDsWithoutLimitReturnEveryNamedItem(t *testing.T) {
	ids := make([]string, 30)
	for i := range ids {
		ids[i] = strconv.Itoa(i)
	}
	for _, tc := range []struct {
		name, query string
		wantCount   int
		wantLimit   string // catalog browse limit; empty when the request must not browse
	}{
		{name: "by ID", wantCount: 30},
		{name: "by ID with Limit", query: "&Limit=10", wantCount: 10},
		{name: "by ID from StartIndex", query: "&StartIndex=24", wantCount: 6},
		{name: "with type filter", query: "&IncludeItemTypes=Series", wantCount: 30, wantLimit: "30"},
		{name: "with type filter and Limit", query: "&IncludeItemTypes=Series&Limit=10", wantCount: 10, wantLimit: "10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCollectionsTestHandler(&fakeCollectionSource{}, nil, nil)
			content := &mixedIDsContent{librariesContentService: h.content.(*librariesContentService)}
			content.browse = func(p url.Values) (*upstreamBrowseResponse, error) {
				if tc.wantLimit == "" || p.Get("limit") != tc.wantLimit {
					t.Fatalf("browse limit=%q, want %q", p.Get("limit"), tc.wantLimit)
				}
				limit, _ := strconv.Atoi(p.Get("limit"))
				result := &upstreamBrowseResponse{Total: len(ids)}
				for _, id := range ids[:limit] {
					result.Items = append(result.Items, upstreamListItem{ContentID: id, Type: "series"})
				}
				return result, nil
			}
			h.content = content
			encoded := make([]string, len(ids))
			for i, id := range ids {
				encoded[i] = h.codec.EncodeStringID(EncodedIDItem, id)
			}
			result := performItemsRequest(t, h, "/Items?Ids="+url.QueryEscape(strings.Join(encoded, ","))+tc.query)
			if len(result.Items) != tc.wantCount || result.TotalRecordCount != len(ids) {
				t.Fatalf("items=%d total=%d, want %d/%d", len(result.Items), result.TotalRecordCount, tc.wantCount, len(ids))
			}
		})
	}
}

func TestSpecificIDsSendOnlyRequestedEmptyLists(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		wantTags     bool
	}{
		{name: "without Fields"},
		{name: "with Fields=Tags", fields: "&Fields=Tags", wantTags: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCollectionsTestHandler(&fakeCollectionSource{}, nil, nil)
			h.content = &mixedIDsContent{librariesContentService: h.content.(*librariesContentService)}
			result := performItemsRequest(t, h, "/Items?Ids="+h.codec.EncodeStringID(EncodedIDItem, "a")+tc.fields)
			if len(result.Items) != 1 {
				t.Fatalf("items=%d, want 1", len(result.Items))
			}
			item := result.Items[0]
			if (item.Tags != nil) != tc.wantTags {
				t.Errorf("Tags present=%v, want %v", item.Tags != nil, tc.wantTags)
			}
			if item.People != nil || item.MediaSources != nil || item.MediaStreams != nil || item.Chapters != nil || item.LockedFields != nil {
				t.Errorf("unrequested empty lists sent: %+v", item)
			}
		})
	}
}

// batchedDetailsContent resolves the batched ids through GetItemDetailsByIDs
// and every other id through GetItemDetail, which knows a season, answers 404
// for "missing", and fails for "broken". It records each call.
type batchedDetailsContent struct {
	*librariesContentService
	batched    map[string]bool
	batchErr   error
	batchCalls [][]string
	perItem    []string
}

func batchedDetailsFixture(id string) *upstreamItemDetail {
	if id == "season" {
		return &upstreamItemDetail{ContentID: id, Type: "season", Title: id}
	}
	return &upstreamItemDetail{ContentID: id, Type: "movie", Title: id}
}

func (s *batchedDetailsContent) GetItemDetailsByIDs(_ context.Context, _ *Session, ids []string, _ *int) (map[string]*upstreamItemDetail, error) {
	s.batchCalls = append(s.batchCalls, slices.Clone(ids))
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	out := make(map[string]*upstreamItemDetail, len(ids))
	for _, id := range ids {
		if s.batched[id] {
			out[id] = batchedDetailsFixture(id)
		}
	}
	return out, nil
}

func (s *batchedDetailsContent) GetItemDetail(_ context.Context, _ *Session, id string, _ *int) (*upstreamItemDetail, error) {
	s.perItem = append(s.perItem, id)
	switch id {
	case "missing":
		return nil, &HTTPError{StatusCode: http.StatusNotFound, Message: "item not found"}
	case "broken":
		return nil, errors.New("database unavailable")
	}
	return batchedDetailsFixture(id), nil
}

// TestSpecificIDsBatchDetailLookups: an Ids list fetches its details in one
// batch and looks up only what the batch left out one by one, keeping the
// order the client asked for, the 404 skip, and the abort on any other error.
func TestSpecificIDsBatchDetailLookups(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ids         []string
		batchErr    error
		wantStatus  int
		wantNames   []string
		wantPerItem []string
	}{
		{name: "batch serves what it can", ids: []string{"m1", "season", "missing", "m2"}, wantStatus: http.StatusOK, wantNames: []string{"m1", "season", "m2"}, wantPerItem: []string{"season", "missing"}},
		{name: "failed batch looks each id up", ids: []string{"m1", "season", "missing", "m2"}, batchErr: errors.New("batch failed"), wantStatus: http.StatusOK, wantNames: []string{"m1", "season", "m2"}, wantPerItem: []string{"m1", "season", "missing", "m2"}},
		{name: "other errors abort", ids: []string{"m1", "broken", "m2"}, wantStatus: http.StatusInternalServerError, wantPerItem: []string{"broken"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCollectionsTestHandler(&fakeCollectionSource{}, nil, nil)
			content := &batchedDetailsContent{librariesContentService: h.content.(*librariesContentService), batched: map[string]bool{"m1": true, "m2": true}, batchErr: tc.batchErr}
			h.content = content
			encoded := make([]string, len(tc.ids))
			for i, id := range tc.ids {
				encoded[i] = h.codec.EncodeStringID(EncodedIDItem, id)
			}
			req := httptest.NewRequest(http.MethodGet, "/Items?Ids="+url.QueryEscape(strings.Join(encoded, ",")), nil)
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
			rec := httptest.NewRecorder()
			h.HandleItems(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if len(content.batchCalls) != 1 || !slices.Equal(content.batchCalls[0], tc.ids) {
				t.Fatalf("batch calls = %v, want one for %v", content.batchCalls, tc.ids)
			}
			if !slices.Equal(content.perItem, tc.wantPerItem) {
				t.Fatalf("per-item lookups = %v, want %v", content.perItem, tc.wantPerItem)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var result queryResultDTO
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatalf("decode: %v", err)
			}
			names := make([]string, 0, len(result.Items))
			for _, item := range result.Items {
				names = append(names, item.Name)
			}
			if !slices.Equal(names, tc.wantNames) {
				t.Fatalf("names = %v, want %v", names, tc.wantNames)
			}
		})
	}
}
