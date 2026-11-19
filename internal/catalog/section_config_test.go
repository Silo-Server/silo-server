package catalog

import (
	"encoding/json"
	"slices"
	"testing"
)

// A row and its See All page read a section config's scope with this one
// parser. Configs that mix a structured media_scope with legacy keys decide
// legacy by whether a legacy key holds a value.
func TestParseSectionConfigScope(t *testing.T) {
	for _, tc := range []struct {
		config    string
		scope     string
		libraries []int
	}{
		{`{"media_scope":"video","library_ids":[4]}`, "video", []int{4}},
		{`{"media_scope":"movie","filter_type":""}`, "movie", nil},
		{`{"media_scope":"movie","filter_library_ids":[]}`, "movie", nil},
		{`{"media_scope":"movie","order":"desc"}`, "", nil},
		{`{"filter_type":"Series","filter_library_id":3}`, "series", []int{3}},
		{`{"filter_type":"movie","match":"all","groups":[],"sort":"title","order":"asc"}`, "movie", nil},
	} {
		got := ParseSectionConfigScope(json.RawMessage(tc.config))
		if got.MediaScope != tc.scope || !slices.Equal(got.LibraryIDs, tc.libraries) {
			t.Errorf("ParseSectionConfigScope(%s) = %+v, want scope %q libraries %v", tc.config, got, tc.scope, tc.libraries)
		}
	}
}
