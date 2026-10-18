package catalog

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestMediaScopeItemTypes pins the expansion of group scopes: "video" covers
// the video-side media_items types so search/browse can offer a
// "Movies & Series vs Audiobooks" split without enumerating types per caller.
func TestMediaScopeItemTypes(t *testing.T) {
	cases := []struct {
		scope string
		want  []string
	}{
		{"", nil},
		{"movie", []string{"movie"}},
		{"audiobook", []string{"audiobook"}},
		// A manga library browses only its series items; the per-chapter ebook
		// items are excluded because the manga scope expands to type=manga only.
		{"manga", []string{"manga"}},
		{"video", []string{"movie", "series"}},
		{" Video ", []string{"movie", "series"}},
		{"video_with_episodes", []string{"movie", "series", "episode"}},
	}
	for _, tc := range cases {
		if got := MediaScopeItemTypes(tc.scope); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("MediaScopeItemTypes(%q) = %v, want %v", tc.scope, got, tc.want)
		}
	}
}

func TestMediaScopeMatchesItemType(t *testing.T) {
	cases := []struct {
		scope    string
		itemType string
		want     bool
	}{
		{"", "audiobook", true},
		{"video", "movie", true},
		{"video", "series", true},
		{"video", "audiobook", false},
		{"audiobook", "audiobook", true},
		{"manga", "manga", true},
		{"manga", "ebook", false},
		{"movie", "series", false},
		{"video_with_episodes", "episode", true},
		{"video_with_episodes", "audiobook", false},
	}
	for _, tc := range cases {
		if got := MediaScopeMatchesItemType(tc.scope, tc.itemType); got != tc.want {
			t.Errorf("MediaScopeMatchesItemType(%q, %q) = %v, want %v", tc.scope, tc.itemType, got, tc.want)
		}
	}
}

// TestParseCatalogRequest_VideoMediaScope asserts ?type=video parses into the
// query definition's media scope, and that catalogSearchAccess expands it to
// the video item types for the direct search path.
func TestParseCatalogRequest_VideoMediaScope(t *testing.T) {
	req, err := ParseCatalogRequest(url.Values{
		"source": {"query"},
		"q":      {"the rookie"},
		"type":   {"video"},
	})
	if err != nil {
		t.Fatalf("ParseCatalogRequest: %v", err)
	}
	if req.Query.MediaScope != "video" {
		t.Fatalf("expected media scope video, got %q", req.Query.MediaScope)
	}

	_, itemTypes, earlyEmpty := catalogSearchAccess(req, AccessFilter{})
	if earlyEmpty {
		t.Fatal("unexpected early empty")
	}
	if !reflect.DeepEqual(itemTypes, []string{"movie", "series"}) {
		t.Fatalf("expected video scope to expand to movie+series, got %v", itemTypes)
	}
}

// TestPreviewPage_VideoScopeUsesTypeAny asserts the preview/query-executor
// path renders a multi-type condition for the video group scope.
func TestPreviewPage_VideoScopeUsesTypeAny(t *testing.T) {
	sql, args, err := (&QueryExecutor{}).buildPreviewPageSQL(
		QueryDefinition{
			MediaScope: "video",
			Sort:       QuerySort{Field: "title", Order: "asc"},
		},
		AccessFilter{},
		20,
		0,
		true,
	)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL error: %v", err)
	}
	if !strings.Contains(sql, "mi.type = ANY(") {
		t.Fatalf("expected mi.type = ANY(...) for video scope, got %s", sql)
	}
	found := false
	for _, arg := range args {
		if types, ok := arg.([]string); ok && reflect.DeepEqual(types, []string{"movie", "series"}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected movie+series type arg, got %v", args)
	}
}

// TestQueryDefinitionValidate_VideoScope asserts "video" passes definition
// validation alongside the single-type scopes.
func TestQueryDefinitionValidate_VideoScope(t *testing.T) {
	def := QueryDefinition{MediaScope: "video"}
	if err := def.Validate(); err != nil {
		t.Fatalf("expected video media scope to validate, got %v", err)
	}
	bad := QueryDefinition{MediaScope: "podcast"}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected invalid media scope to fail validation")
	}
	// Smart collections and sections store definitions; the search-only
	// scope must not become a stored scope the media_items executor cannot
	// honor.
	searchOnly := QueryDefinition{MediaScope: MediaScopeVideoWithEpisodes}
	if err := searchOnly.Validate(); err == nil {
		t.Fatal("expected the search-only scope to fail definition validation")
	}
}

