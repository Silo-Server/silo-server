package recommendations

import (
	"context"
	"time"
)

// ScoredItem represents a recommended item with a relevance score and explanation.
type ScoredItem struct {
	MediaItemID  string  `json:"media_item_id"`
	Score        float64 `json:"score"`
	Reason       string  `json:"reason"`
	ReasonDetail string  `json:"reason_detail,omitempty"`
}

// ForYouRow represents a single row in the ForYou response.
type ForYouRow struct {
	Type         string       `json:"type"`
	Label        string       `json:"label"`
	ClusterIndex int          `json:"cluster_index,omitempty"`
	Items        []ScoredItem `json:"items"`
}

// ForYouResponse is the grouped response for the ForYou endpoint.
type ForYouResponse struct {
	Rows []ForYouRow `json:"rows"`
}

// TasteCluster represents a sub-profile cluster from k-means.
type TasteCluster struct {
	UserID         int       `json:"-"`
	ProfileID      string    `json:"-"`
	ClusterIdx     int       `json:"cluster_idx"`
	Embedding      []float32 `json:"-"`
	DominantGenres []string  `json:"dominant_genres"`
	Label          string    `json:"label"`
	MemberCount    int       `json:"member_count"`
	TotalWeight    float64   `json:"total_weight"`
	UpdatedAt      time.Time `json:"-"`
}

// CowatchPair represents a co-watch similarity between two items.
type CowatchPair struct {
	ItemID        string  `json:"item_id"`
	SimilarItemID string  `json:"similar_item_id"`
	JaccardScore  float64 `json:"jaccard_score"`
	CowatchCount  int     `json:"cowatch_count"`
}

// WatchSignal holds computed watch progress signal data for a single item.
type WatchSignal struct {
	MediaItemID   string
	ProgressPct   float64
	Completed     bool
	RewatchCount  int
	LastWatchedAt time.Time
}

// Recommender provides recommendation operations.
type Recommender interface {
	SimilarItems(ctx context.Context, itemID string, limit int, userID int, profileID string) ([]ScoredItem, error)
	ForYou(ctx context.Context, userID int, profileID string, limit int) (*ForYouResponse, error)
	BecauseYouWatched(ctx context.Context, userID int, profileID string, sourceItemID string, limit int) ([]ScoredItem, error)
	SimilarUsersLiked(ctx context.Context, userID int, profileID string, limit int) ([]ScoredItem, error)
	RefreshTasteProfile(ctx context.Context, userID int, profileID string) error
	GetTasteProfileSummary(ctx context.Context, userID int, profileID string) (*TasteProfileSummary, error)
	EmbedItem(ctx context.Context, itemID string) error
	EmbedAll(ctx context.Context) (embedded int, err error)
}

// TasteProfileSummary is the user-facing taste profile response.
type TasteProfileSummary struct {
	TopGenres         []string       `json:"top_genres"`
	FavoriteDirectors []string       `json:"favorite_directors"`
	SignalCounts      map[string]int `json:"signal_counts"`
	UpdatedAt         string         `json:"updated_at"`
}

// Signal weights for taste profile computation.
const (
	WeightRated5    = 1.0
	WeightRewatch   = 0.9 // Completed 2+ times
	WeightRated4    = 0.7
	WeightFavorited = 0.8 // Strong deliberate action
	WeightWatchHigh = 0.8 // Watch progress >= 90%
	WeightWatchMed  = 0.3 // Watch progress 50-89%
	WeightRated3    = 0.2
	WeightWatchlist = 0.15 // Intent signal
	WeightWatchLow  = -0.2 // Abandoned (< 15%)
	WeightRatedLow  = -0.5 // 1-2 star ratings
)

// RecType constants for recommendation cache.
const (
	RecTypeForYouMain          = "for_you_main"
	RecTypeForYouClusterPrefix = "for_you_cluster_"
	RecTypePopular             = "popular"
	RecTypeRecentlyAdded       = "recently_added"
	RecTypeTopRated            = "top_rated"
	RecTypeGenreSamplerPrefix  = "genre_sampler_"
	RecTypeSimilarUsersLiked   = "similar_users_liked"
	RecTypeBecauseWatched      = "because_you_watched"
)

// GlobalCacheUserID is the sentinel user_id for global (non-personalized) cache entries.
const GlobalCacheUserID = 0

// GlobalCacheProfileID is the sentinel profile_id for global cache entries.
const GlobalCacheProfileID = "__global__"

// Cold-start thresholds for graduated warm-up.
const (
	ColdStartFullPersonalized = 15
	ColdStartMixed            = 5
	ColdStartMinimal          = 1
)

// MMR lambda values by recommendation type.
const (
	LambdaForYou         = 0.7
	LambdaGenreRow       = 0.8
	LambdaBecauseWatched = 0.7
	LambdaSimilarUsers   = 0.6
	LambdaSimilarItems   = 0.8
)

// GenreCapPercent is the maximum fraction of a recommendation row any single genre can occupy.
const GenreCapPercent = 0.4

// RecencyBoostDays is the number of days a new item gets a relevance boost.
const RecencyBoostDays = 7

// RecencyBoostMultiplier is the max multiplier for newly added items.
const RecencyBoostMultiplier = 1.2

// CacheCandidateLimit is the default number of candidates cached per row so
// read paths have headroom for watched, low-rated, access, and dedup filters.
const CacheCandidateLimit = 60
