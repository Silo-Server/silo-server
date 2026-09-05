package apiv2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

// The libraries domain, administrator side: the /libraries operations that
// manage media folders, their scanned roots and the metadata matcher's
// backlog. The viewer-facing /library/{id} reads live in catalog_types.go
// and its section files.

// Library is a media folder as an administrator manages it.
type Library struct {
	ID                         ID       `json:"id" example:"1"`
	Paths                      []string `json:"paths" doc:"Root directories the library scans" example:"[\"/media/movies\"]"`
	Type                       string   `json:"type" doc:"Library kind (movies, series, mixed, audiobooks, ebooks, podcasts, manga); free-form until the vocabulary is ratified (#135)" example:"movies"`
	Name                       string   `json:"name" example:"Movies"`
	Enabled                    bool     `json:"enabled" example:"true"`
	MetadataLanguage           string   `json:"metadata_language" doc:"ISO 639-1 code metadata is fetched in" example:"en"`
	AutoTranslateMetadata      bool     `json:"auto_translate_metadata" doc:"Translate descriptions when providers lack the language" example:"false"`
	ChapterThumbnailsEnabled   bool     `json:"chapter_thumbnails_enabled" example:"false"`
	ChapterThumbnailsSupported bool     `json:"chapter_thumbnails_supported" doc:"Whether the server can produce chapter thumbnails (public asset storage is configured)" example:"true"`
	IntroDetectionEnabled      bool     `json:"intro_detection_enabled" example:"false"`
	TrailerKinds               []string `json:"trailer_kinds" doc:"Remote video kinds fetched during metadata refresh; empty disables them" example:"[\"trailer\"]"`
	SortOrder                  int      `json:"sort_order" doc:"Position among libraries, lowest first" example:"0"`
	PosterURL                  string   `json:"poster_url,omitempty" doc:"Presigned poster URL; absent when the library has no poster"`
	LastScannedAt              *Instant `json:"last_scanned_at,omitempty" doc:"Absent until the first scan completes"`
	ScanWarningCode            *string  `json:"scan_warning_code,omitempty" doc:"Outstanding scan warning (empty_root, dead_root, …); absent when none" example:"empty_root"`
	ScanWarningMessage         *string  `json:"scan_warning_message,omitempty" doc:"Human-readable warning; absent when none"`
	ScanWarningAt              *Instant `json:"scan_warning_at,omitempty" doc:"When the warning was raised; absent when none"`
}

// LibraryCreate is the createLibrary body.
type LibraryCreate struct {
	Paths                    []string `json:"paths" minItems:"1" doc:"Root directories the library scans" example:"[\"/media/movies\"]"`
	Type                     string   `json:"type" minLength:"1" doc:"Library kind (movies, series, mixed, audiobooks, ebooks, podcasts, manga)" example:"movies"`
	Name                     string   `json:"name" minLength:"1" example:"Movies"`
	MetadataLanguage         string   `json:"metadata_language,omitempty" doc:"ISO 639-1 code; default en" example:"en"`
	ChapterThumbnailsEnabled bool     `json:"chapter_thumbnails_enabled,omitempty" doc:"Requires public asset storage" example:"false"`
	IntroDetectionEnabled    bool     `json:"intro_detection_enabled,omitempty" example:"false"`
	TrailerKinds             []string `json:"trailer_kinds,omitempty" doc:"Remote video kinds to fetch; omitted applies the default (every provider kind), empty disables them" example:"[\"trailer\"]"`
}

// LibraryUpdate is the updateLibrary body; omitted members are unchanged
// and no member admits null.
type LibraryUpdate struct {
	Paths                    *[]string `json:"paths,omitempty" nullable:"false" minItems:"1" doc:"Replaces every root; a changed set queues a rescan" example:"[\"/media/movies\"]"`
	Type                     *string   `json:"type,omitempty" nullable:"false" minLength:"1" example:"movies"`
	Name                     *string   `json:"name,omitempty" nullable:"false" minLength:"1" example:"Movies"`
	Enabled                  *bool     `json:"enabled,omitempty" nullable:"false" example:"true"`
	MetadataLanguage         *string   `json:"metadata_language,omitempty" nullable:"false" doc:"ISO 639-1 code; a change queues a quick metadata refresh" example:"en"`
	AutoTranslateMetadata    *bool     `json:"auto_translate_metadata,omitempty" nullable:"false" example:"false"`
	ChapterThumbnailsEnabled *bool     `json:"chapter_thumbnails_enabled,omitempty" nullable:"false" example:"false"`
	IntroDetectionEnabled    *bool     `json:"intro_detection_enabled,omitempty" nullable:"false" example:"false"`
	TrailerKinds             *[]string `json:"trailer_kinds,omitempty" nullable:"false" doc:"Replaces the allow-list; empty disables remote videos" example:"[\"trailer\"]"`
}

// LibraryCreateInput is the createLibrary request.
type LibraryCreateInput struct {
	Body LibraryCreate
}

// LibraryUpdateInput is the updateLibrary request.
type LibraryUpdateInput struct {
	ID   ID `path:"id" doc:"The library to update" example:"1"`
	Body LibraryUpdate
	// RawBody lets the handler refuse explicit nulls, which the framework
	// would otherwise read as absence.
	RawBody []byte
}

// LibraryIDInput names one library.
type LibraryIDInput struct {
	ID ID `path:"id" doc:"The library" example:"1"`
}

// LibraryOutput is a single-library response.
type LibraryOutput struct {
	Body Library
}

// LibraryCreatedOutput is the createLibrary response.
type LibraryCreatedOutput struct {
	Location string `header:"Location" doc:"URL of the created library"`
	Body     Library
}

// LibraryCollectionOutput is the listLibraries response.
type LibraryCollectionOutput struct {
	Body LibraryCollection
}

// LibraryCollection is the named envelope the contract carries.
type LibraryCollection struct {
	Collection[Library]
}

// AdminJobAcceptedOutput is the answer of an operation that queued a job:
// the job resource and, per the lifecycle convention for accepted
// asynchronous work, its canonical URI in Location.
type AdminJobAcceptedOutput struct {
	Location string `header:"Location" doc:"URI of the queued job"`
	Body     AdminJob
}

// LibraryMountCheckRoot is one root's reachability.
type LibraryMountCheckRoot struct {
	Path         string  `json:"path" example:"/media/movies"`
	Reachable    bool    `json:"reachable" example:"true"`
	ErrorCode    *string `json:"error_code" nullable:"true" doc:"Why the root is unreachable; null when it is" example:"not_found"`
	ErrorMessage *string `json:"error_message" nullable:"true" doc:"Human-readable reason; null when reachable"`
	SuspectEmpty bool    `json:"suspect_empty" doc:"Reachable but every known file under it is missing: the signature of a lost mount exposing an empty mountpoint" example:"false"`
}

// LibraryMountCheck is the result of probing a library's roots.
type LibraryMountCheck struct {
	Status      string                  `json:"status" doc:"Overall verdict" example:"healthy"`
	LibraryID   ID                      `json:"library_id" example:"1"`
	LibraryName string                  `json:"library_name" example:"Movies"`
	Healthy     bool                    `json:"healthy" example:"true"`
	CheckedAt   Instant                 `json:"checked_at" example:"2026-01-02T03:04:05.678Z"`
	Summary     string                  `json:"summary" example:"All 1 roots reachable"`
	Roots       []LibraryMountCheckRoot `json:"roots"`
}

// LibraryMountCheckOutput is the checkLibraryMount response.
type LibraryMountCheckOutput struct {
	Body LibraryMountCheck
}

// MetadataMatchQueueStatus is one library's matcher backlog.
type MetadataMatchQueueStatus struct {
	LibraryID    ID  `json:"library_id" example:"1"`
	MovieCount   int `json:"movie_count" example:"0"`
	SeriesCount  int `json:"series_count" example:"0"`
	RawFileCount int `json:"raw_file_count" example:"0"`
	TotalCount   int `json:"total_count" example:"0"`
	PendingCount int `json:"pending_count" example:"0"`
	ParkedCount  int `json:"parked_count" doc:"Entries the matcher gave up on until an operator retries them" example:"0"`
}

// MetadataMatchQueueStatusCollectionOutput is the listMetadataMatchQueues response.
type MetadataMatchQueueStatusCollectionOutput struct {
	Body MetadataMatchQueueStatusCollection
}

// MetadataMatchQueueStatusCollection is the named envelope the contract carries.
type MetadataMatchQueueStatusCollection struct {
	Collection[MetadataMatchQueueStatus]
}

