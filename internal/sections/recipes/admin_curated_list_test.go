package recipes

import (
	"encoding/json"
	"testing"
)

func TestAdminCuratedListRequiresItems(t *testing.T) {
	rec, _ := Get("admin_curated_list")
	if err := rec.Validate(json.RawMessage(`{"item_ids":[]}`)); err == nil {
		t.Error("expected error for empty item_ids")
	}
	if err := rec.Validate(json.RawMessage(`{"item_ids":["a","b","c"]}`)); err != nil {
		t.Errorf("good config rejected: %v", err)
	}
	if err := rec.Validate(json.RawMessage(``)); err == nil {
		t.Error("expected error for empty raw")
	}
	if err := rec.Validate(json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing item_ids")
	}
}
