package recipes

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"time"
)

// RotationCadence controls how often the editorial spotlight rotates.
type RotationCadence string

const (
	CadenceDaily   RotationCadence = "daily"
	CadenceWeekly  RotationCadence = "weekly"
	CadenceMonthly RotationCadence = "monthly"
)

// bucketTime converts a timestamp to a stable bucket key based on the cadence.
// Daily cadence (cadenceDays <= 1) uses the unix-day index. Monthly cadence
// (cadenceDays >= 28) uses calendar year*100+month. All other cadences use
// ISO year*100+week so spotlights advance cleanly on ISO-week boundaries.
func bucketTime(now time.Time, cadenceDays int) int64 {
	if cadenceDays <= 1 {
		return now.Unix() / 86400
	}
	if cadenceDays >= 28 {
		return int64(now.Year())*100 + int64(now.Month())
	}
	year, week := now.ISOWeek()
	return int64(year)*100 + int64(week)
}

// RotationIndex returns a deterministic index in [0, count) for the given
// timestamp, subject key, candidate count, and bucket size in days.
// When days is 0, weekly (7-day) bucketing is used.
func RotationIndex(t time.Time, key string, count int, days int) int {
	if count <= 0 {
		return 0
	}
	bucket := bucketTime(t, days)
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s|%d", key, bucket)
	return int(h.Sum64() % uint64(count))
}

// EditorialSpotlightParams configures the editorial_spotlight resolver.
//
// LibraryID is reserved for future use: the section-level libraryID passed
// through fetchSection currently takes precedence and config-level pinning
// is not yet honored. Persisting LibraryID in the section config is safe —
// it will be picked up when the wiring lands.
type EditorialSpotlightParams struct {
	SubjectType     string          `json:"subject_type"`
	Subject         string          `json:"subject,omitempty"`
	AutoRotate      bool            `json:"auto_rotate,omitempty"`
	RotationCadence RotationCadence `json:"rotation_cadence,omitempty"`
	LibraryID       *int            `json:"library_id,omitempty"`
}

// Note: "franchise" is intentionally absent — the fetcher has no franchise
// data source yet, so accepting it would validate configs that can never
// resolve. Re-add once franchise/collection grouping data lands.
var validSubjectTypes = map[string]bool{
	"director": true,
	"studio":   true,
	"actor":    true,
	"era":      true,
}

type editorialSpotlightRecipe struct{}

func (editorialSpotlightRecipe) Type() string                   { return "editorial_spotlight" }
func (editorialSpotlightRecipe) NewParams() any                 { return &EditorialSpotlightParams{} }
func (editorialSpotlightRecipe) DefaultCacheTTL() time.Duration { return 24 * time.Hour }

func (editorialSpotlightRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("editorial_spotlight", rc)
}

func (editorialSpotlightRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("editorial_spotlight: subject_type is required")
	}
	var p EditorialSpotlightParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.SubjectType == "" {
		return errors.New("editorial_spotlight: subject_type is required")
	}
	if !validSubjectTypes[p.SubjectType] {
		return fmt.Errorf("editorial_spotlight: unknown subject_type %q", p.SubjectType)
	}

	if err := oneOf("editorial_spotlight: rotation_cadence", string(p.RotationCadence),
		"", string(CadenceDaily), string(CadenceWeekly), string(CadenceMonthly)); err != nil {
		return err
	}

	if !p.AutoRotate && p.Subject == "" {
		return errors.New("editorial_spotlight: subject is required when auto_rotate is false")
	}

	return nil
}

