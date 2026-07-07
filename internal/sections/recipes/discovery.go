package recipes

import (
	"encoding/json"
	"time"
)

// HiddenGemsParams configures the hidden_gems resolver.
type HiddenGemsParams struct {
	MinRating    float64 `json:"min_rating,omitempty"`
	MaxPlayCount int     `json:"max_play_count,omitempty"`
}

type hiddenGemsRecipe struct{}

func (hiddenGemsRecipe) Type() string                   { return "hidden_gems" }
func (hiddenGemsRecipe) NewParams() any                 { return &HiddenGemsParams{} }
func (hiddenGemsRecipe) DefaultCacheTTL() time.Duration { return 6 * time.Hour }
func (hiddenGemsRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("hidden_gems", rc)
}
func (hiddenGemsRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p HiddenGemsParams
	return json.Unmarshal(raw, &p)
}
func (hiddenGemsRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:            "hidden_gems",
		Category:        CategoryDiscovery,
		AvoidDuplicates: true,
		Presets: []GalleryPreset{
			{
				Key:              "hidden_gems_default",
				DisplayName:      "Hidden Gems",
				Icon:             "💎",
				DescriptionShort: "Highly rated titles in your library that no one's watched.",
				DefaultParams:    json.RawMessage(`{"min_rating":7.5,"max_play_count":2}`),
			},
		},
	}
}

// CriticallyAcclaimedParams configures the critically_acclaimed resolver.
type CriticallyAcclaimedParams struct {
	MinScore float64 `json:"min_score,omitempty"`
	Source   string  `json:"source,omitempty"`
}

type criticallyAcclaimedRecipe struct{}

func (criticallyAcclaimedRecipe) Type() string                   { return "critically_acclaimed" }
func (criticallyAcclaimedRecipe) NewParams() any                 { return &CriticallyAcclaimedParams{} }
func (criticallyAcclaimedRecipe) DefaultCacheTTL() time.Duration { return 6 * time.Hour }
func (criticallyAcclaimedRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("critically_acclaimed", rc)
}
func (criticallyAcclaimedRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p CriticallyAcclaimedParams
	return json.Unmarshal(raw, &p)
}
func (criticallyAcclaimedRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:            "critically_acclaimed",
		Category:        CategoryDiscovery,
		AvoidDuplicates: true,
		Presets: []GalleryPreset{
			{
				Key:              "ca_imdb",
				DisplayName:      "Critically Acclaimed",
				Icon:             "🏆",
				DescriptionShort: "8.0+ rated by IMDb.",
				DefaultParams:    json.RawMessage(`{"min_score":8.0,"source":"imdb"}`),
			},
		},
	}
}

