package recipes

import (
	"encoding/json"
	"testing"
)

func TestLibraryStaplesAcceptEmptyParams(t *testing.T) {
	for _, typ := range []string{"recently_added", "continue_watching", "random"} {
		rec, _ := Get(typ)
		if err := rec.Validate(json.RawMessage(`{}`)); err != nil {
			t.Errorf("%s.Validate({}) = %v", typ, err)
		}
	}
}

func TestContinueWatchingRecipeExposesWatchingAndListeningPresets(t *testing.T) {
	rec, ok := Get("continue_watching")
	if !ok {
		t.Fatal("continue_watching recipe not registered")
	}
	presets := rec.Definition().Presets
	if len(presets) != 2 {
		t.Fatalf("presets len = %d, want 2", len(presets))
	}
	if presets[0].DisplayName != "Continue Watching" || string(presets[0].DefaultParams) != `{"continue_type":"watching"}` {
		t.Fatalf("watching preset = %+v", presets[0])
	}
	if presets[1].DisplayName != "Continue Listening" || string(presets[1].DefaultParams) != `{"continue_type":"listening"}` {
		t.Fatalf("listening preset = %+v", presets[1])
	}
}

func TestContinueWatchingRecipeRejectsUnknownContinueType(t *testing.T) {
	rec, _ := Get("continue_watching")
	if err := rec.Validate(json.RawMessage(`{"continue_type":"scrolling"}`)); err == nil {
		t.Fatal("Validate accepted unknown continue_type")
	}
}