// ProviderChainEntry is one metadata provider at one content level.
type ProviderChainEntry struct {
	PluginInstallationID ID     `json:"plugin_installation_id" example:"3"`
	CapabilityID         string `json:"capability_id" example:"tmdb"`
	ProviderSlug         string `json:"provider_slug" example:"tmdb"`
	Priority             int    `json:"priority" doc:"Lower runs first" example:"0"`
	Enabled              bool   `json:"enabled" example:"true"`
}

// ProviderChainLevel is the provider chain at one content level.
type ProviderChainLevel struct {
	ContentLevel string               `json:"content_level" example:"movie"`
	Entries      []ProviderChainEntry `json:"entries"`
}

// LibraryProviderDefaults is the chain a new library would be seeded with.
type LibraryProviderDefaults struct {
	Levels []ProviderChainLevel `json:"levels" doc:"One entry per content level the type has, in level-name order; empty for a type the server seeds no chain for"`
}

// LibraryProviderDefaultsInput is the getLibraryProviderDefaults query.
type LibraryProviderDefaultsInput struct {
	LibraryType string `query:"library_type" required:"true" minLength:"1" doc:"Library kind to seed for" example:"movies"`
}

// LibraryProviderDefaultsOutput is the getLibraryProviderDefaults response.
type LibraryProviderDefaultsOutput struct {
	Body LibraryProviderDefaults
}

// LibraryOrderEntry assigns one library its position.
type LibraryOrderEntry struct {
	ID       ID  `json:"id" example:"1"`
	Position int `json:"position" minimum:"0" example:"0"`
}

// LibraryReorder is the reorderLibraries body.
type LibraryReorder struct {
	Entries []LibraryOrderEntry `json:"entries" doc:"Libraries and their positions; libraries not named keep their order after the named ones"`
}

// LibraryReorderInput is the reorderLibraries request.
type LibraryReorderInput struct {
	Body LibraryReorder
}

// RootOverride is an operator's identity override on a scanned root.
type RootOverride struct {
	ForcedType   string `json:"forced_type,omitempty" example:"movie"`
	ForcedTitle  string `json:"forced_title,omitempty" example:"Heat"`
	ForcedYear   int    `json:"forced_year,omitempty" example:"1995"`
	ForcedTmdbID string `json:"forced_tmdb_id,omitempty" example:"949"`
	ForcedImdbID string `json:"forced_imdb_id,omitempty" example:"tt0113277"`
	ForcedTvdbID string `json:"forced_tvdb_id,omitempty"`
	Note         string `json:"note,omitempty"`
}

// LibraryRoot is one scanned content root inside a library.
type LibraryRoot struct {
	LibraryID         ID              `json:"library_id" example:"1"`
	LibraryName       string          `json:"library_name" example:"Movies"`
	RootPath          string          `json:"root_path" example:"/media/movies/Heat (1995)"`
	State             string          `json:"state" doc:"Inference state of the root" example:"resolved"`
	InferredType      string          `json:"inferred_type" example:"movie"`
	TypeConfidence    string          `json:"type_confidence" example:"high"`
	Title             string          `json:"title" example:"Heat"`
	Year              int             `json:"year" doc:"0 when unknown" example:"1995"`
	TmdbID            string          `json:"tmdb_id,omitempty" example:"949"`
	ImdbID            string          `json:"imdb_id,omitempty" example:"tt0113277"`
	TvdbID            string          `json:"tvdb_id,omitempty"`
	ObservedFileCount int             `json:"observed_file_count" example:"1"`
	SampleFilePath    string          `json:"sample_file_path,omitempty" example:"/media/movies/Heat (1995)/Heat.mkv"`
	Evidence          json.RawMessage `json:"evidence_json,omitempty" doc:"The scanner's inference evidence, as recorded"`
	OverrideSource    string          `json:"override_source,omitempty" doc:"Where the active identity came from when overridden"`
	FirstSeenAt       Instant         `json:"first_seen_at" example:"2026-01-02T03:04:05.678Z"`
	LastSeenAt        Instant         `json:"last_seen_at" example:"2026-01-02T03:04:05.678Z"`
	ActiveOverride    *RootOverride   `json:"active_override,omitempty" doc:"Absent when no operator override applies"`
	ContentID         string          `json:"content_id,omitempty" doc:"The catalog item this root matched; absent when unmatched"`
}

