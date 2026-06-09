package jellycompat

import (
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