// TestParseCatalogRequest_VideoWithEpisodesScope pins the search-only scope:
// the v2 grammar records it for text search and narrows every other read to
// video, the v1 grammar keeps dropping it, and sources without text search
// refuse it.
func TestParseCatalogRequest_VideoWithEpisodesScope(t *testing.T) {
	v2 := CatalogRequestOptions{SearchMediaScopes: true}
	req, err := ParseCatalogRequestWithOptions(url.Values{
		"source": {"query"},
		"q":      {"the rookie"},
		"type":   {" Video_With_Episodes "},
	}, v2)
	if err != nil {
		t.Fatalf("ParseCatalogRequestWithOptions: %v", err)
	}
	if req.Query.MediaScope != MediaScopeVideo || req.SearchMediaScope != MediaScopeVideoWithEpisodes {
		t.Fatalf("scopes = query %q search %q, want video and video_with_episodes", req.Query.MediaScope, req.SearchMediaScope)
	}
	if err := validateCatalogQueryRequest(req, true); err != nil {
		t.Fatalf("validateCatalogQueryRequest: %v", err)
	}
	_, itemTypes, earlyEmpty := catalogSearchAccess(req, AccessFilter{})
	if earlyEmpty || !reflect.DeepEqual(itemTypes, []string{"movie", "series", "episode"}) {
		t.Fatalf("search item types = %v (early empty %v), want movie+series+episode", itemTypes, earlyEmpty)
	}

	browse, err := ParseCatalogRequestWithOptions(url.Values{"type": {"video_with_episodes"}}, v2)
	if err != nil {
		t.Fatalf("browse parse: %v", err)
	}
	if browse.Query.MediaScope != MediaScopeVideo || browse.SearchQuery != "" {
		t.Fatalf("browse scope = %q, want video", browse.Query.MediaScope)
	}

	v1, err := ParseCatalogRequest(url.Values{"q": {"the rookie"}, "type": {"video_with_episodes"}})
	if err != nil {
		t.Fatalf("v1 parse: %v", err)
	}
	if v1.Query.MediaScope != "" || v1.SearchMediaScope != "" {
		t.Fatalf("v1 grammar accepted the search-only scope: query %q search %q", v1.Query.MediaScope, v1.SearchMediaScope)
	}

	for _, source := range []string{"favorites", "watchlist", "history", "user_collection", "section"} {
		values := url.Values{"source": {source}, "q": {"x"}, "type": {"video_with_episodes"}, "collection_id": {"7"}, "scope": {"home"}, "section_id": {"s"}}
		if _, err := ParseCatalogRequestWithOptions(values, v2); !errors.Is(err, ErrSearchMediaScopeSource) {
			t.Fatalf("source %s accepted the search-only scope (err %v)", source, err)
		}
	}
}

// TestResolveDirectSearchSource_VideoWithEpisodesScope asserts the search
// provider receives the episode item type and an unscoped definition, so the
// episode branch of a scoped search is not emptied by the browse scope.
func TestResolveDirectSearchSource_VideoWithEpisodesScope(t *testing.T) {
	provider := &fakeSearchProvider{result: &CatalogSearchResult{Items: []*models.MediaItem{}}}
	resolver := &CatalogResolver{searchProvider: provider}
	req, err := ParseCatalogRequestWithOptions(url.Values{"q": {"dune"}, "type": {"video_with_episodes"}}, CatalogRequestOptions{SearchMediaScopes: true})
	if err != nil {
		t.Fatal(err)
	}
	req.CursorPaging = true
	if _, err := resolver.resolveDirectSearchSource(context.Background(), req, AccessFilter{}); err != nil {
		t.Fatalf("resolveDirectSearchSource: %v", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(provider.requests))
	}
	got := provider.requests[0]
	if !reflect.DeepEqual(got.ItemTypes, []string{"movie", "series", "episode"}) || got.Definition.MediaScope != "" {
		t.Fatalf("provider request item types %v definition scope %q", got.ItemTypes, got.Definition.MediaScope)
	}
}