// LibraryRootListInput is the listLibraryRoots query.
type LibraryRootListInput struct {
	LimitParam
	LibraryID ID     `query:"library_id" required:"true" doc:"The library whose roots to list" example:"1"`
	State     string `query:"state" doc:"Only roots in this inference state" example:"ambiguous"`
	Query     string `query:"q" doc:"Case-insensitive substring over root path, title and sample file path" example:"heat"`
	Cursor    string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// LibraryRootCollection is the named envelope the contract carries, with
// the unpaged total the admin UI shows.
type LibraryRootCollection struct {
	Collection[LibraryRoot]
	Total int `json:"total" doc:"Roots matching the filter across every page" example:"1"`
}

// LibraryRootCollectionOutput is the listLibraryRoots response.
type LibraryRootCollectionOutput struct {
	Body LibraryRootCollection
}

// RootOverrideSet is the setRootOverride body.
type RootOverrideSet struct {
	LibraryID ID     `json:"library_id" example:"1"`
	RootPath  string `json:"root_path" minLength:"1" doc:"The root, as listLibraryRoots reports it" example:"/media/movies/Heat (1995)"`
	RootOverride
}

// RootOverrideSetInput is the setRootOverride request.
type RootOverrideSetInput struct {
	Body RootOverrideSet
}

// RootOverrideDeleteInput is the deleteRootOverride query.
type RootOverrideDeleteInput struct {
	LibraryID ID     `query:"library_id" required:"true" example:"1"`
	RootPath  string `query:"root_path" required:"true" minLength:"1" doc:"The root, as listLibraryRoots reports it" example:"/media/movies/Heat (1995)"`
}

// SkippedRoot is a root the scanner skipped.
type SkippedRoot struct {
	LibraryID      ID      `json:"library_id" example:"1"`
	LibraryName    string  `json:"library_name" example:"Movies"`
	RootPath       string  `json:"root_path" example:"/media/movies/Extras"`
	Reason         string  `json:"reason" example:"no_media_files"`
	SampleFilePath string  `json:"sample_file_path" example:"/media/movies/Extras/notes.txt"`
	FileCount      int     `json:"file_count" example:"3"`
	FirstSeenAt    Instant `json:"first_seen_at" example:"2026-01-02T03:04:05.678Z"`
	LastSeenAt     Instant `json:"last_seen_at" example:"2026-01-02T03:04:05.678Z"`
}

// SkippedRootCollectionOutput is the listSkippedRoots response.
type SkippedRootListInput struct {
	LimitParam
	Query  string `query:"q" doc:"Substring over root path, library name or reason"`
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor"`
}

type SkippedRootCollectionOutput struct {
	Body SkippedRootCollection
}

// SkippedRootCollection is the named envelope the contract carries.
type SkippedRootCollection struct {
	Collection[SkippedRoot]
}

// StaleMediaID is a provider identifier a provider no longer resolves.
type StaleMediaID struct {
	ContentID   string  `json:"content_id" example:"movie:heat-1995"`
	LibraryID   ID      `json:"library_id" doc:"\"0\" when the item has no file in any library" example:"1"`
	LibraryName string  `json:"library_name" example:"Movies"`
	Title       string  `json:"title" example:"Heat"`
	Year        int     `json:"year" example:"1995"`
	ContentType string  `json:"content_type" example:"movie"`
	Provider    string  `json:"provider" example:"tmdb"`
	ProviderID  string  `json:"provider_id" example:"949"`
	FirstSeenAt Instant `json:"first_seen_at" example:"2026-01-02T03:04:05.678Z"`
	LastSeenAt  Instant `json:"last_seen_at" example:"2026-01-02T03:04:05.678Z"`
}

// StaleMediaIDListInput is the listStaleIds query.
type StaleMediaIDListInput struct {
	Query string `query:"q" doc:"Substring over title, provider, provider ID or library name"`
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// StaleMediaIDCollectionOutput is the listStaleIds response.
type StaleMediaIDCollectionOutput struct {
	Body StaleMediaIDCollection
}

// StaleMediaIDCollection is the named envelope the contract carries.
type StaleMediaIDCollection struct {
	Collection[StaleMediaID]
}

// StaleIDRematchInput names the item to rematch.
type StaleIDRematchInput struct {
	ContentID string `path:"content_id" minLength:"1" doc:"The catalog item" example:"movie:heat-1995"`
}

// UnmatchedItem is a catalog item awaiting a metadata match.
type UnmatchedItem struct {
	ContentID   string `json:"content_id" example:"movie:heat-1995"`
	Title       string `json:"title" example:"Heat"`
	Year        int    `json:"year" example:"1995"`
	ContentType string `json:"content_type" example:"movie"`
	LibraryID   ID     `json:"library_id" doc:"\"0\" when the item is in no library" example:"1"`
	LibraryName string `json:"library_name" example:"Movies"`
	Status      string `json:"status" doc:"unmatched, pending or ambiguous" example:"unmatched"`
}

// UnmatchedItemListInput is the listUnmatchedItems query.
type UnmatchedItemListInput struct {
	LimitParam
	Query  string `query:"q" doc:"Case-insensitive substring over title, type, status and library name" example:"heat"`
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// UnmatchedItemCollection is the named envelope the contract carries, with
// the unpaged total the admin UI shows.
type UnmatchedItemCollection struct {
	Collection[UnmatchedItem]
	Total int `json:"total" doc:"Items matching the filter across every page" example:"1"`
}

// UnmatchedItemCollectionOutput is the listUnmatchedItems response.
type UnmatchedItemCollectionOutput struct {
	Body UnmatchedItemCollection
}

// EmptyRootCleanup acknowledges an armed cleanup.
type EmptyRootCleanup struct {
	Status  string `json:"status" example:"ok"`
	Message string `json:"message" example:"Empty-root cleanup confirmed for next scan"`
}

// EmptyRootCleanupOutput is the confirmEmptyRootCleanup response.
type EmptyRootCleanupOutput struct {
	Body EmptyRootCleanup
}

// MetadataMatchQueueDetailInput is the getMetadataMatchQueue query.
type MetadataMatchQueueDetailInput struct {
	ID     ID     `path:"id" doc:"Library identifier" example:"1"`
	Limit  int    `query:"limit" minimum:"1" maximum:"50" default:"10" doc:"Rows per list; default 10, maximum 50" example:"10"`
	Cursor string `query:"cursor" doc:"Opaque cursor from the previous page" example:""`
}

// MovieMatchQueueEntry is one movie file the matcher has queued.
type MovieMatchQueueEntry struct {
	MediaFileID               ID              `json:"media_file_id" example:"120"`
	LibraryID                 ID              `json:"library_id" example:"1"`
	FilePath                  string          `json:"file_path" example:"/media/movies/Heat (1995)/Heat.mkv"`
	FirstQueuedAt             Instant         `json:"first_queued_at" example:"2026-01-02T03:04:05.678Z"`
	AvailableAt               Instant         `json:"available_at" doc:"When the matcher may next try" example:"2026-01-02T03:04:05.678Z"`
	LastAttemptedAt           *Instant        `json:"last_attempted_at,omitempty"`
	AttemptCount              int             `json:"attempt_count" example:"1"`
	LastError                 string          `json:"last_error,omitempty"`
	State                     string          `json:"state" doc:"pending or parked" example:"pending"`
	FailureKind               string          `json:"failure_kind,omitempty"`
	FailureDetail             json.RawMessage `json:"failure_detail,omitempty" doc:"Matcher-specific failure document"`
	DeterministicAttemptCount int             `json:"deterministic_attempt_count" example:"0"`
	MatcherRevision           int             `json:"matcher_revision" example:"3"`
	ParkedAt                  *Instant        `json:"parked_at,omitempty"`
	UpdatedAt                 Instant         `json:"updated_at" example:"2026-01-02T03:04:05.678Z"`
}

// SeriesMatchQueueEntry is one series root the matcher has queued.
type SeriesMatchQueueEntry struct {
	LibraryID                 ID              `json:"library_id" example:"1"`
	ObservedRootPath          string          `json:"observed_root_path" example:"/media/tv/Severance"`
	FirstQueuedAt             Instant         `json:"first_queued_at" example:"2026-01-02T03:04:05.678Z"`
	AvailableAt               Instant         `json:"available_at" example:"2026-01-02T03:04:05.678Z"`
	LastAttemptedAt           *Instant        `json:"last_attempted_at,omitempty"`
	AttemptCount              int             `json:"attempt_count" example:"1"`
	LastError                 string          `json:"last_error,omitempty"`
	State                     string          `json:"state" example:"pending"`
	FailureKind               string          `json:"failure_kind,omitempty"`
	FailureDetail             json.RawMessage `json:"failure_detail,omitempty" doc:"Matcher-specific failure document"`
	DeterministicAttemptCount int             `json:"deterministic_attempt_count" example:"0"`
	MatcherRevision           int             `json:"matcher_revision" example:"3"`
	ParkedAt                  *Instant        `json:"parked_at,omitempty"`
	UpdatedAt                 Instant         `json:"updated_at" example:"2026-01-02T03:04:05.678Z"`
}

// RawMatchBacklogEntry is one scanned file no matcher has claimed.
type RawMatchBacklogEntry struct {
	MediaFileID     ID       `json:"media_file_id" example:"121"`
	LibraryID       ID       `json:"library_id" example:"1"`
	FilePath        string   `json:"file_path" example:"/media/movies/unknown.mkv"`
	BaseTitle       string   `json:"base_title,omitempty" example:"unknown"`
	BaseYear        int      `json:"base_year,omitempty"`
	BaseType        string   `json:"base_type,omitempty" example:"movie"`
	LastAttemptedAt *Instant `json:"last_attempted_at,omitempty"`
	CreatedAt       Instant  `json:"created_at" example:"2026-01-02T03:04:05.678Z"`
	UpdatedAt       Instant  `json:"updated_at" example:"2026-01-02T03:04:05.678Z"`
}

// MetadataMatchQueueDetail is one library's backlog with a page of entries.
type MetadataMatchQueueDetail struct {
	MetadataMatchQueueStatus
	Movies   []MovieMatchQueueEntry  `json:"movies" doc:"Empty, never null"`
	Series   []SeriesMatchQueueEntry `json:"series" doc:"Empty, never null"`
	RawFiles []RawMatchBacklogEntry  `json:"raw_files" doc:"Empty, never null"`
	Page     PageInfo                `json:"page" doc:"The three lists page together"`
}

// MetadataMatchQueueDetailOutput is the getMetadataMatchQueue response.
type MetadataMatchQueueDetailOutput struct {
	Body MetadataMatchQueueDetail
}

// MetadataMatchQueueAction is what a cancel or retry did.
type MetadataMatchQueueAction struct {
	Status           string                   `json:"status" doc:"queued after a retry, cancelled after a cancel" example:"queued"` //nolint:misspell // v1 wire value
	LibraryID        ID                       `json:"library_id" example:"1"`
	MovieCancelled   int                      `json:"movie_cancelled,omitempty"`    //nolint:misspell // v1 wire name
	SeriesCancelled  int                      `json:"series_cancelled,omitempty"`   //nolint:misspell // v1 wire name
	RawFileCancelled int                      `json:"raw_file_cancelled,omitempty"` //nolint:misspell // v1 wire name
	RawFileRetried   int                      `json:"raw_file_retried,omitempty"`
	TotalCancelled   int                      `json:"total_cancelled,omitempty"` //nolint:misspell // v1 wire name
	Queue            MetadataMatchQueueStatus `json:"queue" doc:"The counts afterwards"`
}

// MetadataMatchQueueActionOutput is the cancel and retry response.
type MetadataMatchQueueActionOutput struct {
	Body MetadataMatchQueueAction
}

// LibraryRefresh chooses the refresh depth.
type LibraryRefresh struct {
	Mode string `json:"mode,omitempty" enum:"quick,full" doc:"quick refreshes stale items only; full refreshes every item. Default quick" example:"quick"`
}

// LibraryRefreshInput is the refreshLibraryMetadata request.
type LibraryRefreshInput struct {
	ID   ID              `path:"id" doc:"Library identifier" example:"1"`
	Body *LibraryRefresh `required:"false" doc:"Absent picks a quick refresh"`
}

// LibraryProviders is a library's provider chain.
type LibraryProviders struct {
	Levels []ProviderChainLevel `json:"levels" doc:"One entry per content level with a chain, in level-name order"`
}

// LibraryProvidersOutput is the getLibraryProviders response.
type LibraryProvidersOutput struct {
	Body LibraryProviders
}

// ProviderChainEntryInput is one entry of a chain being set.
type ProviderChainEntryInput struct {
	PluginInstallationID ID     `json:"plugin_installation_id" example:"3"`
	CapabilityID         string `json:"capability_id" minLength:"1" example:"tmdb"`
	Priority             int    `json:"priority" doc:"Lower runs first" example:"0"`
	Enabled              bool   `json:"enabled" example:"true"`
}

// ProviderChainLevelInput is the chain to set at one content level.
type ProviderChainLevelInput struct {
	ContentLevel string                    `json:"content_level" minLength:"1" example:"movie"`
	Entries      []ProviderChainEntryInput `json:"entries"`
}

// LibraryProvidersSet is the whole chain to install; a level not listed
// ends up with no providers.
type LibraryProvidersSet struct {
	Levels []ProviderChainLevelInput `json:"levels"`
}

// LibraryProvidersSetInput is the setLibraryProviders request.
type LibraryProvidersSetInput struct {
	ID   ID `path:"id" doc:"Library identifier" example:"1"`
	Body LibraryProvidersSet
}

// LibraryPosterForm is the uploadLibraryPoster multipart form.
type LibraryPosterForm struct {
	Poster huma.FormFile `form:"poster" contentType:"image/jpeg,image/png,image/webp" required:"true" doc:"The image file: image/jpeg, image/png or image/webp, at most 10 MiB"`
}

// LibraryPosterUploadInput is the uploadLibraryPoster request.
type LibraryPosterUploadInput struct {
	ID      ID `path:"id" doc:"Library identifier" example:"1"`
	RawBody huma.MultipartFormFiles[LibraryPosterForm]
}

// offsetPosition is the cursor payload of the administrative listings that
// page by offset underneath: the number of rows the previous pages emitted.
type offsetPosition struct {
	Offset int `json:"o"`
}

const (
	opUpdateLibrary            = "updateLibrary"
	opRematchStaleId           = "rematchStaleId"
	opSetLibraryProviders      = "setLibraryProviders"
	opConfirmEmptyRootCleanup  = "confirmEmptyRootCleanup"
	opRetryMetadataMatchQueue  = "retryMetadataMatchQueue"
	opDeleteLibraryPoster      = "deleteLibraryPoster"
	opDeleteLibrary            = "deleteLibrary"
	opReorderLibraries         = "reorderLibraries"
	opSetRootOverride          = "setRootOverride"
	opDeleteRootOverride       = "deleteRootOverride"
	opCreateLibrary            = "createLibrary"
	opCancelMetadataMatchQueue = "cancelMetadataMatchQueue"
	opCheckLibraryMount        = "checkLibraryMount"
	opRefreshLibraryMetadata   = "refreshLibraryMetadata"
	opListLibraryRoots         = "listLibraryRoots"
	opListUnmatchedItems       = "listUnmatchedItems"
	opListStaleIDs             = "listStaleIds"
	opListSkippedRoots         = "listSkippedRoots"
	tiebreakerContentID        = "content_id"
	locationBodyLibraryID      = locationBody + ".library_id"
	locationQueryLibraryID     = "query.library_id"
	detailNotLibraryID         = "not a library identifier"
)

func registerLibraries(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	admin := func(op huma.Operation) Operation {
		operation := Operation{Operation: op, Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true}
		switch op.OperationID {
		case opCreateLibrary, opUpdateLibrary, opRematchStaleId, opSetLibraryProviders,
			opConfirmEmptyRootCleanup, opRetryMetadataMatchQueue, opCancelMetadataMatchQueue, opDeleteLibraryPoster:
			operation.RetrySafety = RetrySafetyNonRetryable
		case opDeleteLibrary, opCheckLibraryMount, opReorderLibraries, opSetRootOverride, opDeleteRootOverride:
			operation.RetrySafety = RetrySafetyNaturalIdempotent
		case opRefreshLibraryMetadata:
			operation.RetrySafety = RetrySafetyCoalescing
		}
		return operation
	}

	Register(reg, admin(humaOp(http.MethodGet, Prefix+"/libraries", "listLibraries", "libraries",
		"List every library in sort order, with its presigned poster URL.")), reg.listLibraries)

	create := humaOp(http.MethodPost, Prefix+"/libraries", opCreateLibrary, "libraries",
		"Create a library, seed its sections and provider chain, and queue its first scan.")
	create.DefaultStatus = http.StatusCreated
	create.Errors = []int{http.StatusConflict}
	Register(reg, admin(create), reg.createLibrary)

	update := humaOp(http.MethodPatch, Prefix+"/libraries/{id}", opUpdateLibrary, "libraries",
		"Update a library; omitted members are unchanged. A changed path set queues a rescan and a changed language a quick metadata refresh.")
	update.Errors = []int{http.StatusConflict}
	Register(reg, admin(update), reg.updateLibrary)

	del := humaOp(http.MethodDelete, Prefix+"/libraries/{id}", opDeleteLibrary, "libraries",
		"Disable a library and queue the job that deletes it and its items; answers 202 with the job.")
	del.DefaultStatus = http.StatusAccepted
	del.Errors = []int{http.StatusConflict}
	Register(reg, admin(del), reg.deleteLibrary)

	Register(reg, admin(humaOp(http.MethodPost, Prefix+"/libraries/{id}/check-mount", opCheckLibraryMount, "libraries",
		"Probe every root of a library; a healthy result clears an outstanding empty-root or dead-root warning.")), reg.checkLibraryMount)

	Register(reg, admin(humaOp(http.MethodGet, Prefix+"/libraries/metadata-match-queue", "listMetadataMatchQueues", "libraries",
		"List the metadata matcher backlog of every library.")), reg.listMetadataMatchQueues)

	Register(reg, admin(humaOp(http.MethodGet, Prefix+"/libraries/provider-defaults", "getLibraryProviderDefaults", "libraries",
		"The provider chain a new library of a type would be seeded with, per content level.")), reg.getLibraryProviderDefaults)

	Register(reg, admin(humaOp(http.MethodPost, Prefix+"/libraries/reorder", opReorderLibraries, "libraries",
		"Assign libraries their sort positions.")), reg.reorderLibraries)

	roots := humaOp(http.MethodGet, Prefix+"/libraries/roots", opListLibraryRoots, "libraries",
		"Page the scanned content roots of one library with their active overrides.")
	roots.Errors = []int{http.StatusNotFound}
	Register(reg, admin(roots), func(ctx context.Context, in *LibraryRootListInput) (*LibraryRootCollectionOutput, error) {
		return reg.listLibraryRoots(ctx, cursors, in)
	})

	setOverride := humaOp(http.MethodPut, Prefix+"/libraries/roots/override", opSetRootOverride, "libraries",
		"Set the identity override on a scanned root, replacing any existing one.")
	setOverride.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, admin(setOverride), reg.setRootOverride)

	deleteOverride := humaOp(http.MethodDelete, Prefix+"/libraries/roots/override", opDeleteRootOverride, "libraries",
		"Remove the identity override on a scanned root.")
	deleteOverride.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, admin(deleteOverride), reg.deleteRootOverride)

	Register(reg, admin(humaOp(http.MethodGet, Prefix+"/libraries/skipped-roots", opListSkippedRoots, "libraries",
		"Page roots the scanner skipped, across libraries.")), func(ctx context.Context, in *SkippedRootListInput) (*SkippedRootCollectionOutput, error) {
		return reg.listSkippedRoots(ctx, cursors, in)
	})

	Register(reg, admin(humaOp(http.MethodGet, Prefix+"/libraries/stale-ids", opListStaleIDs, "libraries",
		"List provider identifiers that no longer resolve, with the items carrying them, a page at a time.")), func(ctx context.Context, in *StaleMediaIDListInput) (*StaleMediaIDCollectionOutput, error) {
		return reg.listStaleIDs(ctx, cursors, in)
	})

	Register(reg, admin(humaOp(http.MethodPost, Prefix+"/libraries/stale-ids/{content_id}/rematch", opRematchStaleId, "libraries",
		"Clear an item's provider identifiers and refresh its metadata in the background.")), reg.rematchStaleID)

	Register(reg, admin(humaOp(http.MethodGet, Prefix+"/libraries/unmatched-items", opListUnmatchedItems, "libraries",
		"Page the catalog items awaiting a metadata match, in title order.")), func(ctx context.Context, in *UnmatchedItemListInput) (*UnmatchedItemCollectionOutput, error) {
		return reg.listUnmatchedItems(ctx, cursors, in)
	})

	Register(reg, admin(humaOp(http.MethodPost, Prefix+"/libraries/{id}/confirm-empty-root-cleanup", opConfirmEmptyRootCleanup, "libraries",
		"Arm the library's next scan to clean up an empty root once instead of treating it as a lost mount.")), reg.confirmEmptyRootCleanup)

	Register(reg, admin(humaOp(http.MethodGet, Prefix+"/libraries/{id}/metadata-match-queue", opGetMetadataMatchQueue, "libraries",
		"One library's matcher backlog counts with a page of its queued movies, series roots and raw files.")), func(ctx context.Context, in *MetadataMatchQueueDetailInput) (*MetadataMatchQueueDetailOutput, error) {
		return reg.getMetadataMatchQueue(ctx, cursors, in)
	})
	Register(reg, admin(humaOp(http.MethodPost, Prefix+"/libraries/{id}/metadata-match-queue/retry", opRetryMetadataMatchQueue, "libraries",
		"Re-sync and immediately retry every queued matcher entry of the library; answers the counts afterwards.")), reg.retryMetadataMatchQueue)
	Register(reg, admin(humaOp(http.MethodPost, Prefix+"/libraries/{id}/metadata-match-queue/cancel", opCancelMetadataMatchQueue, "libraries",
		"Drop every queued matcher entry of the library and suppress its raw backlog; answers what was canceled.")), reg.cancelMetadataMatchQueue)

	refresh := humaOp(http.MethodPost, Prefix+"/libraries/{id}/refresh-metadata", opRefreshLibraryMetadata, "libraries",
		"Queue a metadata refresh of the library's items; answers 202 with the job.")
	refresh.DefaultStatus = http.StatusAccepted
	refresh.Errors = []int{http.StatusConflict}
	Register(reg, admin(refresh), reg.refreshLibraryMetadata)

	Register(reg, admin(humaOp(http.MethodGet, Prefix+"/libraries/{id}/providers", "getLibraryProviders", "libraries",
		"The library's metadata provider chain, per content level. Legacy unlevelled rows (content_level '') that an upgraded database keeps are not exposed; setLibraryProviders preserves them.")), reg.getLibraryProviders)
	setProviders := humaOp(http.MethodPut, Prefix+"/libraries/{id}/providers", opSetLibraryProviders, "libraries",
		"Replace the library's whole provider chain and wake the matcher. Legacy unlevelled rows (content_level '') are kept as they are; a level not listed ends up with no providers.")
	setProviders.DefaultStatus = http.StatusNoContent
	Register(reg, admin(setProviders), reg.setLibraryProviders)

	upload := humaOp(http.MethodPut, Prefix+"/libraries/{id}/poster", "uploadLibraryPoster", "libraries",
		"Store a poster for the library from a multipart upload (JPEG, PNG or WebP, at most 10 MiB) and answer the library with its new poster URL.")
	Register(reg, Operation{Operation: upload, RetrySafety: RetrySafetyNonRetryable, Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, MaxBodyBytes: maxPosterBytes + posterFormOverhead}, reg.uploadLibraryPoster)

	deletePoster := humaOp(http.MethodDelete, Prefix+"/libraries/{id}/poster", opDeleteLibraryPoster, "libraries",
		"Remove the library's poster; a library without one is left as is.")
	deletePoster.DefaultStatus = http.StatusNoContent
	Register(reg, admin(deletePoster), reg.deleteLibraryPoster)
}

// opGetMetadataMatchQueue is the operation id; the cursor scope is bound to it.
const opGetMetadataMatchQueue = "getMetadataMatchQueue"

// maxPosterBytes is the poster size v1 accepts; posterFormOverhead leaves
// room for the multipart framing around it.
const (
	maxPosterBytes     = 10 << 20
	posterFormOverhead = 1 << 20
)

func (reg *Registry) confirmEmptyRootCleanup(ctx context.Context, in *LibraryIDInput) (*EmptyRootCleanupOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	if err := svc.ConfirmEmptyRootCleanup(ctx, id); err != nil {
		return nil, libraryProblem(err)
	}
	return &EmptyRootCleanupOutput{Body: EmptyRootCleanup{Status: "ok", Message: "Empty-root cleanup confirmed for next scan"}}, nil
}

func (reg *Registry) getMetadataMatchQueue(ctx context.Context, cursors *Cursors, in *MetadataMatchQueueDetailInput) (*MetadataMatchQueueDetailOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{OperationID: opGetMetadataMatchQueue, Security: strconv.Itoa(userID), Filter: string(in.ID), Sort: "queue", Tiebreaker: "offset"}
	offset, p := decodeOffset(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	view, err := svc.GetMetadataMatchQueue(ctx, id, in.Limit, offset)
	if err != nil {
		return nil, libraryProblem(err)
	}
	out := MetadataMatchQueueDetail{
		MetadataMatchQueueStatus: MetadataMatchQueueStatus{
			LibraryID: IDFromInt(int64(view.LibraryID)), MovieCount: view.MovieCount, SeriesCount: view.SeriesCount, RawFileCount: view.RawFileCount,
			TotalCount: view.TotalCount, PendingCount: view.PendingCount, ParkedCount: view.ParkedCount,
		},
		Movies:   make([]MovieMatchQueueEntry, 0, len(view.Movies)),
		Series:   make([]SeriesMatchQueueEntry, 0, len(view.Series)),
		RawFiles: make([]RawMatchBacklogEntry, 0, len(view.RawFiles)),
	}
	for _, e := range view.Movies {
		out.Movies = append(out.Movies, MovieMatchQueueEntry{
			MediaFileID: IDFromInt(int64(e.MediaFileID)), LibraryID: IDFromInt(int64(e.MediaFolderID)), FilePath: e.FilePath,
			FirstQueuedAt: NewInstant(e.FirstQueuedAt), AvailableAt: NewInstant(e.AvailableAt), LastAttemptedAt: instantPtr(e.LastAttemptedAt),
			AttemptCount: e.AttemptCount, LastError: e.LastError, State: e.State, FailureKind: e.FailureKind, FailureDetail: e.FailureDetail,
			DeterministicAttemptCount: e.DeterministicAttemptCount, MatcherRevision: e.MatcherRevision, ParkedAt: instantPtr(e.ParkedAt), UpdatedAt: NewInstant(e.UpdatedAt),
		})
	}
	for _, e := range view.Series {
		out.Series = append(out.Series, SeriesMatchQueueEntry{
			LibraryID: IDFromInt(int64(e.MediaFolderID)), ObservedRootPath: e.ObservedRootPath,
			FirstQueuedAt: NewInstant(e.FirstQueuedAt), AvailableAt: NewInstant(e.AvailableAt), LastAttemptedAt: instantPtr(e.LastAttemptedAt),
			AttemptCount: e.AttemptCount, LastError: e.LastError, State: e.State, FailureKind: e.FailureKind, FailureDetail: e.FailureDetail,
			DeterministicAttemptCount: e.DeterministicAttemptCount, MatcherRevision: e.MatcherRevision, ParkedAt: instantPtr(e.ParkedAt), UpdatedAt: NewInstant(e.UpdatedAt),
		})
	}
	for _, e := range view.RawFiles {
		out.RawFiles = append(out.RawFiles, RawMatchBacklogEntry{
			MediaFileID: IDFromInt(int64(e.MediaFileID)), LibraryID: IDFromInt(int64(e.MediaFolderID)), FilePath: e.FilePath,
			BaseTitle: e.BaseTitle, BaseYear: e.BaseYear, BaseType: e.BaseType, LastAttemptedAt: instantPtr(e.LastAttemptedAt),
			CreatedAt: NewInstant(e.CreatedAt), UpdatedAt: NewInstant(e.UpdatedAt),
		})
	}
	// The three lists page in lockstep, as v1's offset did; a next page
	// exists while any of them has rows past this one.
	next := ""
	if end := offset + in.Limit; view.MovieTotal > end || view.SeriesTotal > end || view.RawFileTotal > end {
		next, err = cursors.Encode(scope, offsetPosition{Offset: end})
		if err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	out.Page = PageInfo{NextCursor: next, HasMore: next != ""}
	return &MetadataMatchQueueDetailOutput{Body: out}, nil
}

func matchQueueActionOf(v handlers.MetadataMatchQueueActionView) MetadataMatchQueueAction {
	return MetadataMatchQueueAction{
		Status: v.Status, LibraryID: IDFromInt(int64(v.LibraryID)),
		MovieCancelled: v.MovieCancelled, SeriesCancelled: v.SeriesCancelled, RawFileCancelled: v.RawFileCancelled,
		RawFileRetried: v.RawFileRetried, TotalCancelled: v.TotalCancelled,
		Queue: MetadataMatchQueueStatus{
			LibraryID: IDFromInt(int64(v.Queue.LibraryID)), MovieCount: v.Queue.MovieCount, SeriesCount: v.Queue.SeriesCount, RawFileCount: v.Queue.RawFileCount,
			TotalCount: v.Queue.TotalCount, PendingCount: v.Queue.PendingCount, ParkedCount: v.Queue.ParkedCount,
		},
	}
}

func (reg *Registry) retryMetadataMatchQueue(ctx context.Context, in *LibraryIDInput) (*MetadataMatchQueueActionOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	view, err := svc.RetryMetadataMatchQueue(ctx, id)
	if err != nil {
		return nil, libraryProblem(err)
	}
	return &MetadataMatchQueueActionOutput{Body: matchQueueActionOf(view)}, nil
}

func (reg *Registry) cancelMetadataMatchQueue(ctx context.Context, in *LibraryIDInput) (*MetadataMatchQueueActionOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	view, err := svc.CancelMetadataMatchQueue(ctx, id)
	if err != nil {
		return nil, libraryProblem(err)
	}
	return &MetadataMatchQueueActionOutput{Body: matchQueueActionOf(view)}, nil
}

func (reg *Registry) refreshLibraryMetadata(ctx context.Context, in *LibraryRefreshInput) (*AdminJobAcceptedOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	mode := adminjob.LibraryRefreshModeQuick
	if in.Body != nil && in.Body.Mode != "" {
		mode = adminjob.LibraryRefreshMode(in.Body.Mode)
	}
	job, err := svc.RefreshLibraryMetadata(ctx, id, userID, mode)
	if err != nil {
		return nil, libraryProblem(err)
	}
	return acceptedJob(job), nil
}

func (reg *Registry) getLibraryProviders(ctx context.Context, in *LibraryIDInput) (*LibraryProvidersOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	levels, err := svc.LibraryProviders(ctx, id)
	if err != nil {
		return nil, libraryProblem(err)
	}
	// A database upgraded from before per-level chains keeps its legacy
	// unlevelled rows (SyncBuiltinProviderChains copies them to every served
	// level and leaves the originals for old binaries). v1 returns that level
	// under the empty key; v2 content levels are named, so the legacy rows are
	// not exposed here and setLibraryProviders carries them across.
	if _, ok := levels[legacyContentLevel]; ok {
		named := make(map[string][]handlers.ChainLevelEntryView, len(levels)-1)
		for name, entries := range levels {
			if name != legacyContentLevel {
				named[name] = entries
			}
		}
		levels = named
	}
	return &LibraryProvidersOutput{Body: LibraryProviders{Levels: providerChainLevelsOf(levels)}}, nil
}

// legacyContentLevel is the content_level of provider chain rows written
// before chains were kept per level: the empty string.
const legacyContentLevel = ""

func (reg *Registry) setLibraryProviders(ctx context.Context, in *LibraryProvidersSetInput) (*struct{}, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	levels := make(map[string][]handlers.ProviderChainEntryInput, len(in.Body.Levels))
	for i, level := range in.Body.Levels {
		if _, dup := levels[level.ContentLevel]; dup {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: "body.levels[" + strconv.Itoa(i) + "].content_level", Code: codeInvalid, Detail: "content level listed more than once"})
		}
		entries := make([]handlers.ProviderChainEntryInput, 0, len(level.Entries))
		for j, e := range level.Entries {
			installation, err := intOfID(e.PluginInstallationID)
			if err != nil || installation <= 0 {
				return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
					WithErrors(ProblemError{Location: "body.levels[" + strconv.Itoa(i) + "].entries[" + strconv.Itoa(j) + "].plugin_installation_id", Code: codeInvalid, Detail: "expected a plugin installation identifier"})
			}
			entries = append(entries, handlers.ProviderChainEntryInput{PluginInstallationID: installation, CapabilityID: e.CapabilityID, Priority: e.Priority, Enabled: e.Enabled})
		}
		levels[level.ContentLevel] = entries
	}
	// The seam replaces every row of the library, and getLibraryProviders
	// hides the legacy level, so carry the legacy rows across unchanged:
	// v1 clients round-trip them, and dropping them would change which
	// chain GetChain's legacy fallback resolves for a level left empty.
	current, err := svc.LibraryProviders(ctx, id)
	if err != nil {
		return nil, libraryProblem(err)
	}
	if legacy := current[legacyContentLevel]; len(legacy) > 0 {
		entries := make([]handlers.ProviderChainEntryInput, 0, len(legacy))
		for _, e := range legacy {
			entries = append(entries, handlers.ProviderChainEntryInput{PluginInstallationID: e.PluginInstallationID, CapabilityID: e.CapabilityID, Priority: e.Priority, Enabled: e.Enabled})
		}
		levels[legacyContentLevel] = entries
	}
	if err := svc.SetLibraryProviders(ctx, id, levels); err != nil {
		return nil, libraryProblem(err)
	}
	return nil, nil
}

func (reg *Registry) uploadLibraryPoster(ctx context.Context, in *LibraryPosterUploadInput) (*LibraryOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	form := in.RawBody.Data()
	if form == nil || !form.Poster.IsSet {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "body.poster", Code: codeRequired, Detail: "a poster file is required"})
	}
	if form.Poster.Size > maxPosterBytes {
		return nil, NewProblem(TypePayloadTooLarge, "The poster exceeds the 10 MiB limit.")
	}
	data, err := io.ReadAll(io.LimitReader(form.Poster, maxPosterBytes+1))
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	if len(data) > maxPosterBytes {
		return nil, NewProblem(TypePayloadTooLarge, "The poster exceeds the 10 MiB limit.")
	}
	view, err := svc.UploadLibraryPoster(ctx, id, form.Poster.ContentType, data)
	if err != nil {
		return nil, libraryProblem(err)
	}
	return &LibraryOutput{Body: libraryOf(view)}, nil
}

