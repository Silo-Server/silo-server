package recipes

import (
	"encoding/json"
	"testing"
)

func TestFormatShowcaseValidatesFormat(t *testing.T) {
	rec, _ := Get("format_showcase")

	validCases := []json.RawMessage{
		nil,
		json.RawMessage(`{}`),
		json.RawMessage(`{"format":"4k"}`),
		json.RawMessage(`{"format":"dolby_vision"}`),
		json.RawMessage(`{"format":"hdr"}`),
		json.RawMessage(`{"format":""}`),
	}
	for _, raw := range validCases {
		if err := rec.Validate(raw); err != nil {
			t.Errorf("valid params %s rejected: %v", raw, err)
		}
	}

	bad := json.RawMessage(`{"format":"8k"}`)
	if err := rec.Validate(bad); err == nil {
		t.Errorf("invalid format should be rejected")
	}
}
