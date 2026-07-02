package sections

import (
	"encoding/json"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// SectionType enumerates the supported section types.
type SectionType string

const (
	SectionContinueWatching SectionType = "continue_watching"
	SectionRecentlyAdded    SectionType = "recently_added"
	SectionRecentlyReleased SectionType = "recently_released"
	SectionWatchlist        SectionType = "watchlist"
	SectionFavorites        SectionType = "favorites"
	SectionGenre            SectionType = "genre"
	SectionCustomFilter     SectionType = "custom_filter"
	SectionRandom           SectionType = "random"
	SectionCollection       SectionType = "collection"

	SectionRecommendedForYou   SectionType = "recommended_for_you"
	SectionBecauseYouWatched   SectionType = "because_you_watched"
	SectionSimilarUsersLiked   SectionType = "similar_users_liked"
	SectionTasteMatch          SectionType = "taste_match"
	SectionNextUp              SectionType = "next_up"
	SectionNextInSeries        SectionType = "next_in_series"
	SectionHiddenGems          SectionType = "hidden_gems"
	SectionCriticallyAcclaimed SectionType = "critically_acclaimed"
	SectionAwardWinners        SectionType = "award_winners"
	SectionForgottenFavorites  SectionType = "forgotten_favorites"
	SectionFormatShowcase      SectionType = "format_showcase"
	SectionEditorialSpotlight  SectionType = "editorial_spotlight"
	SectionSeasonalThemed      SectionType = "seasonal_themed"
	SectionMoodCollection      SectionType = "mood_collection"

	SectionTrendingOnServer    SectionType = "trending_on_server"
	SectionProfileActivityFeed SectionType = "profile_activity_feed"
	SectionNewToLibrary        SectionType = "new_to_library"
	SectionMostWatched         SectionType = "most_watched"

	SectionTrendingDiscover SectionType = "trending_discover"

	SectionAdminCuratedList SectionType = "admin_curated_list"
)

// ValidSectionTypes is the set of all valid section type values.
var ValidSectionTypes = map[SectionType]bool{
	SectionContinueWatching:    true,
	SectionRecentlyAdded:       true,
	SectionRecentlyReleased:    true,
	SectionWatchlist:           true,
	SectionFavorites:           true,
	SectionGenre:               true,
	SectionCustomFilter:        true,
	SectionRandom:              true,
	SectionCollection:          true,
	SectionRecommendedForYou:   true,
	SectionBecauseYouWatched:   true,
	SectionSimilarUsersLiked:   true,
	SectionTasteMatch:          true,
	SectionNextUp:              true,
	SectionNextInSeries:        true,
	SectionHiddenGems:          true,
	SectionCriticallyAcclaimed: true,
	SectionAwardWinners:        true,
	SectionForgottenFavorites:  true,
	SectionFormatShowcase:      true,
	SectionEditorialSpotlight:  true,
	SectionSeasonalThemed:      true,
	SectionMoodCollection:      true,
	SectionTrendingOnServer:    true,
	SectionProfileActivityFeed: true,
	SectionNewToLibrary:        true,
	SectionMostWatched:         true,
	SectionTrendingDiscover:    true,
	SectionAdminCuratedList:    true,
}

// PageSection is an admin-defined section stored in PostgreSQL.
type PageSection struct {
	ID          string          `json:"id"`
	Scope       string          `json:"scope"`
	LibraryID   *int            `json:"library_id"`
	Position    int             `json:"position"`
	SectionType SectionType     `json:"section_type"`
	Title       string          `json:"title"`
	Featured    bool            `json:"featured"`
	ItemLimit   int             `json:"item_limit"`
	Config      json.RawMessage `json:"config"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// ProfileSectionOverride is a per-profile customization stored in the user store.
type ProfileSectionOverride struct {
	ID          string          `json:"id"`
	ProfileID   string          `json:"profile_id"`
	Scope       string          `json:"scope"`
	LibraryID   string          `json:"library_id,omitempty"`
	SectionID   string          `json:"section_id,omitempty"`
	Position    *int            `json:"position,omitempty"`
	Hidden      bool            `json:"hidden"`
	Removed     bool            `json:"removed"`
	SectionType SectionType     `json:"section_type,omitempty"`
	Title       string          `json:"title,omitempty"`
	Featured    *bool           `json:"featured,omitempty"`
	ItemLimit   *int            `json:"item_limit,omitempty"`
	Config      json.RawMessage `json:"config,omitempty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`

	// IsUserAdded marks this override as a profile-built recipe instance
	// rather than a customization of an admin section. When true, SectionID
	// is empty and UserSectionType / UserConfig / UserTitle take precedence
	// over the legacy SectionType / Config / Title fields.
	IsUserAdded     bool            `json:"is_user_added,omitempty"`
	UserSectionType SectionType     `json:"user_section_type,omitempty"`
	UserConfig      json.RawMessage `json:"user_config,omitempty"`
	UserTitle       string          `json:"user_title,omitempty"`
}

// ResolvedSection is the merged result of admin section + profile override.
type ResolvedSection struct {
	ID          string          `json:"id"`
	SectionType SectionType     `json:"section_type"`
	Title       string          `json:"title"`
	Featured    bool            `json:"featured"`
	ItemLimit   int             `json:"item_limit"`
	Config      json.RawMessage `json:"config"`
	Position    int             `json:"position"`
	IsCustom    bool            `json:"is_custom"`
	Customized  bool            `json:"customized"`
	Hidden      bool            `json:"hidden,omitempty"`

	// SuppressNextUp, when true on a continue-watching section, skips the
	// next-up injection (and the combined series collapse/sort that pairs with
	// it) so the section returns in-progress resume points only. Callers that
	// need a strict "resume" view — e.g. the Jellyfin /UserItems/Resume
	// compatibility endpoint, which must never surface not-yet-started items —
	// set this; admin/home rendering leaves it false to keep next-up cards.
	SuppressNextUp bool `json:"-"`
}

// FilterConfig represents the rule-group filter structure.
type FilterConfig struct {
	Match  string        `json:"match"`
	Groups []FilterGroup `json:"groups"`
	Sort   string        `json:"sort,omitempty"`
	Order  string        `json:"order,omitempty"`
}

// FilterGroup is a group of filter rules joined by AND or OR.
type FilterGroup struct {
	Match string       `json:"match"`
	Rules []FilterRule `json:"rules"`
}

// FilterRule is a single filter condition.
type FilterRule struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// SectionConfigFilters holds the optional type and library filters from section config.
type SectionConfigFilters struct {
	FilterType       string `json:"filter_type"`
	FilterLibraryID  *int   `json:"filter_library_id"`
	FilterLibraryIDs []int  `json:"filter_library_ids"`
}

// SectionCollectionConfig holds the selected collection reference.
// A section may reference either a library collection (admin-managed) or a
// user collection (personal, profile-scoped). Only one field should be set.
type SectionCollectionConfig struct {
	LibraryCollectionID string `json:"library_collection_id,omitempty"`
	UserCollectionID    string `json:"user_collection_id,omitempty"`
}

// ParseConfigFilters extracts filter_type and filter_library_id from config JSON.
func ParseConfigFilters(config json.RawMessage) SectionConfigFilters {
	var f SectionConfigFilters
	if len(config) > 0 {
		_ = json.Unmarshal(config, &f)
	}
	if len(f.FilterLibraryIDs) == 0 && f.FilterLibraryID == nil {
		def, err := ParseQueryDefinition(config)
		if err == nil {
			switch def.MediaScope {
			case "movie", "series", "audiobook":
				f.FilterType = def.MediaScope
			}
			if len(def.LibraryIDs) > 0 {
				f.FilterLibraryIDs = append([]int(nil), def.LibraryIDs...)
			}
		}
	}
	return f
}

// LibraryIDs returns the effective library filter IDs, supporting both the
// legacy single-library field and the newer multi-library field.
func (f SectionConfigFilters) LibraryIDs() []int {
	ids := make([]int, 0, len(f.FilterLibraryIDs)+1)
	seen := make(map[int]struct{}, len(f.FilterLibraryIDs)+1)

	for _, id := range f.FilterLibraryIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	if f.FilterLibraryID != nil && *f.FilterLibraryID > 0 {
		if _, ok := seen[*f.FilterLibraryID]; !ok {
			ids = append(ids, *f.FilterLibraryID)
		}
	}

	if len(ids) == 0 {
		return nil
	}
	return ids
}

// ParseCollectionConfig extracts library_collection_id from config JSON.
func ParseCollectionConfig(config json.RawMessage) SectionCollectionConfig {
	var c SectionCollectionConfig
	if len(config) > 0 {
		_ = json.Unmarshal(config, &c)
	}
	return c
}

func ParseQueryDefinition(config json.RawMessage) (catalog.QueryDefinition, error) {
	if len(config) == 0 {
		return catalog.QueryDefinition{}.Normalize(), nil
	}

	var legacy struct {
		FilterType       string          `json:"filter_type"`
		FilterLibraryID  *int            `json:"filter_library_id"`
		FilterLibraryIDs []int           `json:"filter_library_ids"`
		Sort             json.RawMessage `json:"sort"`
		Order            string          `json:"order"`
	}
	if err := json.Unmarshal(config, &legacy); err != nil {
		return catalog.QueryDefinition{}, err
	}

	if legacy.FilterType != "" || legacy.FilterLibraryID != nil || len(legacy.FilterLibraryIDs) > 0 || legacy.Order != "" || isLegacySortConfig(legacy.Sort) {
		return catalog.NormalizeLegacySectionFilter(config)
	}

	var def catalog.QueryDefinition
	if err := json.Unmarshal(config, &def); err != nil {
		return catalog.QueryDefinition{}, err
	}
	def = def.Normalize()
	return def, def.Validate()
}

func isLegacySortConfig(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '"'
}