func (reg *Registry) deleteLibraryPoster(ctx context.Context, in *LibraryIDInput) (*struct{}, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	if err := svc.DeleteLibraryPoster(ctx, id); err != nil {
		return nil, libraryProblem(err)
	}
	return nil, nil
}

// libraryAdmin is the wired service, or the fail-closed problem.
func (reg *Registry) libraryAdmin() (LibraryAdminService, *Problem) {
	if reg.deps.LibraryAdmin == nil {
		return nil, unavailable("library management")
	}
	return reg.deps.LibraryAdmin, nil
}

// libraryProblem maps the v1 decision onto problem types: a rejected member
// is a validation failure naming it, a bare 400 a validation failure on the
// body, and the rest follow the status.
func libraryProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // the seams return the value directly
	if !ok {
		return serviceProblem(err)
	}
	switch {
	case apiErr.Field != "":
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody + "." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message})
	case apiErr.Status == http.StatusBadRequest:
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody, Code: codeInvalid, Detail: apiErr.Message})
	case apiErr.Status == http.StatusServiceUnavailable:
		// v1 answers 503 when the store behind a command is not configured
		// (delete jobs, provider chains); that is the fail-closed problem,
		// not an internal error.
		return NewProblem(TypeDependencyUnavailable, apiErr.Message).WithRetryAfter(30)
	}
	return serviceProblem(err)
}

