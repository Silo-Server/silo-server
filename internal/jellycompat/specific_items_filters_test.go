package jellycompat

import (
	"context"
	"errors"
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
