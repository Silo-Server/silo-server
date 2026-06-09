package jellycompat

import (
	"encoding/json"
	"testing"
)

func TestDefaultDisplayPreferencesIncludesRequiredImageDimensions(t *testing.T) {
	dto := defaultDisplayPreferences("default", "Wholphin")

	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal default display preferences: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal default display preferences: %v", err)
	}

	if _, ok := raw["PrimaryImageHeight"]; !ok {
		t.Fatal("PrimaryImageHeight missing from display preferences JSON")
	}
	if _, ok := raw["PrimaryImageWidth"]; !ok {
		t.Fatal("PrimaryImageWidth missing from display preferences JSON")
	}
}