// libraryID recovers the integer key an opaque library id carries. An id
// that is not one names no library.
func libraryID(id ID) (int, *Problem) {
	n, err := intOfID(id)
	if err != nil || n <= 0 {
		return 0, NewProblem(TypeNotFound, "No library has that identifier.")
	}
	return n, nil
}

// actingUserID is the account behind the request; the acting-admin gate
// guarantees claims are present.
func actingUserID(ctx context.Context) (int, *Problem) {
	claims := claimsFrom(ctx)
	if claims == nil {
		return 0, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	return claims.UserID, nil
}

func (reg *Registry) listLibraries(ctx context.Context, _ *struct{}) (*LibraryCollectionOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	views, err := svc.ListLibraries(ctx)
	if err != nil {
		return nil, libraryProblem(err)
	}
	items := make([]Library, 0, len(views))
	for i := range views {
		items = append(items, libraryOf(views[i]))
	}
	return &LibraryCollectionOutput{Body: LibraryCollection{Collection: NewCollection(items)}}, nil
}

func (reg *Registry) createLibrary(ctx context.Context, in *LibraryCreateInput) (*LibraryCreatedOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	view, err := svc.CreateLibrary(ctx, handlers.LibraryCreateRequest{
		Paths:                    in.Body.Paths,
		Type:                     in.Body.Type,
		Name:                     in.Body.Name,
		MetadataLanguage:         in.Body.MetadataLanguage,
		ChapterThumbnailsEnabled: in.Body.ChapterThumbnailsEnabled,
		IntroDetectionEnabled:    in.Body.IntroDetectionEnabled,
		TrailerKinds:             in.Body.TrailerKinds,
	})
	if err != nil {
		return nil, libraryProblem(err)
	}
	lib := libraryOf(view)
	return &LibraryCreatedOutput{Location: Prefix + "/libraries/" + string(lib.ID), Body: lib}, nil
}

func (reg *Registry) updateLibrary(ctx context.Context, in *LibraryUpdateInput) (*LibraryOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	view, err := svc.UpdateLibrary(ctx, id, userID, handlers.LibraryUpdateRequest{
		Paths:                    in.Body.Paths,
		Type:                     in.Body.Type,
		Name:                     in.Body.Name,
		Enabled:                  in.Body.Enabled,
		MetadataLanguage:         in.Body.MetadataLanguage,
		AutoTranslateMetadata:    in.Body.AutoTranslateMetadata,
		ChapterThumbnailsEnabled: in.Body.ChapterThumbnailsEnabled,
		IntroDetectionEnabled:    in.Body.IntroDetectionEnabled,
		TrailerKinds:             in.Body.TrailerKinds,
	})
	if err != nil {
		return nil, libraryProblem(err)
	}
	return &LibraryOutput{Body: libraryOf(view)}, nil
}

// deleteLibrary answers 202 with the queued job, as v1 does. A deletion
// already queued or running is a conflict problem; the job-monitor contract
// that would carry the active job is a later foundation rule.
func (reg *Registry) deleteLibrary(ctx context.Context, in *LibraryIDInput) (*AdminJobAcceptedOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	job, err := svc.DeleteLibrary(ctx, id, userID)
	if err != nil {
		return nil, libraryProblem(err)
	}
	return acceptedJob(job), nil
}

func (reg *Registry) checkLibraryMount(ctx context.Context, in *LibraryIDInput) (*LibraryMountCheckOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	view, err := svc.CheckLibraryMount(ctx, id)
	if err != nil {
		return nil, libraryProblem(err)
	}
	roots := make([]LibraryMountCheckRoot, 0, len(view.Roots))
	for _, r := range view.Roots {
		roots = append(roots, LibraryMountCheckRoot{Path: r.Path, Reachable: r.Reachable, ErrorCode: r.ErrorCode, ErrorMessage: r.ErrorMessage, SuspectEmpty: r.SuspectEmpty})
	}
	return &LibraryMountCheckOutput{Body: LibraryMountCheck{
		Status:      view.Status,
		LibraryID:   IDFromInt(int64(view.LibraryID)),
		LibraryName: view.LibraryName,
		Healthy:     view.Healthy,
		CheckedAt:   NewInstant(view.CheckedAt),
		Summary:     view.Summary,
		Roots:       roots,
	}}, nil
}

func (reg *Registry) listMetadataMatchQueues(ctx context.Context, _ *struct{}) (*MetadataMatchQueueStatusCollectionOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	views, err := svc.ListMetadataMatchQueues(ctx)
	if err != nil {
		return nil, libraryProblem(err)
	}
	items := make([]MetadataMatchQueueStatus, 0, len(views))
	for _, v := range views {
		items = append(items, MetadataMatchQueueStatus{
			LibraryID:    IDFromInt(int64(v.LibraryID)),
			MovieCount:   v.MovieCount,
			SeriesCount:  v.SeriesCount,
			RawFileCount: v.RawFileCount,
			TotalCount:   v.TotalCount,
			PendingCount: v.PendingCount,
			ParkedCount:  v.ParkedCount,
		})
	}
	return &MetadataMatchQueueStatusCollectionOutput{Body: MetadataMatchQueueStatusCollection{Collection: NewCollection(items)}}, nil
}

func (reg *Registry) getLibraryProviderDefaults(ctx context.Context, in *LibraryProviderDefaultsInput) (*LibraryProviderDefaultsOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	byLevel, err := svc.LibraryProviderDefaults(ctx, in.LibraryType)
	if err != nil {
		return nil, libraryProblem(err)
	}
	return &LibraryProviderDefaultsOutput{Body: LibraryProviderDefaults{Levels: providerChainLevelsOf(byLevel)}}, nil
}

// providerChainLevelsOf orders a chain-by-level map by level name, the one
// deterministic order the wire carries.
func providerChainLevelsOf(byLevel map[string][]handlers.ChainLevelEntryView) []ProviderChainLevel {
	names := make([]string, 0, len(byLevel))
	for name := range byLevel {
		names = append(names, name)
	}
	sort.Strings(names)
	levels := make([]ProviderChainLevel, 0, len(names))
	for _, name := range names {
		entries := make([]ProviderChainEntry, 0, len(byLevel[name]))
		for _, e := range byLevel[name] {
			entries = append(entries, ProviderChainEntry{
				PluginInstallationID: IDFromInt(int64(e.PluginInstallationID)),
				CapabilityID:         e.CapabilityID,
				ProviderSlug:         e.ProviderSlug,
				Priority:             e.Priority,
				Enabled:              e.Enabled,
			})
		}
		levels = append(levels, ProviderChainLevel{ContentLevel: name, Entries: entries})
	}
	return levels
}

func (reg *Registry) reorderLibraries(ctx context.Context, in *LibraryReorderInput) (*struct{}, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	entries := make([]catalogpkg.FolderReorderEntry, 0, len(in.Body.Entries))
	for i, e := range in.Body.Entries {
		id, err := intOfID(e.ID)
		if err != nil || id <= 0 {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationBody + ".entries[" + strconv.Itoa(i) + "].id", Code: codeInvalid, Detail: detailNotLibraryID})
		}
		entries = append(entries, catalogpkg.FolderReorderEntry{ID: id, Position: e.Position})
	}
	if err := svc.ReorderLibraries(ctx, entries); err != nil {
		return nil, libraryProblem(err)
	}
	return nil, nil
}

