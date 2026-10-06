package recipes

import (
	"encoding/json"
	"testing"
)

func TestCustomFilterValidatesFilterShape(t *testing.T) {
	rec, _ := Get("custom_filter")
	good := json.RawMessage(`{"filter":{"match":"all","groups":[]}}`)
	if err := rec.Validate(good); err != nil {
		t.Errorf("good config rejected: %v", err)
	}
	bad := json.RawMessage(`{"filter":42}`)
	if err := rec.Validate(bad); err == nil {
		t.Errorf("bad config accepted")
	}
}
