package recipes

import (
	"encoding/json"
	"errors"
	"time"
)

// TrendingDiscoverParams configures the trending_discover section: external
// global trending pulled from a single source (TMDB or Trakt), mixing movies +
// series in one list, matched to titles already in the library.
type TrendingDiscoverParams struct {
	Source string `json:"source"` // "tmdb" | "trakt"
	Window string `json:"window"` // "day" | "week" (TMDB only; ignored by Trakt)
}

type trendingDiscoverRecipe struct{}

func (trendingDiscoverRecipe) Type() string                   { return "trending_discover" }
func (trendingDiscoverRecipe) NewParams() any                 { return &TrendingDiscoverParams{} }
func (trendingDiscoverRecipe) DefaultCacheTTL() time.Duration { return time.Hour }
func (trendingDiscoverRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("trending_discover", rc)
}

func (trendingDiscoverRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var p TrendingDiscoverParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	switch p.Source {
	case "", "tmdb", "trakt":
	default:
		return errors.New(`trending_discover: source must be "tmdb" or "trakt"`)
	}
	switch p.Window {
	case "", "day", "week":
	default:
		return errors.New(`trending_discover: window must be "day" or "week"`)
	}
	return nil
}

func (trendingDiscoverRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:     "trending_discover",
		Category: CategorySocial,
		Presets: []GalleryPreset{
			{Key: "tdisc_tmdb_day", DisplayName: "TMDB Trending Today", Icon: "🔥", DescriptionShort: "Today's trending movies & shows from TMDB, matched to your library.", DefaultParams: json.RawMessage(`{"source":"tmdb","window":"day"}`)},
			{Key: "tdisc_tmdb_week", DisplayName: "TMDB Trending This Week", Icon: "🔥", DescriptionShort: "This week's trending movies & shows from TMDB, matched to your library.", DefaultParams: json.RawMessage(`{"source":"tmdb","window":"week"}`)},
			{Key: "tdisc_trakt", DisplayName: "Trakt Trending", Icon: "📈", DescriptionShort: "Trending movies & shows on Trakt, matched to your library.", DefaultParams: json.RawMessage(`{"source":"trakt","window":"week"}`)},
		},
	}
}

func init() {
	Register(trendingDiscoverRecipe{})
}
