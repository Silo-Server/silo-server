package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

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