// AwardWinnersParams configures the award_winners resolver.
type AwardWinnersParams struct {
	AwardType string `json:"award_type,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type awardWinnersRecipe struct{}

func (awardWinnersRecipe) Type() string                   { return "award_winners" }
func (awardWinnersRecipe) NewParams() any                 { return &AwardWinnersParams{} }
func (awardWinnersRecipe) DefaultCacheTTL() time.Duration { return 6 * time.Hour }
func (awardWinnersRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("award_winners", rc)
}
func (awardWinnersRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p AwardWinnersParams
	return json.Unmarshal(raw, &p)
}
func (awardWinnersRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:            "award_winners",
		Category:        CategoryDiscovery,
		AvoidDuplicates: true,
		// The resolver is a stub until award metadata exists (see
		// fetchAwardWinners). Hidden keeps existing saved sections resolvable
		// (they render empty) without advertising presets that can't work.
		Hidden: true,
		Presets: []GalleryPreset{
			{
				Key:              "aw_oscar",
				DisplayName:      "Oscar Winners",
				Icon:             "🏅",
				DescriptionShort: "Academy Award winners in your library.",
				DefaultParams:    json.RawMessage(`{"award_type":"oscar"}`),
			},
			{
				Key:              "aw_emmy",
				DisplayName:      "Emmy Winners",
				Icon:             "📺",
				DescriptionShort: "Emmy-winning shows.",
				DefaultParams:    json.RawMessage(`{"award_type":"emmy"}`),
			},
			{
				Key:              "aw_cannes",
				DisplayName:      "Cannes Selections",
				Icon:             "🎞️",
				DescriptionShort: "Cannes Festival selections.",
				DefaultParams:    json.RawMessage(`{"award_type":"cannes"}`),
			},
		},
	}
}

// ForgottenFavoritesParams configures the forgotten_favorites resolver.
type ForgottenFavoritesParams struct {
	LookbackDays int `json:"lookback_days,omitempty"`
}

type forgottenFavoritesRecipe struct{}

func (forgottenFavoritesRecipe) Type() string                   { return "forgotten_favorites" }
func (forgottenFavoritesRecipe) NewParams() any                 { return &ForgottenFavoritesParams{} }
func (forgottenFavoritesRecipe) DefaultCacheTTL() time.Duration { return 6 * time.Hour }
func (forgottenFavoritesRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("forgotten_favorites", rc)
}
func (forgottenFavoritesRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p ForgottenFavoritesParams
	return json.Unmarshal(raw, &p)
}
func (forgottenFavoritesRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:            "forgotten_favorites",
		Category:        CategoryDiscovery,
		AvoidDuplicates: true,
		Presets: []GalleryPreset{
			{
				Key:              "ff_default",
				DisplayName:      "Forgotten Favorites",
				Icon:             "🕰️",
				DescriptionShort: "In your library, haven't been watched in a year.",
				DefaultParams:    json.RawMessage(`{"lookback_days":365}`),
			},
		},
	}
}

// FormatShowcaseParams configures the format_showcase resolver.
type FormatShowcaseParams struct {
	Format string `json:"format"`         // 4k | dolby_vision | hdr
	Sort   string `json:"sort,omitempty"` // rating (default) | recent
}

type formatShowcaseRecipe struct{}

func (formatShowcaseRecipe) Type() string                   { return "format_showcase" }
func (formatShowcaseRecipe) NewParams() any                 { return &FormatShowcaseParams{} }
func (formatShowcaseRecipe) DefaultCacheTTL() time.Duration { return 6 * time.Hour }
func (formatShowcaseRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("format_showcase", rc)
}
func (formatShowcaseRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p FormatShowcaseParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if err := oneOf("format_showcase: format", p.Format, "", "4k", "dolby_vision", "hdr"); err != nil {
		return err
	}
	return oneOf("format_showcase: sort", p.Sort, "", "rating", "recent")
}
func (formatShowcaseRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:            "format_showcase",
		Category:        CategoryDiscovery,
		AvoidDuplicates: false,
		Presets: []GalleryPreset{
			{Key: "fs_4k", DisplayName: "4K Showcase", Icon: "🎥", DescriptionShort: "Titles available in 4K UHD.", DefaultParams: json.RawMessage(`{"format":"4k"}`)},
			{Key: "fs_4k_recent", DisplayName: "New in 4K", Icon: "🎥", DescriptionShort: "Recently added 4K UHD titles.", DefaultParams: json.RawMessage(`{"format":"4k","sort":"recent"}`)},
			{Key: "fs_dv", DisplayName: "Dolby Vision Picks", Icon: "🌈", DescriptionShort: "Dolby Vision titles.", DefaultParams: json.RawMessage(`{"format":"dolby_vision"}`)},
			{Key: "fs_hdr", DisplayName: "HDR Highlights", Icon: "✨", DescriptionShort: "HDR-mastered titles.", DefaultParams: json.RawMessage(`{"format":"hdr"}`)},
		},
	}
}

func init() {
	Register(hiddenGemsRecipe{})
	Register(criticallyAcclaimedRecipe{})
	Register(awardWinnersRecipe{})
	Register(forgottenFavoritesRecipe{})
	Register(formatShowcaseRecipe{})
}