func (reg *Registry) listLibraryRoots(ctx context.Context, cursors *Cursors, in *LibraryRootListInput) (*LibraryRootCollectionOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	libID, p := libraryID(in.LibraryID)
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	state := strings.TrimSpace(in.State)
	search := strings.TrimSpace(in.Query)
	scope := CursorScope{
		OperationID: opListLibraryRoots,
		Security:    strconv.Itoa(userID),
		Filter:      url.Values{"library_id": {strconv.Itoa(libID)}, "state": {state}, "q": {search}}.Encode(),
		Sort:        "store",
		Tiebreaker:  "store",
	}
	offset, p := decodeOffset(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	views, total, err := svc.ListLibraryRoots(ctx, libID, state, search, in.Limit+1, offset)
	if err != nil {
		return nil, libraryProblem(err)
	}
	views, next, p := offsetPage(cursors, scope, len(views), in.Limit, offset, views)
	if p != nil {
		return nil, p
	}
	items := make([]LibraryRoot, 0, len(views))
	for i := range views {
		items = append(items, libraryRootOf(views[i]))
	}
	return &LibraryRootCollectionOutput{Body: LibraryRootCollection{Collection: Paginated(items, next), Total: total}}, nil
}

func (reg *Registry) setRootOverride(ctx context.Context, in *RootOverrideSetInput) (*struct{}, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	libID, err := intOfID(in.Body.LibraryID)
	if err != nil || libID <= 0 {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBodyLibraryID, Code: codeInvalid, Detail: detailNotLibraryID})
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	err = svc.SetRootOverride(ctx, userID, handlers.RootOverrideUpsertRequest{
		LibraryID:    libID,
		RootPath:     in.Body.RootPath,
		ForcedType:   in.Body.ForcedType,
		ForcedTitle:  in.Body.ForcedTitle,
		ForcedYear:   in.Body.ForcedYear,
		ForcedTmdbID: in.Body.ForcedTmdbID,
		ForcedImdbID: in.Body.ForcedImdbID,
		ForcedTvdbID: in.Body.ForcedTvdbID,
		Note:         in.Body.Note,
	})
	if err != nil {
		return nil, libraryProblem(err)
	}
	return nil, nil
}

