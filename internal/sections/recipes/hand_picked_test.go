package recipes

import (
	"encoding/json"
	"testing"
)

func TestCollectionRequiresLibraryCollectionID(t *testing.T) {
	rec, _ := Get("collection")
	if err := rec.Validate(json.RawMessage(`{}`)); err == nil {
		t.Error("expected validation error when library_collection_id missing")
	}
	good := json.RawMessage(`{"library_collection_id":"42"}`)
	if err := rec.Validate(good); err != nil {
		t.Errorf("good config rejected: %v", err)
	}
}
