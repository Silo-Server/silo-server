package recipes

import (
	"encoding/json"
	"errors"
	"time"
)

type forYouRecipe struct{}

func (forYouRecipe) Type() string                     { return "recommended_for_you" }
func (forYouRecipe) NewParams() any                   { return &struct{}{} }
func (forYouRecipe) Validate(_ json.RawMessage) error { return nil }
func (forYouRecipe) DefaultCacheTTL() time.Duration   { return time.Hour }
func (forYouRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("recommended_for_you", rc)
}
func (forYouRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:            "recommended_for_you",
		Category:        CategoryPersonalized,
		AvoidDuplicates: true,
		Presets: []GalleryPreset{
			{Key: "for_you", DisplayName: "Recommended For You", Icon: "⭐", DescriptionShort: "Per-profile picks from the recommendation engine.", DefaultParams: json.RawMessage(`{}`)},
		},
	}
}

// BecauseYouWatchedParams is the typed param shape for because_you_watched.
type BecauseYouWatchedParams struct {
	// AnchorItemID is the media item this row is anchored to. Empty = auto-pick the latest watched.
	AnchorItemID string `json:"anchor_item_id"`
}

type becauseRecipe struct{}

func (becauseRecipe) Type() string                   { return "because_you_watched" }
func (becauseRecipe) NewParams() any                 { return &BecauseYouWatchedParams{} }
func (becauseRecipe) DefaultCacheTTL() time.Duration { return time.Hour }
func (becauseRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("because_you_watched", rc)
}
func (becauseRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p BecauseYouWatchedParams
	return json.Unmarshal(raw, &p)
}
func (becauseRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:             "because_you_watched",
		Category:         CategoryPersonalized,
		AvoidDuplicates:  true,
		SupportsRotation: true, // auto-pick anchor rotates as profile completes new items
		Presets: []GalleryPreset{
			{Key: "bcw_auto", DisplayName: "Because You Watched", Icon: "📺", DescriptionShort: "Picks based on your most recent watch.", DefaultParams: json.RawMessage(`{"anchor_item_id":""}`)},
		},
	}
}

type similarUsersRecipe struct{}

func (similarUsersRecipe) Type() string                     { return "similar_users_liked" }
func (similarUsersRecipe) NewParams() any                   { return &struct{}{} }
func (similarUsersRecipe) Validate(_ json.RawMessage) error { return nil }
func (similarUsersRecipe) DefaultCacheTTL() time.Duration   { return time.Hour }
func (similarUsersRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("similar_users_liked", rc)
}
func (similarUsersRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:            "similar_users_liked",
		Category:        CategoryPersonalized,
		AvoidDuplicates: true,
		Presets: []GalleryPreset{
			{Key: "similar", DisplayName: "Profiles Like You Enjoyed", Icon: "👥", DescriptionShort: "What similar profiles are loving.", DefaultParams: json.RawMessage(`{}`)},
		},
	}
}

// TasteMatchParams optionally narrows by genre. When Genre is empty the
// recommendation reader auto-picks the profile's strongest taste cluster
// (falling back to the server-wide top genre).
type TasteMatchParams struct {
	Genre string `json:"genre"`
}

type tasteMatchRecipe struct{}

func (tasteMatchRecipe) Type() string                   { return "taste_match" }
func (tasteMatchRecipe) NewParams() any                 { return &TasteMatchParams{} }
func (tasteMatchRecipe) DefaultCacheTTL() time.Duration { return time.Hour }
func (tasteMatchRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("taste_match", rc)
}
func (tasteMatchRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p TasteMatchParams
	return json.Unmarshal(raw, &p)
}
func (tasteMatchRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:            "taste_match",
		Category:        CategoryPersonalized,
		AvoidDuplicates: true,
		Presets: []GalleryPreset{
			{Key: "taste_top", DisplayName: "Top Picks Today", Icon: "🎯", DescriptionShort: "Best matches for your strongest taste — set a genre to narrow it.", DefaultParams: json.RawMessage(`{}`)},
		},
	}
}

// ReturningShowsParams configures returning_shows: series the profile has
// watched that received a brand-new season within the lookback window.
type ReturningShowsParams struct {
	LookbackDays int `json:"lookback_days,omitempty"` // default 30
}

type returningShowsRecipe struct{}

func (returningShowsRecipe) Type() string                   { return "returning_shows" }
func (returningShowsRecipe) NewParams() any                 { return &ReturningShowsParams{} }
func (returningShowsRecipe) DefaultCacheTTL() time.Duration { return time.Hour }
func (returningShowsRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("returning_shows", rc)
}
func (returningShowsRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p ReturningShowsParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.LookbackDays < 0 {
		return errors.New("returning_shows: lookback_days must be >= 0")
	}
	return nil
}
func (returningShowsRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:            "returning_shows",
		Category:        CategoryPersonalized,
		AvoidDuplicates: true,
		Presets: []GalleryPreset{
			{
				Key:              "returning_shows_default",
				DisplayName:      "Returning Shows",
				Icon:             "🔁",
				DescriptionShort: "Shows you've watched with a brand-new season in your library.",
				DefaultParams:    json.RawMessage(`{"lookback_days":30}`),
			},
		},
	}
}

func init() {
	Register(forYouRecipe{})
	Register(becauseRecipe{})
	Register(similarUsersRecipe{})
	Register(tasteMatchRecipe{})
	Register(returningShowsRecipe{})
}
