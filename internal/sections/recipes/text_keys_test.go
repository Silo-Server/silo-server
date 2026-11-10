package recipes

import (
	"strings"
	"testing"
	"time"
)

// Every gallery preset sends a name and a one-line description key, a long
// description key exactly when it has one, and no two presets share a key
// unless the catalog already names both by the same section title.
func TestPresetTextKeysCoverEveryPreset(t *testing.T) {
	seen := map[string]string{}
	for _, rec := range List() {
		for _, p := range rec.Definition().Presets {
			keys := PresetTextKeys(p)
			if keys.DisplayName == "" || keys.DescriptionShort == "" {
				t.Errorf("preset %s has no name or description key: %+v", p.Key, keys)
			}
			if (keys.DescriptionLong != "") != (p.DescriptionLong != "") {
				t.Errorf("preset %s: long description %q, key %q", p.Key, p.DescriptionLong, keys.DescriptionLong)
			}
			for _, key := range []string{keys.DisplayName, keys.DescriptionShort, keys.DescriptionLong} {
				if key == "" {
					continue
				}
				if !strings.HasPrefix(key, "home.recipe.") && !strings.HasPrefix(key, "home.section.") {
					t.Errorf("preset %s key %q is outside the shared namespaces", p.Key, key)
				}
				if other, dup := seen[key]; dup {
					t.Errorf("preset %s and %s share key %q", p.Key, other, key)
				}
				seen[key] = p.Key
			}
		}
	}
}

// A seasonal row's built-in title is keyed; a title an admin typed, for a
// theme or a custom holiday, is not.
func TestSeasonalPickKeysOnlyBuiltInTitles(t *testing.T) {
	now := time.Date(2026, time.December, 20, 12, 0, 0, 0, time.UTC)
	pick, ok := ActiveSeasonalPick(SeasonalThemedParams{EnabledThemes: []string{ThemeChristmas}}, now)
	if !ok || pick.Title == "" || pick.TitleKey != seasonalVibeTitleKey(ThemeChristmas, pick.Vibe) {
		t.Fatalf("built-in pick = %+v", pick)
	}
	renamed, ok := ActiveSeasonalPick(SeasonalThemedParams{EnabledThemes: []string{ThemeChristmas}, ThemeTitles: map[string]string{ThemeChristmas: "Our Christmas"}}, now)
	if !ok || renamed.Title != "Our Christmas" || renamed.TitleKey != "" {
		t.Fatalf("renamed pick = %+v", renamed)
	}
	for _, theme := range SeasonalThemes {
		for _, vibe := range theme.Vibes {
			if vibe.Title != "" && strings.Count(seasonalVibeTitleKey(theme.Key, vibe.Key), ".") != 4 {
				t.Errorf("%s/%s key %q", theme.Key, vibe.Key, seasonalVibeTitleKey(theme.Key, vibe.Key))
			}
		}
	}
}
