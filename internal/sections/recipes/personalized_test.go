package recipes

import (
	"encoding/json"
	"testing"
)

func TestBecauseYouWatchedAcceptsAnchorParam(t *testing.T) {
	rec, _ := Get("because_you_watched")
	good := json.RawMessage(`{"anchor_item_id":"abc123"}`)
	if err := rec.Validate(good); err != nil {
		t.Fatalf("validate good: %v", err)
	}
	auto := json.RawMessage(`{"anchor_item_id":""}`)
	if err := rec.Validate(auto); err != nil {
		t.Fatalf("validate empty anchor (auto): %v", err)
	}
}
