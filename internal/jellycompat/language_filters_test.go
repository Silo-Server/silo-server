package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
)

type languageFacetContentService struct {
	countingContentService
	params url.Values
}

func (s *languageFacetContentService) ListItemFilters(_ context.Context, _ *Session, params url.Values) (*upstreamItemFiltersResponse, error) {
	s.params = params
	resp := &upstreamItemFiltersResponse{}
	if params.Get("language_facet_types") != "" {
		resp.AudioLanguages = []string{"ja", "en", "xx-unknown"}
		resp.SubtitleLanguages = []string{"pt-BR"}
	}
	return resp, nil
}

func TestHandleFilters2LanguageFacets(t *testing.T) {
	content := &languageFacetContentService{}
	h := &ItemsHandler{codec: NewResourceIDCodec(), content: content}

	serve := func(target string) queryFiltersDTO {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
		rec := httptest.NewRecorder()
		h.HandleFilters2Stub(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var dto queryFiltersDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return dto
	}

	dto := serve("/Items/Filters2?IncludeItemTypes=Movie,Episode")
	if got := content.params.Get("language_facet_types"); got != "movie,series" {
		t.Fatalf("language_facet_types = %q", got)
	}
	want := []nameValuePair{{Name: "English (en)", Value: "en"}, {Name: "Japanese (ja)", Value: "ja"}, {Name: "xx-unknown", Value: "xx-unknown"}}
	if !slices.Equal(dto.AudioLanguages, want) {
		t.Fatalf("AudioLanguages = %+v, want %+v", dto.AudioLanguages, want)
	}
	if len(dto.SubtitleLanguages) != 1 || dto.SubtitleLanguages[0].Value != "pt-BR" || dto.SubtitleLanguages[0].Name != "Brazilian Portuguese (pt-BR)" {
		t.Fatalf("SubtitleLanguages = %+v", dto.SubtitleLanguages)
	}

	dto = serve("/Items/Filters2?IncludeItemTypes=BoxSet")
	if content.params.Get("language_facet_types") != "" || len(dto.AudioLanguages) != 0 || dto.AudioLanguages == nil {
		t.Fatalf("non-video types must return empty (non-null) language facets: %+v", dto)
	}
}

// rootRoutingContent records whether a root /Items request listed library
// views or ran a catalog browse.
type rootRoutingContent struct {
	countingContentService
	browsed      bool
	browseParams url.Values
}

func (*rootRoutingContent) ListUserLibraries(context.Context, *Session) ([]upstreamUserLibrary, error) {
	return []upstreamUserLibrary{{ID: 3, Name: "Movies", Type: "movies"}}, nil
}

func (s *rootRoutingContent) BrowseItems(_ context.Context, _ *Session, params url.Values) (*upstreamBrowseResponse, error) {
	s.browsed = true
	s.browseParams = params
	return &upstreamBrowseResponse{}, nil
}
