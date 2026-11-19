package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
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
func (s *mixedIDsContent) GetItemDetailsByIDs(ctx context.Context, session *Session, ids []string, libraryID *int, _ detailBatchOptions) (map[string]*upstreamItemDetail, error) {
	out := make(map[string]*upstreamItemDetail, len(ids))
	for _, id := range ids {
		out[id], _ = s.GetItemDetail(ctx, session, id, libraryID)
	}
	return out, nil
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

func (s *batchedDetailsContent) GetItemDetailsByIDs(_ context.Context, _ *Session, ids []string, _ *int, _ detailBatchOptions) (map[string]*upstreamItemDetail, error) {
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
