package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Category groups recipes for the UI gallery and for admin filtering.
type Category string

const (
	CategoryLibraryStaples Category = "library_staples"
	CategoryPersonalized   Category = "personalized"
	CategoryDiscovery      Category = "discovery"
	CategoryEditorial      Category = "editorial"
	CategorySeasonal       Category = "seasonal"
	CategoryMood           Category = "mood"
	CategoryHandPicked     Category = "hand_picked"
	CategorySocial         Category = "social"
	CategoryCustom         Category = "custom"
)

// GalleryPreset is a UI-facing preset that maps to a Recipe + DefaultParams.
// Many presets can share one resolver via parameterization (editorial, seasonal, mood).
type GalleryPreset struct {
	Key              string          `json:"key"`
	DisplayName      string          `json:"display_name"`
	Icon             string          `json:"icon"`
	DescriptionShort string          `json:"description_short"`
	DescriptionLong  string          `json:"description_long,omitempty"`
	DefaultParams    json.RawMessage `json:"default_params"`
}

// RecipeDefinition is the metadata side of a Recipe — what shows up in the gallery.
type RecipeDefinition struct {
	Type             string          `json:"type"`
	Category         Category        `json:"category"`
	Presets          []GalleryPreset `json:"presets"`
	AvoidDuplicates  bool            `json:"avoid_duplicates"`
	SupportsRotation bool            `json:"supports_rotation"`
	AdminOnly        bool            `json:"admin_only"`
	// Hidden recipes are still resolvable (so existing sections with that type
	// keep working) but the API gallery list omits them. Used to phase out
	// duplicate type aliases like `genre` (which is just a custom_filter).
	Hidden bool `json:"hidden,omitempty"`
}

// LibraryScope constrains a section to one or more libraries (or all libraries when both are nil/empty).
type LibraryScope struct {
	LibraryID  *int  // single-library scope, or nil for "home"
	LibraryIDs []int // multi-library scope (when admin picked several); empty for default
}

// DBPool is the minimal pool surface a resolver needs.
// The concrete type used at wire-up is *pgxpool.Pool.
type DBPool interface{}

// RecommendationReader matches the recommendation reader interface in sections/fetcher.go.
type RecommendationReader interface {
	GetForYouMain(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (*recommendations.ForYouRow, error)
	GetBecauseYouWatched(ctx context.Context, userID int, profileID, sourceItemID string, limit int, filter catalog.AccessFilter) ([]recommendations.ScoredItem, error)
	GetSimilarUsersLiked(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]recommendations.ScoredItem, error)
	GetTasteMatchRow(ctx context.Context, userID int, profileID, genre string, limit int, filter catalog.AccessFilter) (*recommendations.ForYouRow, error)
}

// ResolverContext is everything a resolver needs to resolve.
type ResolverContext struct {
	Ctx       context.Context
	Now       time.Time
	UserID    int
	ProfileID string
	Library   LibraryScope
	Filter    catalog.AccessFilter
	Params    json.RawMessage
	ItemLimit int
	Title     string
	SeenItems map[string]struct{} // populated by dispatcher; resolvers honor only when AvoidDuplicates is true on the def

	Pool           DBPool
	StoreProvider  userstore.UserStoreProvider
	Recs           RecommendationReader
	CollectionRepo *catalog.LibraryCollectionRepository
	NextUpRepo     *catalog.NextUpRepository
}

// SectionItemMeta mirrors sections.SectionItemMeta but lives here to avoid import cycles.
type SectionItemMeta struct {
	SeriesID          *string
	SeriesTitle       string
	SeasonNumber      *int
	EpisodeNumber     *int
	Badges            []string
	PositionSeconds   *float64
	DurationSeconds   *float64
	ProgressUpdatedAt *string
	ItemSource        string    // "in_progress" or "next_up"
	SortTimestamp     time.Time // when the preceding episode was completed (for ordering)
}

// ResolvedItems is the resolver's output.
type ResolvedItems struct {
	Items         []*models.MediaItem
	TotalCount    int
	ItemMeta      map[string]SectionItemMeta
	TitleOverride string        // e.g. "Director Spotlight: Greta Gerwig"
	Suppressed    bool          // when true, dispatcher omits the section
	LayoutHint    string        // poster | landscape | hero | square
	CacheTTL      time.Duration // 0 = use DefaultCacheTTL
}

// oneOf validates an enum-style string param against its allowed values.
// Shared by recipe validators so allowed-value checks read identically.
func oneOf(field, value string, allowed ...string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("%s: invalid value %q (want one of %s)", field, value, strings.Join(allowed, "|"))
}

// Recipe is the contract every section type implements.
type Recipe interface {
	Type() string
	Definition() RecipeDefinition
	NewParams() any
	Validate(params json.RawMessage) error
	Resolve(rc ResolverContext) (ResolvedItems, error)
	DefaultCacheTTL() time.Duration
}
