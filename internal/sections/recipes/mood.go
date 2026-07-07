package recipes

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// moodInfo describes a single mood preset.
type moodInfo struct {
	Key       string
	Label     string
	Icon      string
	GenresAny []string
	MinRating float64
}

// Moods is the canonical ordered list. The order here drives the gallery preset order.
var Moods = []moodInfo{
	{Key: "feel_good", Label: "Feel-Good Comedies", Icon: "😄", GenresAny: []string{"Comedy", "Family"}, MinRating: 6.5},
	{Key: "mind_bending", Label: "Mind-Bending Sci-Fi", Icon: "🌀", GenresAny: []string{"Science Fiction", "Mystery"}, MinRating: 7.0},
	{Key: "comfort", Label: "Comfort Rewatches", Icon: "🛋️", GenresAny: []string{"Comedy", "Romance", "Family"}, MinRating: 6.0},
	{Key: "edge_of_seat", Label: "Edge of Your Seat", Icon: "😬", GenresAny: []string{"Thriller", "Action"}, MinRating: 6.5},
	{Key: "tearjerker", Label: "Tearjerkers", Icon: "😢", GenresAny: []string{"Drama", "Romance"}, MinRating: 7.0},
	{Key: "quiet_sunday", Label: "Quiet Sunday Cinema", Icon: "☕", GenresAny: []string{"Drama", "Documentary"}, MinRating: 7.0},
	{Key: "date_night", Label: "Date Night", Icon: "💕", GenresAny: []string{"Romance", "Comedy"}, MinRating: 6.0},
	{Key: "after_midnight", Label: "After Midnight", Icon: "🌙", GenresAny: []string{"Horror", "Thriller"}, MinRating: 6.0},
}

// MoodByKey looks up a mood by its key. Returns the mood and whether it exists.
func MoodByKey(key string) (moodInfo, bool) {
	for _, m := range Moods {
		if m.Key == key {
			return m, true
		}
	}
	return moodInfo{}, false
}

// MoodCollectionParams configures the mood_collection resolver.
//
// Intensity is reserved for future use (low | med | high). It will eventually
// adjust MinRating or genre weighting, but is currently ignored — persisting
// it in the section config is safe and forward-compatible.
type MoodCollectionParams struct {
	Mood      string `json:"mood"`
	Intensity string `json:"intensity,omitempty"` // low | med | high
}

type moodRecipe struct{}

func (moodRecipe) Type() string                   { return "mood_collection" }
func (moodRecipe) NewParams() any                 { return &MoodCollectionParams{} }
func (moodRecipe) DefaultCacheTTL() time.Duration { return 12 * time.Hour }

func (moodRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("mood_collection", rc)
}

func (moodRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("mood_collection: mood is required")
	}
	var p MoodCollectionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.Mood == "" {
		return errors.New("mood_collection: mood is required")
	}
	if _, ok := MoodByKey(p.Mood); !ok {
		return fmt.Errorf("mood_collection: unknown mood %q", p.Mood)
	}
	return nil
}

func (moodRecipe) Definition() RecipeDefinition {
	presets := make([]GalleryPreset, 0, len(Moods))
	for _, m := range Moods {
		params, _ := json.Marshal(MoodCollectionParams{Mood: m.Key})
		presets = append(presets, GalleryPreset{
			Key:              "mood_" + m.Key,
			DisplayName:      m.Label,
			Icon:             m.Icon,
			DescriptionShort: m.Label,
			DefaultParams:    json.RawMessage(params),
		})
	}
	return RecipeDefinition{
		Type:             "mood_collection",
		Category:         CategoryMood,
		SupportsRotation: false,
		AvoidDuplicates:  true,
		Presets:          presets,
	}
}

// ShortWatchesParams configures short_watches: movies that fit in a short
// evening slot, capped by runtime.
type ShortWatchesParams struct {
	MaxMinutes int     `json:"max_minutes,omitempty"` // default 95
	MinRating  float64 `json:"min_rating,omitempty"`  // default 6.0
}

type shortWatchesRecipe struct{}

func (shortWatchesRecipe) Type() string                   { return "short_watches" }
func (shortWatchesRecipe) NewParams() any                 { return &ShortWatchesParams{} }
func (shortWatchesRecipe) DefaultCacheTTL() time.Duration { return 6 * time.Hour }
func (shortWatchesRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("short_watches", rc)
}
func (shortWatchesRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p ShortWatchesParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.MaxMinutes < 0 {
		return errors.New("short_watches: max_minutes must be >= 0")
	}
	return nil
}
func (shortWatchesRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:            "short_watches",
		Category:        CategoryMood,
		AvoidDuplicates: true,
		Presets: []GalleryPreset{
			{
				Key:              "short_watches_default",
				DisplayName:      "Short & Sweet",
				Icon:             "⏱️",
				DescriptionShort: "Well-rated movies under 95 minutes.",
				DefaultParams:    json.RawMessage(`{"max_minutes":95}`),
			},
		},
	}
}

func init() {
	Register(moodRecipe{})
	Register(shortWatchesRecipe{})
}
