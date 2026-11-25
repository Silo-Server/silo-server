package jellycompat

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// pagedLibraryContent is a media_items browse over n movies. It caps a page at
// compatBrowseMaxLimit, as the catalog does, and records each call's limit,
// offset and whether it asked for the total.
type pagedLibraryContent struct {
	countingContentService
	n     int
	calls []pagedLibraryCall
}

type pagedLibraryCall struct {
	limit, offset int
	total         bool
}

func (s *pagedLibraryContent) BrowseItems(_ context.Context, _ *Session, params url.Values) (*upstreamBrowseResponse, error) {
	limit, _ := strconv.Atoi(params.Get("limit"))
	offset, _ := strconv.Atoi(params.Get("offset"))
	wantTotal := params.Get("include_total") != "false"
	s.calls = append(s.calls, pagedLibraryCall{limit, offset, wantTotal})
	limit = min(limit, compatBrowseMaxLimit)
	end := min(offset+limit, s.n)
	items := []upstreamListItem{}
	for i := offset; i < end; i++ {
		items = append(items, upstreamListItem{ContentID: fmt.Sprintf("movie-%d", i), Type: "movie", Title: fmt.Sprintf("Movie %d", i)})
	}
	total := 0
	if wantTotal {
		total = s.n
	}
	return &upstreamBrowseResponse{Items: items, Total: total, HasMore: end < s.n}, nil
}

// Jellyfin returns every matching item when Limit is absent, and Seerr's
// library sync reads a library in one such request (#5312). An explicit Limit
// keeps paging, and Limit=0 still only counts.
func TestBrowseItemsWithoutLimitReturnsEveryItem(t *testing.T) {
	for _, tc := range []struct {
		params    string
		wantCount int
		wantFirst string
		wantCalls []pagedLibraryCall
	}{
		{"", 2037, "movie-0", []pagedLibraryCall{{1000, 0, true}, {1000, 1000, false}, {1000, 2000, false}}},
		{"&StartIndex=1000", 1037, "movie-1000", []pagedLibraryCall{{1000, 1000, true}, {1000, 2000, false}}},
		{"&Limit=24", 24, "movie-0", []pagedLibraryCall{{24, 0, true}}},
		{"&Limit=0", 0, "", []pagedLibraryCall{{1, 0, true}}},
	} {
		t.Run(tc.params, func(t *testing.T) {
			content := &pagedLibraryContent{n: 2037}
			codec := NewResourceIDCodec()
			h := &ItemsHandler{
				catalogUserState: true,
				content:          content,
				codec:            codec,
				mapper:           newMapper(codec, &config.Config{}),
				userData:         &mockUserDataService{},
				images:           NewImageCache(time.Hour, time.Now),
			}

			result := performItemsRequest(t, h, "/Items?IncludeItemTypes=Movie,Series&Recursive=true&SortBy=SortName"+tc.params)

			ids := decodedItemIDs(t, codec, result)
			if len(ids) != tc.wantCount || result.TotalRecordCount != 2037 {
				t.Fatalf("items = %d total = %d, want %d total 2037", len(ids), result.TotalRecordCount, tc.wantCount)
			}
			if tc.wantFirst != "" && ids[0] != tc.wantFirst {
				t.Fatalf("first item = %q, want %q", ids[0], tc.wantFirst)
			}
			if fmt.Sprint(content.calls) != fmt.Sprint(tc.wantCalls) {
				t.Fatalf("browse calls = %v, want %v", content.calls, tc.wantCalls)
			}
		})
	}
}
