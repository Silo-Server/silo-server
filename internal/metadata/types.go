package metadata

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
)

// RefreshMode determines how the pipeline processes an item.
type RefreshMode int

const (
	ModeInitialMatch     RefreshMode = iota // New file discovered by scanner
	ModeScheduledRefresh                    // Background periodic refresh
	ModeManualRefresh                       // User-triggered refresh
	ModeIdentify                            // User manually identified item
)

// MergeMode determines how provider results merge into existing items.
type MergeMode int

const (
	MergeFillEmpty       MergeMode = iota // Only fill zero/empty fields
	MergeReplaceUnlocked                  // Replace all unlocked fields
)

// MetadataField identifies a lockable metadata field.
type MetadataField int

const (
	FieldName MetadataField = iota
	FieldOverview
	FieldGenres
	FieldStudios
	FieldCast
	FieldCrew
	FieldRating
	FieldRuntime
	FieldTags
	FieldContentRating
	FieldImages
	FieldAirSchedule
)

// RefreshPriority controls queue ordering.
type RefreshPriority int

const (
	PriorityHigh   RefreshPriority = 0 // Manual refresh, identify
	PriorityNormal RefreshPriority = 1 // Newly matched items
	PriorityLow    RefreshPriority = 2 // Scheduled background refresh
)

// ProcessRequest is the unified input to MetadataService.Process().
type ProcessRequest struct {
	ContentID   string            // Existing item (refresh/identify) or empty (new match)
	Hints       *MatchHints       // From scanner (initial match only)
	ProviderIDs map[string]string // From user selection (identify only)
	FolderID    string            // Which library folder
	Language    string            // ISO 639-1 metadata language (resolved from folder)
	Mode        RefreshMode
}

// ProcessResult is the output of MetadataService.Process().
type ProcessResult struct {
	ContentID string
	IsNew     bool // True if this was an initial match (new item created)
	Updated   bool // True if any fields were changed
}

// MatchHints carries scanner-extracted data into the pipeline.
// Adapted from matcher.MatchHints with FilePath added for local providers.
type MatchHints struct {
	ContentID                 string
	FileHash                  string
	TmdbID                    string
	TvdbID                    string
	ImdbID                    string
	Title                     string
	Year                      int
	Type                      string // "movie" or "series"
	SeasonNum                 int
	EpisodeNum                int
	HintSource                string // "xattr", "nfo", "folder", "filename"
	FilePath                  string // Absolute path to media file (legacy local-provider field)
	RepresentativeFilePath    string
	ObservedRootPath          string
	AllGroupFilePaths         []string
	PrimarySidecarSearchPaths []string
}

// SearchQuery is passed to SearchProvider.Search().
type SearchQuery struct {
	Title                     string
	Year                      int
	ContentType               string            // "movie" or "series"
	ProviderIDs               map[string]string // Accumulated IDs from prior providers
	Language                  string            // ISO 639-1 code from library preference
	FilePath                  string
	RepresentativeFilePath    string
	ObservedRootPath          string
	AllGroupFilePaths         []string
	PrimarySidecarSearchPaths []string
}

// SearchResult is returned from SearchProvider.Search().
type SearchResult struct {
	Name        string
	Year        int
	ProviderIDs map[string]string
	ImageURL    string
	Overview    string
	Provider    string // Slug of the provider that returned this
}

// MetadataRequest is passed to MetadataProvider.GetMetadata().
type MetadataRequest struct {
	ProviderIDs               map[string]string
	ContentType               string
	Language                  string
	FilePath                  string // Populated for local providers, empty for remote
	RepresentativeFilePath    string
	ObservedRootPath          string
	AllGroupFilePaths         []string
	PrimarySidecarSearchPaths []string
	GroupTitle                string
	GroupYear                 int
}

// PersonDetailRequest is passed to person detail lookups.
type PersonDetailRequest struct {
	ProviderIDs map[string]string
	Language    string
}

// PersonDetailResult carries person-level metadata from a provider.
type PersonDetailResult struct {
	Name            string
	SortName        string
	Bio             string
	BirthDate       string
	DeathDate       string
	Birthplace      string
	Homepage        string
	PhotoPath       string
	PhotoSourcePath string
	PhotoThumbhash  string
	ProviderIDs     map[string]string
}