func (reg *Registry) deleteRootOverride(ctx context.Context, in *RootOverrideDeleteInput) (*struct{}, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	libID, err := intOfID(in.LibraryID)
	if err != nil || libID <= 0 {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationQueryLibraryID, Code: codeInvalid, Detail: detailNotLibraryID})
	}
	if err := svc.DeleteRootOverride(ctx, handlers.RootOverrideDeleteRequest{LibraryID: libID, RootPath: in.RootPath}); err != nil {
		return nil, libraryProblem(err)
	}
	return nil, nil
}

func (reg *Registry) listSkippedRoots(ctx context.Context, cursors *Cursors, in *SkippedRootListInput) (*SkippedRootCollectionOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{OperationID: opListSkippedRoots, Security: strconv.Itoa(userID), Filter: strings.TrimSpace(in.Query), Sort: "last_seen_at", Tiebreaker: "library_id,root_path"}
	offset, p := decodeOffset(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	views, err := svc.ListSkippedRoots(ctx, strings.TrimSpace(in.Query), in.Limit+1, offset)
	if err != nil {
		return nil, libraryProblem(err)
	}
	views, next, p := offsetPage(cursors, scope, len(views), in.Limit, offset, views)
	if p != nil {
		return nil, p
	}
	items := make([]SkippedRoot, 0, len(views))
	for _, v := range views {
		items = append(items, SkippedRoot{
			LibraryID:      IDFromInt(int64(v.LibraryID)),
			LibraryName:    v.LibraryName,
			RootPath:       v.RootPath,
			Reason:         v.Reason,
			SampleFilePath: v.SampleFilePath,
			FileCount:      v.FileCount,
			FirstSeenAt:    NewInstant(v.FirstSeenAt),
			LastSeenAt:     NewInstant(v.LastSeenAt),
		})
	}
	return &SkippedRootCollectionOutput{Body: SkippedRootCollection{Collection: Paginated(items, next)}}, nil
}

func (reg *Registry) listStaleIDs(ctx context.Context, cursors *Cursors, in *StaleMediaIDListInput) (*StaleMediaIDCollectionOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{OperationID: opListStaleIDs, Security: strconv.Itoa(userID), Filter: strings.TrimSpace(in.Query), Sort: "last_seen_at", Tiebreaker: tiebreakerContentID}
	offset, p := decodeOffset(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	views, err := svc.ListStaleIDs(ctx, strings.TrimSpace(in.Query), in.Limit+1, offset)
	if err != nil {
		return nil, libraryProblem(err)
	}
	views, next, p := offsetPage(cursors, scope, len(views), in.Limit, offset, views)
	if p != nil {
		return nil, p
	}
	items := make([]StaleMediaID, 0, len(views))
	for _, v := range views {
		items = append(items, StaleMediaID{
			ContentID:   v.ContentID,
			LibraryID:   IDFromInt(int64(v.LibraryID)),
			LibraryName: v.LibraryName,
			Title:       v.Title,
			Year:        v.Year,
			ContentType: v.ContentType,
			Provider:    v.Provider,
			ProviderID:  v.ProviderID,
			FirstSeenAt: NewInstant(v.FirstSeen),
			LastSeenAt:  NewInstant(v.LastSeen),
		})
	}
	return &StaleMediaIDCollectionOutput{Body: StaleMediaIDCollection{Collection: Paginated(items, next)}}, nil
}

func (reg *Registry) rematchStaleID(ctx context.Context, in *StaleIDRematchInput) (*struct{}, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	if err := svc.RematchStaleID(ctx, in.ContentID); err != nil {
		return nil, libraryProblem(err)
	}
	return nil, nil
}

func (reg *Registry) listUnmatchedItems(ctx context.Context, cursors *Cursors, in *UnmatchedItemListInput) (*UnmatchedItemCollectionOutput, error) {
	svc, p := reg.libraryAdmin()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	search := strings.TrimSpace(in.Query)
	scope := CursorScope{
		OperationID: opListUnmatchedItems,
		Security:    strconv.Itoa(userID),
		Filter:      "q=" + search,
		Sort:        "title",
		Tiebreaker:  tiebreakerContentID,
	}
	offset, p := decodeOffset(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	views, total, err := svc.ListUnmatchedItems(ctx, search, in.Limit+1, offset)
	if err != nil {
		return nil, libraryProblem(err)
	}
	views, next, p := offsetPage(cursors, scope, len(views), in.Limit, offset, views)
	if p != nil {
		return nil, p
	}
	items := make([]UnmatchedItem, 0, len(views))
	for _, v := range views {
		items = append(items, UnmatchedItem{
			ContentID:   v.ContentID,
			Title:       v.Title,
			Year:        v.Year,
			ContentType: v.ContentType,
			LibraryID:   IDFromInt(int64(v.LibraryID)),
			LibraryName: v.LibraryName,
			Status:      v.Status,
		})
	}
	return &UnmatchedItemCollectionOutput{Body: UnmatchedItemCollection{Collection: Paginated(items, next), Total: total}}, nil
}

// decodeOffset reads the offset an administrative listing's cursor carries;
// no cursor starts at the first row.
func decodeOffset(cursors *Cursors, scope CursorScope, cursor string) (int, *Problem) {
	if cursor == "" {
		return 0, nil
	}
	var pos offsetPosition
	if p := cursors.Decode(scope, cursor, &pos); p != nil {
		return 0, p
	}
	return pos.Offset, nil
}

// offsetPage trims a limit+1 probe to the page and mints the next cursor
// when a row followed.
func offsetPage[T any](cursors *Cursors, scope CursorScope, fetched, limit, offset int, rows []T) ([]T, string, *Problem) {
	if fetched <= limit {
		return rows, "", nil
	}
	next, err := cursors.Encode(scope, offsetPosition{Offset: offset + limit})
	if err != nil {
		return nil, "", NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	return rows[:limit], next, nil
}

func libraryOf(v handlers.LibraryView) Library {
	return Library{
		ID:                         IDFromInt(int64(v.ID)),
		Paths:                      NonNil(v.Paths),
		Type:                       v.Type,
		Name:                       v.Name,
		Enabled:                    v.Enabled,
		MetadataLanguage:           v.MetadataLanguage,
		AutoTranslateMetadata:      v.AutoTranslateMetadata,
		ChapterThumbnailsEnabled:   v.ChapterThumbnailsEnabled,
		ChapterThumbnailsSupported: v.ChapterThumbnailsSupported,
		IntroDetectionEnabled:      v.IntroDetectionEnabled,
		TrailerKinds:               NonNil(v.TrailerKinds),
		SortOrder:                  v.SortOrder,
		PosterURL:                  v.PosterURL,
		LastScannedAt:              instantPtr(v.LastScannedAt),
		ScanWarningCode:            v.ScanWarningCode,
		ScanWarningMessage:         v.ScanWarningMessage,
		ScanWarningAt:              instantPtr(v.ScanWarningAt),
	}
}

func libraryRootOf(v handlers.LibraryRootView) LibraryRoot {
	out := LibraryRoot{
		LibraryID:         IDFromInt(int64(v.LibraryID)),
		LibraryName:       v.LibraryName,
		RootPath:          v.RootPath,
		State:             v.State,
		InferredType:      v.InferredType,
		TypeConfidence:    v.TypeConfidence,
		Title:             v.Title,
		Year:              v.Year,
		TmdbID:            v.TmdbID,
		ImdbID:            v.ImdbID,
		TvdbID:            v.TvdbID,
		ObservedFileCount: v.ObservedFiles,
		SampleFilePath:    v.SampleFilePath,
		Evidence:          v.Evidence,
		OverrideSource:    v.OverrideSource,
		FirstSeenAt:       NewInstant(v.FirstSeenAt),
		LastSeenAt:        NewInstant(v.LastSeenAt),
		ContentID:         v.ContentID,
	}
	if v.ActiveOverride != nil {
		o := v.ActiveOverride
		out.ActiveOverride = &RootOverride{
			ForcedType: o.ForcedType, ForcedTitle: o.ForcedTitle, ForcedYear: o.ForcedYear,
			ForcedTmdbID: o.ForcedTmdbID, ForcedImdbID: o.ForcedImdbID, ForcedTvdbID: o.ForcedTvdbID, Note: o.Note,
		}
	}
	return out
}
