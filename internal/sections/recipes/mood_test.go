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

func TestMoodPresetDescriptionsStateTheRule(t *testing.T) {
	want := map[string]string{
		"mood_feel_good": "Comedy or Family, rated 6.5+ on TMDB with 100+ votes.",
		"mood_comfort":   "Comedy, Romance or Family, rated 6.0+ on TMDB with 100+ votes.",
	}
	for _, p := range (moodRecipe{}).Definition().Presets {
		if w, ok := want[p.Key]; ok && p.DescriptionShort != w {
			t.Errorf("%s description = %q, want %q", p.Key, p.DescriptionShort, w)
		}
		if p.DescriptionShort == p.DisplayName {
			t.Errorf("%s description repeats its name %q", p.Key, p.DisplayName)
		}
	}
}