// MetadataResult carries structured metadata from a single provider.
type MetadataResult struct {
	HasMetadata      bool
	ProviderIDs      map[string]string
	Title            string
	OriginalTitle    string
	SortTitle        string
	Overview         string
	Tagline          string
	Year             int
	Runtime          int
	Genres           []string
	Studios          []string
	Networks         []string
	Countries        []string
	Keywords         []string
	OriginalLanguage string
	ContentRating    string
	Ratings          Ratings
	People           []models.ItemPerson
	// Images (S3 paths or URLs).
	PosterPath        string
	PosterThumbhash   string
	BackdropPath      string
	BackdropThumbhash string
	LogoPath          string
	// Series-specific
	ReleaseDate  string
	SeasonCount  int
	FirstAirDate string
	LastAirDate  string
	AirTime      string
	AirTimezone  string
	// ShowStatus is the publication/airing status ("Ongoing", "Completed",
	// "Continuing", "Ended") when the provider reports one.
	ShowStatus string
}

// Ratings holds ratings from multiple sources.
type Ratings struct {
	IMDB       float64
	TMDB       float64
	RTCritic   float64
	RTAudience float64
}

// ImageRequest is passed to ImageProvider.GetImages().
type ImageRequest struct {
	ProviderIDs map[string]string
	ContentType string
	Language    string
}

// RemoteImage describes an available image from a provider.
type RemoteImage struct {
	ProviderID string // Slug of the provider that returned this image
	URL        string
	Type       ImageType
	Language   string
	Width      int
	Height     int
	Rating     float64 // Vote average for ordering
}

// ImageType classifies image purpose.
type ImageType int

const (
	ImagePoster ImageType = iota
	ImageBackdrop
	ImageLogo
	ImageStill // Episode stills
	ImageProfile
)

// CacheImageRequest describes an image to be cached. For season posters
// and episode stills, ContentID is the parent series's ID and the
// SeasonNumber / EpisodeNumber fields scope the S3 key so siblings do not
// collide. Both pointers are nil for item-level images.
type CacheImageRequest struct {
	SourceURL     string
	ProviderID    string
	ContentType   string // "movies" or "series"
	ContentID     string
	ImageType     ImageType
	SeasonNumber  *int
	EpisodeNumber *int
	Language      string
}

// CacheImageResult is returned by ImageCacher on success.
type CacheImageResult struct {
	BasePath  string // S3 key prefix
	Thumbhash string // base64-encoded
	Ext       string // file extension including dot (e.g. ".jpg", ".png")
}

// ImageCacher caches a remote image to object storage.
type ImageCacher interface {
	CacheImage(ctx context.Context, req CacheImageRequest) (*CacheImageResult, error)
}

type ImageCacheJobEnqueuer interface {
	Enqueue(ctx context.Context, in EnqueueImageCacheJobInput) error
	EnqueueBatch(ctx context.Context, inputs []EnqueueImageCacheJobInput) (int, error)
}

// SeasonsRequest is passed to EpisodeProvider.GetSeasons().
type SeasonsRequest struct {
	ProviderIDs map[string]string
	ContentType string
	Language    string
}

// EpisodesRequest is passed to EpisodeProvider.GetEpisodes().
type EpisodesRequest struct {
	ProviderIDs  map[string]string
	SeasonNumber int
	Language     string
}

// SeasonResult carries season data from a provider.
type SeasonResult struct {
	ContentID        string
	SeasonNumber     int
	Title            string
	Overview         string
	AirDate          string
	PosterPath       string
	PosterSourcePath string
	PosterThumbhash  string
	Episodes         []EpisodeResult
}

// EpisodeResult carries episode data from a provider.
type EpisodeResult struct {
	ContentID       string
	ProviderIDs     map[string]string
	SeasonNumber    int
	EpisodeNumber   int
	Title           string
	Overview        string
	AirDate         string
	Runtime         int
	Ratings         Ratings
	StillPath       string
	StillSourcePath string
	StillThumbhash  string
}
