package jellycompat

import (
	"context"
	"fmt"
	"net/url"
	"slices"
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
	sort          string
}

func (s *pagedLibraryContent) BrowseItems(_ context.Context, _ *Session, params url.Values) (*upstreamBrowseResponse, error) {
	limit, _ := strconv.Atoi(params.Get("limit"))
	offset, _ := strconv.Atoi(params.Get("offset"))
	wantTotal := params.Get("include_total") != "false"
	s.calls = append(s.calls, pagedLibraryCall{limit, offset, wantTotal, params.Get("sort")})
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
		{"", 2037, "movie-0", []pagedLibraryCall{{1000, 0, true, "sort_title"}, {1000, 1000, false, "sort_title"}, {1000, 2000, false, "sort_title"}}},
		{"&StartIndex=1000", 1037, "movie-1000", []pagedLibraryCall{{1000, 1000, true, "sort_title"}, {1000, 2000, false, "sort_title"}}},
		{"&Limit=24", 24, "movie-0", []pagedLibraryCall{{24, 0, true, "sort_title"}}},
		{"&Limit=0", 0, "", []pagedLibraryCall{{1, 0, true, "sort_title"}}},
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

// countingUserData records the IDs each favorites and progress lookup names.
type countingUserData struct {
	mockUserDataService
	favoriteLookups, progressLookups []int
}

func (u *countingUserData) ListFavoritesByMediaItems(ctx context.Context, session *Session, ids []string) (map[string]bool, error) {
	u.favoriteLookups = append(u.favoriteLookups, len(ids))
	return u.mockUserDataService.ListFavoritesByMediaItems(ctx, session, ids)
}

func (u *countingUserData) ListProgressByMediaItems(ctx context.Context, session *Session, ids []string) (map[string]*upstreamProgress, error) {
	u.progressLookups = append(u.progressLookups, len(ids))
	return u.mockUserDataService.ListProgressByMediaItems(ctx, session, ids)
}

// A browse without Limit can list a whole library. Its user state is read in
// lookups of at most 1,000 IDs, as the SQLite user store binds one parameter
// per ID; a random order is read in a stable order and shuffled once, so no
// item is skipped between pages.
func TestBrowseItemsWithoutLimitBoundsLookupsAndShufflesOnce(t *testing.T) {
	content := &pagedLibraryContent{n: 2037}
	userData := &countingUserData{}
	codec := NewResourceIDCodec()
	h := &ItemsHandler{
		catalogUserState: true,
		content:          content,
		codec:            codec,
		mapper:           newMapper(codec, &config.Config{}),
		userData:         userData,
		images:           NewImageCache(time.Hour, time.Now),
	}

	result := performItemsRequest(t, h, "/Items?IncludeItemTypes=Movie&Recursive=true&SortBy=Random")

	ids := decodedItemIDs(t, codec, result)
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	if len(slices.Compact(sorted)) != 2037 {
		t.Fatalf("items = %d distinct of %d, want all 2037 once", len(slices.Compact(sorted)), len(ids))
	}
	for _, call := range content.calls {
		if call.sort != "sort_title" {
			t.Fatalf("browse call sort = %q, want the stable title order", call.sort)
		}
	}
	if want := []int{1000, 1000, 37}; !slices.Equal(userData.favoriteLookups, want) || !slices.Equal(userData.progressLookups, want) {
		t.Fatalf("favorite lookups = %v progress lookups = %v, want %v", userData.favoriteLookups, userData.progressLookups, want)
	}
}

// A random browse with StartIndex shuffles every match and then skips
// StartIndex of them, so the items it returns can be any of the library's,
// not only those after StartIndex in title order.
func TestBrowseItemsWithoutLimitShufflesBeforeStartIndex(t *testing.T) {
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

	result := performItemsRequest(t, h, "/Items?IncludeItemTypes=Movie&Recursive=true&SortBy=Random&StartIndex=1000")

	ids := decodedItemIDs(t, codec, result)
	if len(ids) != 1037 || result.TotalRecordCount != 2037 {
		t.Fatalf("items = %d total = %d, want 1037 total 2037", len(ids), result.TotalRecordCount)
	}
	if content.calls[0].offset != 0 {
		t.Fatalf("first browse offset = %d, want 0: every match is read before the shuffle", content.calls[0].offset)
	}
	early := 0
	for _, id := range ids {
		var n int
		if _, err := fmt.Sscanf(id, "movie-%d", &n); err == nil && n < 1000 {
			early++
		}
	}
	// About half of a uniform shuffle's last 1,037 come from the first 1,000
	// in title order; none did when StartIndex was skipped first.
	if early == 0 {
		t.Fatal("no item from before StartIndex in title order came back")
	}
}
