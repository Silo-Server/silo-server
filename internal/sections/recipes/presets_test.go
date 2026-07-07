package recipes

import "testing"

// presetsRequiringInput lists preset keys whose DefaultParams are intentionally
// incomplete: the add-section drawer blocks saving until the admin supplies
// the missing value (a collection id, curated item list, ...). Every other
// preset must ship defaults that pass its own recipe's Validate — a preset
// that fails validation out of the box is un-addable from the gallery.
var presetsRequiringInput = map[string]bool{
	"collection_pick":          true, // collection: requires picking a collection
	"trakt_trending_movies":    true, // collection: Trakt sync target chosen/created at add time
	"trakt_trending_shows":     true,
	"trakt_popular_movies":     true,
	"trakt_popular_shows":      true,
	"trakt_recommended_movies": true,
	"trakt_recommended_shows":  true,
	"acl_blank":                true, // admin_curated_list: drawer requires at least one item
}

func TestAllGalleryPresetDefaultsValidate(t *testing.T) {
	for _, rec := range List() {
		def := rec.Definition()
		if def.Hidden {
			continue
		}
		for _, preset := range def.Presets {
			if presetsRequiringInput[preset.Key] {
				continue
			}
			if err := rec.Validate(preset.DefaultParams); err != nil {
				t.Errorf("recipe %s preset %s: default params fail validation: %v", def.Type, preset.Key, err)
			}
		}
	}
}

// TestHiddenRecipesStayResolvable pins the contract that Hidden recipes remain
// registered (existing saved sections keep resolving) while the gallery omits
// them.
func TestHiddenRecipesStayResolvable(t *testing.T) {
	for _, typ := range []string{"award_winners", "genre"} {
		rec, ok := Get(typ)
		if !ok {
			t.Errorf("hidden recipe %s must stay registered", typ)
			continue
		}
		if !rec.Definition().Hidden {
			t.Errorf("recipe %s expected Hidden", typ)
		}
	}
}