func (editorialSpotlightRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:             "editorial_spotlight",
		Category:         CategoryEditorial,
		SupportsRotation: true,
		AvoidDuplicates:  false,
		Presets: []GalleryPreset{
			{
				Key:              "es_director_auto",
				DisplayName:      "Director Spotlight",
				Icon:             "🎬",
				DescriptionShort: "Weekly spotlight on a director in your library.",
				DefaultParams:    json.RawMessage(`{"subject_type":"director","auto_rotate":true,"rotation_cadence":"weekly"}`),
			},
			{
				Key:              "es_actor",
				DisplayName:      "Actor Spotlight",
				Icon:             "🌟",
				DescriptionShort: "Weekly spotlight on an actor in your library.",
				DefaultParams:    json.RawMessage(`{"subject_type":"actor","auto_rotate":true,"rotation_cadence":"weekly"}`),
			},
			{
				Key:              "es_studio",
				DisplayName:      "Studio Spotlight",
				Icon:             "🏛️",
				DescriptionShort: "Weekly spotlight on a studio in your library.",
				DefaultParams:    json.RawMessage(`{"subject_type":"studio","auto_rotate":true,"rotation_cadence":"weekly"}`),
			},
			{Key: "es_era_80s", DisplayName: "The 80s", Icon: "📼", DescriptionShort: "Films from the 1980s.", DefaultParams: json.RawMessage(`{"subject_type":"era","subject":"1980s"}`)},
		},
	}
}

// GenreRouletteParams configures genre_roulette: a deterministic rotating
// spotlight on one of the library's top genres, using the same bucket hashing
// as editorial_spotlight so every client sees the same genre for the whole
// rotation window.
type GenreRouletteParams struct {
	RotationCadence RotationCadence `json:"rotation_cadence,omitempty"` // daily | weekly (default) | monthly
	MinRating       float64         `json:"min_rating,omitempty"`       // default 6.0
}

type genreRouletteRecipe struct{}

func (genreRouletteRecipe) Type() string                   { return "genre_roulette" }
func (genreRouletteRecipe) NewParams() any                 { return &GenreRouletteParams{} }
func (genreRouletteRecipe) DefaultCacheTTL() time.Duration { return 6 * time.Hour }
func (genreRouletteRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("genre_roulette", rc)
}
func (genreRouletteRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p GenreRouletteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	return oneOf("genre_roulette: rotation_cadence", string(p.RotationCadence),
		"", string(CadenceDaily), string(CadenceWeekly), string(CadenceMonthly))
}
func (genreRouletteRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:             "genre_roulette",
		Category:         CategoryEditorial,
		SupportsRotation: true,
		Presets: []GalleryPreset{
			{
				Key:              "genre_roulette_weekly",
				DisplayName:      "Genre Roulette",
				Icon:             "🎰",
				DescriptionShort: "A different genre from your library every week.",
				DefaultParams:    json.RawMessage(`{"rotation_cadence":"weekly"}`),
			},
		},
	}
}

// AnniversariesParams configures anniversaries: titles celebrating a round
// release anniversary this month (e.g. released exactly 10/20/25 years ago).
type AnniversariesParams struct {
	// MilestoneYears keeps only anniversaries that are a multiple of this many
	// years (default 5, so 5th/10th/15th/... anniversaries qualify). Set to 1
	// to include every anniversary.
	MilestoneYears int `json:"milestone_years,omitempty"`
}

type anniversariesRecipe struct{}

func (anniversariesRecipe) Type() string                   { return "anniversaries" }
func (anniversariesRecipe) NewParams() any                 { return &AnniversariesParams{} }
func (anniversariesRecipe) DefaultCacheTTL() time.Duration { return 24 * time.Hour }
func (anniversariesRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("anniversaries", rc)
}
func (anniversariesRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p AnniversariesParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.MilestoneYears < 0 {
		return errors.New("anniversaries: milestone_years must be >= 0")
	}
	return nil
}
func (anniversariesRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:     "anniversaries",
		Category: CategoryEditorial,
		Presets: []GalleryPreset{
			{
				Key:              "anniversaries_default",
				DisplayName:      "Anniversaries",
				Icon:             "🎂",
				DescriptionShort: "Titles celebrating a milestone release anniversary this month.",
				DefaultParams:    json.RawMessage(`{"milestone_years":5}`),
			},
		},
	}
}

func init() {
	Register(editorialSpotlightRecipe{})
	Register(genreRouletteRecipe{})
	Register(anniversariesRecipe{})
}
