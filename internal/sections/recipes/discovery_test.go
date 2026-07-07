package recipes

import (
	"encoding/json"
	"testing"
)

func TestHiddenGemsRecipeRegistered(t *testing.T) {
	rec, ok := Get("hidden_gems")
	if !ok {
		t.Fatal("hidden_gems not registered")
	}
	if rec.Definition().Category != CategoryDiscovery {
		t.Errorf("category = %v want discovery", rec.Definition().Category)
	}
	if !rec.Definition().AvoidDuplicates {
		t.Errorf("hidden_gems should opt into AvoidDuplicates")
	}
}

func TestHiddenGemsAcceptsParams(t *testing.T) {
	rec, _ := Get("hidden_gems")
	good := json.RawMessage(`{"min_rating":7.5,"max_play_count":2}`)
	if err := rec.Validate(good); err != nil {
		t.Errorf("good params rejected: %v", err)
	}
}

func TestCriticallyAcclaimedRecipeRegistered(t *testing.T) {
	rec, ok := Get("critically_acclaimed")
	if !ok {
		t.Fatal("critically_acclaimed not registered")
	}
	if rec.Definition().Category != CategoryDiscovery {
		t.Errorf("category = %v want discovery", rec.Definition().Category)
	}
	if !rec.Definition().AvoidDuplicates {
		t.Errorf("critically_acclaimed should opt into AvoidDuplicates")
	}
	presets := rec.Definition().Presets
	if len(presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(presets))
	}
	if presets[0].Key != "ca_imdb" {
		t.Errorf("preset key = %q want ca_imdb", presets[0].Key)
	}
}

func TestCriticallyAcclaimedAcceptsParams(t *testing.T) {
	rec, _ := Get("critically_acclaimed")
	good := json.RawMessage(`{"min_score":8.0,"source":"imdb"}`)
	if err := rec.Validate(good); err != nil {
		t.Errorf("good params rejected: %v", err)
	}
}

func TestAwardWinnersRecipeRegistered(t *testing.T) {
	rec, ok := Get("award_winners")
	if !ok {
		t.Fatal("award_winners not registered")
	}
	if rec.Definition().Category != CategoryDiscovery {
		t.Errorf("category = %v want discovery", rec.Definition().Category)
	}
	if !rec.Definition().AvoidDuplicates {
		t.Errorf("award_winners should opt into AvoidDuplicates")
	}
	presets := rec.Definition().Presets
	if len(presets) != 3 {
		t.Fatalf("expected 3 presets (oscar, emmy, cannes), got %d", len(presets))
	}
	keys := map[string]bool{"aw_oscar": false, "aw_emmy": false, "aw_cannes": false}
	for _, p := range presets {
		keys[p.Key] = true
	}
	for k, found := range keys {
		if !found {
			t.Errorf("preset %q missing", k)
		}
	}
}

func TestForgottenFavoritesRecipeRegistered(t *testing.T) {
	rec, ok := Get("forgotten_favorites")
	if !ok {
		t.Fatal("forgotten_favorites not registered")
	}
	if rec.Definition().Category != CategoryDiscovery {
		t.Errorf("category = %v want discovery", rec.Definition().Category)
	}
	if !rec.Definition().AvoidDuplicates {
		t.Errorf("forgotten_favorites should opt into AvoidDuplicates")
	}
	presets := rec.Definition().Presets
	if len(presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(presets))
	}
	if presets[0].Key != "ff_default" {
		t.Errorf("preset key = %q want ff_default", presets[0].Key)
	}
}

func TestForgottenFavoritesAcceptsParams(t *testing.T) {
	rec, _ := Get("forgotten_favorites")
	good := json.RawMessage(`{"lookback_days":365}`)
	if err := rec.Validate(good); err != nil {
		t.Errorf("good params rejected: %v", err)
	}
}

func TestFormatShowcaseRecipeRegistered(t *testing.T) {
	if _, ok := Get("format_showcase"); !ok {
		t.Fatal("format_showcase not registered")
	}
}

func TestFormatShowcaseRecipeDefinition(t *testing.T) {
	rec, ok := Get("format_showcase")
	if !ok {
		t.Fatal("format_showcase not registered")
	}
	def := rec.Definition()
	if def.Category != CategoryDiscovery {
		t.Errorf("category = %v want discovery", def.Category)
	}
	if def.AvoidDuplicates {
		t.Errorf("format_showcase should not set AvoidDuplicates")
	}
	if len(def.Presets) != 4 {
		t.Fatalf("expected 4 presets, got %d", len(def.Presets))
	}
	keys := map[string]bool{"fs_4k": false, "fs_4k_recent": false, "fs_dv": false, "fs_hdr": false}
	for _, p := range def.Presets {
		keys[p.Key] = true
	}
	for k, found := range keys {
		if !found {
			t.Errorf("preset %q missing", k)
		}
	}
}

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
