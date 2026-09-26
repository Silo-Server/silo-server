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

func TestParseItemsQueryLanguageFilters(t *testing.T) {
	codec := NewResourceIDCodec()
	req := httptest.NewRequest(http.MethodGet, "/Items?audioLanguages=eng,fre&audioLanguages=jpn&SubtitleLanguages=spa", nil)
	query := parseItemsQuery(req, codec)
	if !slices.Equal(query.audioLanguages, []string{"eng", "fre", "jpn"}) || !slices.Equal(query.subtitleLanguages, []string{"spa"}) {
		t.Fatalf("languages = %v / %v", query.audioLanguages, query.subtitleLanguages)
	}
	if !query.hasIntersectingFilters() {
		t.Fatal("language filters must route to the composed catalog browse")
	}
	params := buildBrowseParams(query)
	if params.Get("audio_languages") != "eng,fre,jpn" || params.Get("subtitle_languages") != "spa" {
		t.Fatalf("browse params = %v", params)
	}

	req = httptest.NewRequest(http.MethodGet, "/Items?SubtitleLanguages=spa&HasSubtitles=false", nil)
	if query := parseItemsQuery(req, codec); len(query.subtitleLanguages) != 0 {
		t.Fatalf("HasSubtitles=false must drop subtitle languages, got %v", query.subtitleLanguages)
	}
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
	browsed bool
}

func (*rootRoutingContent) ListUserLibraries(context.Context, *Session) ([]upstreamUserLibrary, error) {
	return []upstreamUserLibrary{{ID: 3, Name: "Movies", Type: "movies"}}, nil
}

func (s *rootRoutingContent) BrowseItems(context.Context, *Session, url.Values) (*upstreamBrowseResponse, error) {
	s.browsed = true
	return &upstreamBrowseResponse{}, nil
}

// Jellyfin 12 treats any HasFilters parameter on a user-root /Items request as
// a recursive search; a bare request (or one that only excludes virtual
// items) still lists the libraries.
func TestHandleItemsRootFiltersSearchRecursively(t *testing.T) {
	const (
		views = iota
		browse
		empty
	)
	cases := []struct {
		query string
		want  int
	}{
		{"", views},
		{"ExcludeLocationTypes=Virtual", views},
		{"MediaTypes=Video", browse},
		{"Tags=Classic", browse},
		{"HasSubtitles=true", browse},
		{"IsFavorite=false", browse},
		{"Recursive=true", browse},
		// Unresolvable IDs never widen into an unfiltered catalog browse.
		{"Ids=not-an-id", views},
		{"GenreIds=not-an-id", empty},
		{"PersonIds=not-an-id", empty},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			codec := NewResourceIDCodec()
			content := &rootRoutingContent{}
			h := &ItemsHandler{content: content, userData: &mockUserDataService{}, codec: codec, mapper: newMapper(codec, &config.Config{}), images: NewImageCache(time.Hour, time.Now)}
			result := performItemsRequest(t, h, "/Items?"+tc.query)
			if content.browsed != (tc.want == browse) {
				t.Fatalf("browsed = %v, want %v (items %+v)", content.browsed, tc.want == browse, result.Items)
			}
			switch tc.want {
			case views:
				if len(result.Items) != 1 || result.Items[0].Type != "CollectionFolder" {
					t.Fatalf("expected the library views, got %+v", result.Items)
				}
			case empty:
				if len(result.Items) != 0 || result.TotalRecordCount != 0 {
					t.Fatalf("expected no items, got %+v", result)
				}
			}
		})
	}
}
