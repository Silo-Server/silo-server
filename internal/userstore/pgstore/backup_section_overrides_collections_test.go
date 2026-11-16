package pgstore

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// A profile's row of collections maps each collection it lists, in order,
// and loses the ones with no counterpart here; it maps while one is left.
func TestRowOfCollectionsOverrideMapsItsList(t *testing.T) {
	target := userdata.Target{Collections: func(_ context.Context, _ string, ids []string) (map[string]string, error) {
		out := map[string]string{}
		for _, id := range ids {
			if id != "gone" {
				out[id] = "here-" + id
			}
		}
		return out, nil
	}}
	got, ok, err := remapCollections(t.Context(), target, `{"collections":[{"user_collection_id":"a"},{"library_collection_id":"gone"},{"library_collection_id":"b"}]}`)
	if err != nil || !ok {
		t.Fatalf("remap = %v, %v", ok, err)
	}
	var doc struct {
		Collections []map[string]string `json:"collections"`
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	want := []map[string]string{{"user_collection_id": "here-a"}, {"library_collection_id": "here-b"}}
	if !reflect.DeepEqual(doc.Collections, want) {
		t.Errorf("mapped list = %v, want %v", doc.Collections, want)
	}
	if _, ok, err := remapCollections(t.Context(), target, `{"collections":[{"library_collection_id":"gone"}]}`); err != nil || ok {
		t.Errorf("a row left with no collection mapped: %v, %v", ok, err)
	}
}
