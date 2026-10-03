package recipes

import (
	"encoding/json"
	"errors"
	"time"
)

// TrendingParams configures trending_on_server.
type TrendingParams struct {
	Window string `json:"window"` // 24h | 7d | 30d
}

type trendingRecipe struct{}

func (trendingRecipe) Type() string                   { return "trending_on_server" }
func (trendingRecipe) NewParams() any                 { return &TrendingParams{} }
func (trendingRecipe) DefaultCacheTTL() time.Duration { return time.Hour }
func (trendingRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("trending_on_server", rc)
}
func (trendingRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p TrendingParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	switch p.Window {
	case "", "24h", "7d", "30d":
		return nil
	default:
		return errors.New("trending_on_server: window must be 24h, 7d, or 30d")
	}
}
func (trendingRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:     "trending_on_server",
		Category: CategorySocial,
		Presets: []GalleryPreset{
			{Key: "tr_24h", DisplayName: "Trending Now (24h)", Icon: "📈", DescriptionShort: "Most-played in the last day.", DefaultParams: json.RawMessage(`{"window":"24h"}`)},
			{Key: "tr_7d", DisplayName: "Trending This Week", Icon: "📈", DescriptionShort: "Most-played in the last week.", DefaultParams: json.RawMessage(`{"window":"7d"}`)},
			{Key: "tr_30d", DisplayName: "Trending This Month", Icon: "📈", DescriptionShort: "Most-played in the last month.", DefaultParams: json.RawMessage(`{"window":"30d"}`)},
		},
	}
}

// ProfileActivityFeedParams configures profile_activity_feed.
//
// RecentCount is reserved for future use — it will eventually let admins cap
// the number of activity entries per profile (e.g., "show at most N from each
// other profile"). Currently the fetcher honors only the section ItemLimit;
// persisting RecentCount in the section config is forward-compatible.
type ProfileActivityFeedParams struct {
	ProfileID   string `json:"profile_id"` // empty = the caller's other profiles
	RecentCount int    `json:"recent_count,omitempty"`
}

type profileActivityRecipe struct{}

func (profileActivityRecipe) Type() string                   { return "profile_activity_feed" }
func (profileActivityRecipe) NewParams() any                 { return &ProfileActivityFeedParams{} }
func (profileActivityRecipe) DefaultCacheTTL() time.Duration { return 5 * time.Minute }
func (profileActivityRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("profile_activity_feed", rc)
}
func (profileActivityRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p ProfileActivityFeedParams
	return json.Unmarshal(raw, &p)
}
func (profileActivityRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:     "profile_activity_feed",
		Category: CategorySocial,
		Presets: []GalleryPreset{
			{Key: "pa_household", DisplayName: "What Others Just Watched", Icon: "👪", DescriptionShort: "Recent watches by the other profiles on your account.", DefaultParams: json.RawMessage(`{"profile_id":""}`)},
		},
	}
}

// NewToLibraryParams configures new_to_library.
type NewToLibraryParams struct {
	LookbackDays int `json:"lookback_days,omitempty"` // default 30
}

type newToLibraryRecipe struct{}

func (newToLibraryRecipe) Type() string                   { return "new_to_library" }
func (newToLibraryRecipe) NewParams() any                 { return &NewToLibraryParams{} }
func (newToLibraryRecipe) DefaultCacheTTL() time.Duration { return time.Hour }
func (newToLibraryRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("new_to_library", rc)
}
func (newToLibraryRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p NewToLibraryParams
	return json.Unmarshal(raw, &p)
}
func (newToLibraryRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:     "new_to_library",
		Category: CategorySocial,
		Presets: []GalleryPreset{
			{Key: "ntl_30", DisplayName: "New to Library This Month", Icon: "🆕", DescriptionShort: "Added in the last 30 days.", DefaultParams: json.RawMessage(`{"lookback_days":30}`)},
		},
	}
}

// MostWatchedParams configures most_watched.
type MostWatchedParams struct {
	Window string `json:"window"` // week | month
}

type mostWatchedRecipe struct{}

func (mostWatchedRecipe) Type() string                   { return "most_watched" }
func (mostWatchedRecipe) NewParams() any                 { return &MostWatchedParams{} }
func (mostWatchedRecipe) DefaultCacheTTL() time.Duration { return time.Hour }
func (mostWatchedRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("most_watched", rc)
}
func (mostWatchedRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p MostWatchedParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	switch p.Window {
	case "", "week", "month":
		return nil
	default:
		return errors.New("most_watched: window must be week or month")
	}
}
func (mostWatchedRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:     "most_watched",
		Category: CategorySocial,
		Presets: []GalleryPreset{
			{Key: "mw_week", DisplayName: "Most Watched This Week", Icon: "🏆", DescriptionShort: "Top plays in the last 7 days.", DefaultParams: json.RawMessage(`{"window":"week"}`)},
			{Key: "mw_month", DisplayName: "Most Watched This Month", Icon: "🏆", DescriptionShort: "Top plays in the last 30 days.", DefaultParams: json.RawMessage(`{"window":"month"}`)},
		},
	}
}

func init() {
	Register(trendingRecipe{})
	Register(profileActivityRecipe{})
	Register(newToLibraryRecipe{})
	Register(mostWatchedRecipe{})
}
