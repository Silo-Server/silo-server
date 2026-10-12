package jellycompat

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseItemsQuery_ImageTypesBackdropFilter(t *testing.T) {
	codec := NewResourceIDCodec()
	cases := []struct {
		name        string
		rawQuery    string
		wantRequire bool
	}{
		{"backdrop requested", "ImageTypes=Backdrop", true},
		{"lowercase param and value", "imagetypes=backdrop", true},
		{"backdrop among several", "ImageTypes=Primary,Backdrop,Logo", true},
		{"bracket array variant", "ImageTypes[]=Backdrop", true},
		{"primary only does not filter", "ImageTypes=Primary", false},
		{"absent does not filter", "Limit=1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/Items?"+tc.rawQuery, nil)
			query := parseItemsQuery(req, codec)
			if query.requireBackdrop != tc.wantRequire {
				t.Fatalf("requireBackdrop = %v, want %v", query.requireBackdrop, tc.wantRequire)
			}
			params := buildBrowseParams(query)
			gotParam := params.Get("require_backdrop") == "true"
			if gotParam != tc.wantRequire {
				t.Fatalf("require_backdrop param = %q, want emitted=%v", params.Get("require_backdrop"), tc.wantRequire)
			}
		})
	}
}

// Jellyfin Web sends EnableImageTypes once per type
// (enableImageTypes=Thumb&enableImageTypes=Backdrop); every value counts, as
// in the comma-separated form.
func TestParseItemsQuery_EnableImageTypesRepeated(t *testing.T) {
	codec := NewResourceIDCodec()
	cases := []struct {
		name     string
		rawQuery string
		want     map[string]bool
	}{
		{"repeated", "enableImageTypes=Thumb&enableImageTypes=Backdrop", map[string]bool{"thumb": true, "backdrop": true}},
		{"comma separated", "EnableImageTypes=Primary,Backdrop", map[string]bool{"primary": true, "backdrop": true}},
		{"repeated and comma separated", "EnableImageTypes=Primary,Logo&EnableImageTypes=Backdrop", map[string]bool{"primary": true, "logo": true, "backdrop": true}},
		{"absent", "Limit=1", nil},
		{"empty", "EnableImageTypes=", nil},
		{"only separators", "EnableImageTypes=,%20&EnableImageTypes=", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := parseItemsQuery(httptest.NewRequest(http.MethodGet, "/Items?"+tc.rawQuery, nil), codec)
			if !maps.Equal(query.enableImageTypes, tc.want) || (tc.want == nil) != (query.enableImageTypes == nil) {
				t.Fatalf("enableImageTypes = %v, want %v", query.enableImageTypes, tc.want)
			}
		})
	}

	// With both types enabled, an item keeps its backdrop tags when it has no
	// thumb, which is what Jellyfin Web falls back to.
	items := []baseItemDTO{{ImageTags: map[string]string{"Primary": "p"}, BackdropImageTags: []string{"b"}}}
	applyItemsResponseOptions(items, parseItemsQuery(httptest.NewRequest(http.MethodGet, "/Items?imageTypeLimit=1&enableImageTypes=Thumb&enableImageTypes=Backdrop", nil), codec))
	if len(items[0].BackdropImageTags) != 1 {
		t.Fatalf("BackdropImageTags = %q, want the backdrop kept", items[0].BackdropImageTags)
	}
	if _, ok := items[0].ImageTags["Primary"]; ok {
		t.Fatalf("ImageTags = %v, want Primary stripped", items[0].ImageTags)
	}
}
