package recipes

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// libStapleParams is the (empty) param shape for parameter-free library staples.
type libStapleParams struct{}

// libStaple wraps a delegated resolver func with Recipe metadata.
type libStaple struct {
	typ         string
	displayName string
	icon        string
	descShort   string
	cacheTTL    time.Duration
	presets     []GalleryPreset
}

func (l *libStaple) Type() string   { return l.typ }
func (l *libStaple) NewParams() any { return &libStapleParams{} }
func (l *libStaple) Validate(raw json.RawMessage) error {
	if l.typ != "continue_watching" {
		return nil // any JSON is acceptable; missing fields ignored
	}
	var params struct {
		ContinueType string `json:"continue_type"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return err
		}
	}
	switch strings.ToLower(strings.TrimSpace(params.ContinueType)) {
	case "", "watching", "listening", "reading":
		return nil
	default:
		return fmt.Errorf("continue_type must be 'watching', 'listening', or 'reading'")
	}
}
func (l *libStaple) DefaultCacheTTL() time.Duration { return l.cacheTTL }

func (l *libStaple) Definition() RecipeDefinition {
	presets := l.presets
	if presets == nil {
		presets = []GalleryPreset{
			{
				Key:              l.typ + "_default",
				DisplayName:      l.displayName,
				Icon:             l.icon,
				DescriptionShort: l.descShort,
				DefaultParams:    json.RawMessage(`{}`),
			},
		}
	}
	return RecipeDefinition{
		Type:     l.typ,
		Category: CategoryLibraryStaples,
		Presets:  presets,
	}
}

// Resolve delegates to the bridge installed by package sections (see Task 1.8).
func (l *libStaple) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve(l.typ, rc)
}

func init() {
	Register(&libStaple{typ: "recently_added", displayName: "Recently Added", icon: "🆕", descShort: "Latest additions to your library.", cacheTTL: 5 * time.Minute})
	Register(&libStaple{typ: "recently_released", displayName: "New Releases", icon: "🎬", descShort: "Recently released titles.", cacheTTL: 30 * time.Minute})
	Register(&libStaple{
		typ:         "continue_watching",
		displayName: "Continue Watching",
		icon:        "▶️",
		descShort:   "Pick up where you left off.",
		cacheTTL:    time.Minute,
		presets: []GalleryPreset{
			{
				Key:              "continue_watching_default",
				DisplayName:      "Continue Watching",
				Icon:             "▶️",
				DescriptionShort: "Pick up movies and episodes where you left off.",
				DefaultParams:    json.RawMessage(`{"continue_type":"watching"}`),
			},
			{
				Key:              "continue_listening_default",
				DisplayName:      "Continue Listening",
				Icon:             "🎧",
				DescriptionShort: "Pick up audiobooks where you left off.",
				DefaultParams:    json.RawMessage(`{"continue_type":"listening"}`),
			},
		},
	})
	Register(&libStaple{typ: "next_up", displayName: "On Deck", icon: "📺", descShort: "Next episodes ready to watch.", cacheTTL: time.Minute})
	Register(&libStaple{typ: "watchlist", displayName: "Watchlist", icon: "🔖", descShort: "Items you've saved to watch.", cacheTTL: time.Minute})
	Register(&libStaple{typ: "favorites", displayName: "Favorites", icon: "⭐", descShort: "Your favorites.", cacheTTL: time.Minute})
	Register(&libStaple{typ: "random", displayName: "Surprise Me", icon: "🎲", descShort: "A random selection from your library.", cacheTTL: 5 * time.Minute})
}
