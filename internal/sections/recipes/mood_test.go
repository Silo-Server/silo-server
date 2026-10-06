package recipes

import (
	"encoding/json"
	"testing"
)

func TestMoodValidatesKnownMoods(t *testing.T) {
	rec, _ := Get("mood_collection")
	if err := rec.Validate(json.RawMessage(`{"mood":"feel_good"}`)); err != nil {
		t.Errorf("feel_good rejected: %v", err)
	}
	if err := rec.Validate(json.RawMessage(`{"mood":"made_up"}`)); err == nil {
		t.Error("expected error for unknown mood")
	}
}

func TestMoodRequiresMood(t *testing.T) {
	rec, _ := Get("mood_collection")
	if err := rec.Validate(json.RawMessage(``)); err == nil {
		t.Error("expected error for empty raw")
	}
	if err := rec.Validate(json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing mood")
	}
	if err := rec.Validate(json.RawMessage(`{"mood":""}`)); err == nil {
		t.Error("expected error for empty mood string")
	}
}
