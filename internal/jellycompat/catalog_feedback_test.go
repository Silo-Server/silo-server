package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestBrowseYearsWhitespaceReachesCatalog(t *testing.T) {
	browse := &stubBrowseSource{}
	svc := newDirectContentServiceForTest(browse, nil)
	_, err := svc.BrowseItems(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, url.Values{"type": {"movie"}, "years": {"2023, 2024 ,invalid,0,-1"}, "limit": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(browse.calls) != 1 || !slices.Equal(browse.calls[0].filters.Years, []int{2023, 2024}) {
		t.Fatalf("catalog calls: %+v", browse.calls)
	}
}

type feedbackFacetContent struct{ countingContentService }

func (*feedbackFacetContent) ListItemFilters(context.Context, *Session, url.Values) (*upstreamItemFiltersResponse, error) {
	return &upstreamItemFiltersResponse{Genres: []string{"Drama"}, Studios: []string{"Studio"}}, nil
}

func TestPastLastPageSerializesEmptyItemsArray(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{content: &feedbackFacetContent{}, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}, images: NewImageCache(time.Hour, time.Now)}
	for _, tc := range []struct {
		name, path string
		handler    http.HandlerFunc
	}{
		{"genres", "/Genres?StartIndex=10&Limit=1", h.HandleGenres},
		{"studios", "/Studios?StartIndex=10&Limit=1", h.HandleStudios},
		{"specific IDs", "/Items?Ids=" + codec.EncodeStringID(EncodedIDItem, "movie") + "&StartIndex=10&Limit=1", h.HandleItems},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			var result struct {
				Items            json.RawMessage
				TotalRecordCount int
				StartIndex       int
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || string(result.Items) != "[]" || result.TotalRecordCount != 1 || result.StartIndex != 10 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
