package recipes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCollectionPostersValidate(t *testing.T) {
	rec, ok := Get("collection_posters")
	if !ok {
		t.Fatal("collection_posters is not registered")
	}
	tooMany := make([]string, MaxCollectionPosters+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf(`{"library_collection_id":"c%d"}`, i)
	}
	cases := []struct {
		name   string
		config string
		ok     bool
	}{
		{"empty config", ``, false},
		{"empty list", `{"collections":[]}`, false},
		{"over the limit", `{"collections":[` + strings.Join(tooMany, ",") + `]}`, false},
		{"duplicate", `{"collections":[{"library_collection_id":"a"},{"library_collection_id":"a"}]}`, false},
		{"both ids in one entry", `{"collections":[{"library_collection_id":"a","user_collection_id":"b"}]}`, false},
		{"no id in an entry", `{"collections":[{}]}`, false},
		{"server and personal", `{"collections":[{"library_collection_id":"a"},{"user_collection_id":"a"}]}`, true},
		{"at the limit", `{"collections":[` + strings.Join(tooMany[:MaxCollectionPosters], ",") + `]}`, true},
	}
	for _, tc := range cases {
		err := rec.Validate(json.RawMessage(tc.config))
		if (err == nil) != tc.ok {
			t.Errorf("%s: Validate = %v, want ok %v", tc.name, err, tc.ok)
		}
	}
}
