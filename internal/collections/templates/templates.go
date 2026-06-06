// Package templates exposes a curated catalog of pre-configured Library
// Collections. Each Template encapsulates a source provider (TMDB, Trakt,
// MDBList) plus the parameters needed for the existing import endpoints, so
// the admin UI can offer a one-click "create from template" flow analogous to
// the section recipe gallery.
package templates

// Category groups templates for the admin UI gallery.
type Category string

const (
	CategoryTrending   Category = "trending"
	CategoryPopular    Category = "popular"
	CategoryStreaming  Category = "streaming"
	CategoryTopRated   Category = "top_rated"
	CategoryInTheaters Category = "in_theaters"
	CategoryUpcoming   Category = "upcoming"
	CategoryAiring     Category = "airing"
	CategoryEditorial  Category = "editorial"
	CategoryCustom     Category = "custom"
)

// Source identifies which import endpoint a template targets.
type Source string

const (
	SourceTMDB           Source = "tmdb"
	SourceTrakt          Source = "trakt"
	SourceMDBList        Source = "mdblist"
	SourceTMDBDiscover   Source = "tmdb_discover"
	SourceTMDBCollection Source = "tmdb_collection"
)

// MediaKind labels the dominant media type the template returns; the UI uses
// it for filtering and badges.
type MediaKind string

const (
	MediaMovie     MediaKind = "movie"
	MediaTV        MediaKind = "tv"
	MediaMixed     MediaKind = "mixed"
	MediaAudiobook MediaKind = "audiobook"
)

// TMDBSpec is the TMDB-specific portion of a template.
type TMDBSpec struct {
	Preset     string `json:"preset"`
	MediaType  string `json:"media_type"`
	TimeWindow string `json:"time_window,omitempty"`
}

// TraktSpec is the Trakt-specific portion of a template.
type TraktSpec struct {
	Preset    string `json:"preset"`
	MediaType string `json:"media_type"`
}

// MDBListSpec is the MDBList-specific portion of a template.
type MDBListSpec struct {
	URL string `json:"url"`
}

// TMDBCollectionSpec is the TMDB `/collection/{id}` portion of a template.
// Used for franchise/saga templates where the entire member list is curated
// by TMDB (e.g. MCU, Star Wars).
//
// CollectionID == 0 is permitted and treated as a placeholder/sentinel: the
// catalog can ship a generic "TMDB Franchise" template that an admin fills
// in at apply-time. Sync will fail loudly until a non-zero CollectionID is
// configured on the resulting collection.
type TMDBCollectionSpec struct {
	CollectionID int `json:"collection_id"`
}

// TMDBDiscoverSpec is the TMDB `/discover/{movie,tv}` portion of a template.
// It mirrors TMDB's documented discover query parameters so the builder can
// hand the spec directly to the TMDB client.
type TMDBDiscoverSpec struct {
	MediaType        string   `json:"media_type"`
	WithGenres       []int    `json:"with_genres,omitempty"`
	WithoutGenres    []int    `json:"without_genres,omitempty"`
	SortBy           string   `json:"sort_by"`
	VoteCountGte     int      `json:"vote_count_gte,omitempty"`
	VoteAverageGte   float64  `json:"vote_average_gte,omitempty"`
	ReleaseDateGte   string   `json:"release_date_gte,omitempty"`
	ReleaseDateLte   string   `json:"release_date_lte,omitempty"`
	Certifications   []string `json:"certifications,omitempty"`
	CertificationLte string   `json:"certification_lte,omitempty"`
	WithRuntimeGte   int      `json:"with_runtime_gte,omitempty"`
	WithRuntimeLte   int      `json:"with_runtime_lte,omitempty"`
	OriginalLanguage string   `json:"original_language,omitempty"`
}

// Template is a single pre-configured collection blueprint.
//
// Exactly one of TMDB, Trakt, or MDBList is populated, matching Source. The
// admin UI hands the template's source-specific fields to the corresponding
// import endpoint, allowing a one-click create flow.
type Template struct {
	ID                  string              `json:"id"`
	Title               string              `json:"title"`
	Description         string              `json:"description"`
	Icon                string              `json:"icon"`
	Category            Category            `json:"category"`
	Source              Source              `json:"source"`
	MediaKind           MediaKind           `json:"media_kind"`
	DefaultLimit        int                 `json:"default_limit,omitempty"`
	DefaultSortOrder    int                 `json:"default_sort_order,omitempty"`
	DefaultSyncSchedule string              `json:"default_sync_schedule,omitempty"`
	PosterPath          string              `json:"poster_path,omitempty"`
	RequiresProfile     bool                `json:"requires_profile,omitempty"`
	Featured            bool                `json:"featured,omitempty"`
	Tags                []string            `json:"tags,omitempty"`
	TMDB                *TMDBSpec           `json:"tmdb,omitempty"`
	Trakt               *TraktSpec          `json:"trakt,omitempty"`
	MDBList             *MDBListSpec        `json:"mdblist,omitempty"`
	TMDBDiscover        *TMDBDiscoverSpec   `json:"tmdb_discover,omitempty"`
	TMDBCollection      *TMDBCollectionSpec `json:"tmdb_collection,omitempty"`
}

// Bundle is an ordered set of built-in templates that can be applied together
// to seed a useful group of synced collections.
type Bundle struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	TemplateIDs []string `json:"template_ids"`
}

// Catalog is the listing returned by the API: templates grouped by category
// in a stable display order for the gallery.
type Catalog struct {
	Categories []CategoryGroup `json:"categories"`
}

// BundleCatalog is the listing returned by the bundle API.
type BundleCatalog struct {
	Bundles []Bundle `json:"bundles"`
}

// CategoryGroup bundles templates that share a category.
type CategoryGroup struct {
	Category  Category   `json:"category"`
	Label     string     `json:"label"`
	Templates []Template `json:"templates"`
}

// CategoryLabel returns a human-friendly title for a category.
func CategoryLabel(c Category) string {
	switch c {
	case CategoryTrending:
		return "Trending"
	case CategoryPopular:
		return "Popular"
	case CategoryStreaming:
		return "Streaming Services"
	case CategoryTopRated:
		return "Top Rated"
	case CategoryInTheaters:
		return "In Theaters"
	case CategoryUpcoming:
		return "Upcoming"
	case CategoryAiring:
		return "On Air"
	case CategoryEditorial:
		return "Editorial"
	case CategoryCustom:
		return "Custom"
	default:
		return string(c)
	}
}
