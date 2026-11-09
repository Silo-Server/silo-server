package catalog

import (
	"encoding/json"
	"testing"
)

func TestPinSmartRulesToCollectionLibraries(t *testing.T) {
	cases := []struct {
		name      string
		rules     string
		libraries []int
		want      string
	}{
		{name: "no libraries named takes the collection's", rules: `{"match":"all"}`, libraries: []int{2, 1}, want: `{"library_ids":[1,2],"match":"all"}`},
		{name: "JSON null", rules: `null`, libraries: []int{3}, want: `{"library_ids":[3]}`},
		{name: "empty", rules: ``, libraries: []int{3}, want: `{"library_ids":[3]}`},
		{name: "wider rules narrow to the overlap", rules: `{"library_ids":[3,1,2]}`, libraries: []int{1, 2}, want: `{"library_ids":[1,2]}`},
		{name: "no overlap stays as it is", rules: `{"library_ids":[3]}`, libraries: []int{1}, want: `{"library_ids":[3]}`},
		{name: "no collection libraries stays as it is", rules: `{}`, want: `{}`},
		{name: "not an object stays as it is", rules: `[1]`, libraries: []int{1}, want: `[1]`},
		{name: "legacy filter with no libraries", rules: `{"filter_type":"movie"}`, libraries: []int{2}, want: `{"filter_library_ids":[2],"filter_type":"movie","library_ids":[2]}`},
		{name: "legacy filter libraries narrow", rules: `{"filter_library_id":1,"filter_library_ids":[3]}`, libraries: []int{1, 2}, want: `{"filter_library_ids":[1],"library_ids":[1]}`},
		{name: "legacy filter outside the collection's libraries names its own", rules: `{"filter_type":"movie","filter_library_ids":[5,3]}`, libraries: []int{1}, want: `{"filter_library_ids":[3,5],"filter_type":"movie","library_ids":[3,5]}`},
		{name: "ids that are not whole numbers read as none", rules: `{"library_ids":[1.5,-1]}`, libraries: []int{4}, want: `{"library_ids":[4]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PinSmartRulesToCollectionLibraries(json.RawMessage(tc.rules), tc.libraries)
			if string(got) != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
