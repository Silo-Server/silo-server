package recipes

import "strings"

// PresetText is the shared translation catalog keys of a gallery preset's
// text. Each key's English is the preset's DisplayName, DescriptionShort or
// DescriptionLong, so a client that has the key shows its translation and
// otherwise the English. A key is empty when the preset has no such text.
//
// Changing a preset's English means changing the catalog's English for its
// keys in silo-translations in the same release.
type PresetText struct {
	DisplayName      string
	DescriptionShort string
	DescriptionLong  string
}

// presetNameKeys are the presets whose name is a section title the catalog
// already has under home.section; every other preset's name is
// home.recipe.<key>.name.
var presetNameKeys = map[string]string{
	"bcw_auto":                   "home.section.becauseYouWatched",
	"continue_listening_default": "home.section.continueListening",
	"continue_watching_default":  "home.section.continueWatching",
	"hidden_gems_default":        "home.section.hiddenGems",
	"next_up_default":            "home.section.nextUp",
	"recently_added_default":     "home.section.recentlyAdded",
	"returning_shows_default":    "home.section.returningShows",
	"se_auto":                    "home.section.seasonalPicks",
	"similar":                    "home.section.profilesLikeYouEnjoyed",
	"taste_top":                  "home.section.topPicksToday",
	"tr_7d":                      "home.section.trendingThisWeek",
}

// PresetTextKeys returns the catalog keys of preset's text. Not part of
// GalleryPreset's JSON: the frozen v1 gallery sends only the English.
func PresetTextKeys(p GalleryPreset) PresetText {
	base := "home.recipe." + lowerCamel(p.Key)
	out := PresetText{DisplayName: presetNameKeys[p.Key], DescriptionShort: base + ".description"}
	if out.DisplayName == "" {
		out.DisplayName = base + ".name"
	}
	if p.DescriptionLong != "" {
		out.DescriptionLong = base + ".details"
	}
	return out
}

// lowerCamel turns a snake_case key into a lowerCamel catalog key segment.
func lowerCamel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// seasonalVibeTitleKey is the catalog key of a built-in vibe's in-season
// title: home.section.seasonal.<theme>.<vibe>.
func seasonalVibeTitleKey(theme, vibe string) string {
	return "home.section.seasonal." + lowerCamel(theme) + "." + lowerCamel(vibe)
}
