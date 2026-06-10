package catalog

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
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
}
