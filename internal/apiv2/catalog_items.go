package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Section catalog-items: the profile-scoped catalog browse, facet documents,
// and item reads. Cards are CatalogItem (catalog_types.go); the detail
// document composes the card with the members only a detail page shows.

// --- inputs ---

// CatalogBrowseInput is the listCatalogItems query: the source to page and
// the overlay filters v1 accepts on it.
type CatalogBrowseInput struct {
	Source        string   `query:"source" enum:"query,section,library_collection,user_collection,favorites,watchlist,history,person" doc:"What to page; default query (the whole catalog)" example:"query"`
	Scope         string   `query:"scope" enum:"home,library" doc:"For source=section: which page the section is on; default library"`
	SectionID     string   `query:"section_id" doc:"For source=section"`
	LibraryID     ID       `query:"library_id" doc:"Restrict to one library; required for library sections" example:"1"`
	CollectionID  string   `query:"collection_id" doc:"For source=library_collection or user_collection"`
	PersonID      ID       `query:"person_id" doc:"For source=person"`
	Q             string   `query:"q" doc:"Search text" example:"heat"`
	NamePrefix    string   `query:"name_prefix" doc:"Alphabetical jump: only titles starting here"`
	Match         string   `query:"match" enum:"all,any" doc:"How the filters combine; default all"`
	Type          string   `query:"type" doc:"Media scope: movie, series, episode, audiobook, ebook, podcast, video, …" example:"movie"`
	Genre         string   `query:"genre" example:"Crime"`
	Status        string   `query:"status" doc:"Metadata match state" example:"matched"`
	YearMin       int      `query:"year_min" minimum:"0" example:"1990"`
	YearMax       int      `query:"year_max" minimum:"0" example:"1999"`
	ContentRating []string `query:"content_rating,explode" doc:"Repeat the key for several ratings" example:"R"`
	Sort          string   `query:"sort" doc:"sort=field or -field; one term. Fields: title, year, release_date, added_at, rating, runtime, random, … (see the filters document); absent applies the source's saved or default order" example:"-release_date"`
	Group         string   `query:"group" enum:"work" doc:"group=work collapses editions of one book into one card"`
	SkipTotal     bool     `query:"skip_total" doc:"true skips the exact count; the total is then an estimate"`
	ImageSize     string   `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign"`
	LimitParam
	Cursor      string `query:"cursor" doc:"Opaque cursor from page.next_cursor"`
	Groups      string `query:"groups" maxLength:"32768" doc:"JSON array of structured rule groups; use POST catalog/query for larger queries"`
	QueryLimit  int    `query:"query_limit" minimum:"0" doc:"Maximum items in the query result; zero means uncapped"`
	Seek        int    `query:"seek" minimum:"0" maximum:"10000000" doc:"Explicit zero-based window jump; the server locates a boundary without returning intermediate cards"`
	operationID string
}

// CatalogFiltersInput is the getCatalogFilters query: the scope whose
// facets to list, in the listCatalogItems grammar.
type CatalogFiltersInput struct {
	Source        string `query:"source" enum:"query,section,library_collection,user_collection,favorites,watchlist,history,person" doc:"What scope to list facets for; default query"`
	Scope         string `query:"scope" enum:"home,library"`
	SectionID     string `query:"section_id"`
	LibraryID     ID     `query:"library_id" example:"1"`
	CollectionID  string `query:"collection_id"`
	PersonID      ID     `query:"person_id"`
	Type          string `query:"type" example:"movie"`
	SkipTechnical bool   `query:"skip_technical" doc:"true omits the file-derived facets (resolutions, audio and subtitle languages)"`
}

// CatalogFacetSearchInput is the searchCatalogFacet query.
type CatalogFacetSearchInput struct {
	CatalogFiltersInput
	Facet string `query:"facet" required:"true" enum:"genre,studio,network,country,original_language,content_rating,author,narrator,series" doc:"The facet to search" example:"author"`
	Q     string `query:"q" doc:"Case-insensitive prefix" example:"ste"`
	Limit int    `query:"limit" minimum:"1" maximum:"100" default:"20" doc:"Most matches to return; default 20, maximum 100"`
}

// AudiobookGroupsInput is the listAudiobookGroups query.
type AudiobookGroupsInput struct {
	LibraryID ID     `query:"library_id" required:"true" doc:"The audiobook library" example:"3"`
	GroupBy   string `query:"group_by" required:"true" enum:"author,narrator,series" example:"author"`
	Sort      string `query:"sort" enum:"name,count,duration" doc:"Default name"`
	Q         string `query:"q" doc:"Case-insensitive prefix on the group name"`
	SkipTotal bool   `query:"skip_total" doc:"true skips the exact count; the total is then an estimate"`
	ImageSize string `query:"image_size" enum:"small,medium,large,original"`
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor"`
}

// CatalogQueryInput is the queryCatalogItems request: the JSON form of a
// filtered, sorted page.
type CatalogQueryInput struct {
	ImageSize string `query:"image_size" enum:"small,medium,large,original"`
	Body      CatalogQuery
}

// CatalogQuery is the queryCatalogItems body.
type CatalogQuery struct {
	Match        string              `json:"match,omitempty" enum:"all,any" doc:"How the groups combine; default all"`
	Groups       []CatalogQueryGroup `json:"groups,omitempty" doc:"Rule groups; empty matches everything"`
	Sort         string              `json:"sort,omitempty" doc:"Sort field; default added date, newest first" example:"title"`
	Order        string              `json:"order,omitempty" enum:"asc,desc"`
	LibraryID    ID                  `json:"library_id,omitempty" doc:"Restrict to one library" example:"1"`
	Limit        int                 `json:"limit,omitempty" minimum:"1" maximum:"100" doc:"Page size; default 50, maximum 100" example:"50"`
	Source       string              `json:"source,omitempty" enum:"query,section,library_collection,user_collection,favorites,watchlist,history,person"`
	Scope        string              `json:"scope,omitempty" enum:"home,library"`
	SectionID    string              `json:"section_id,omitempty"`
	CollectionID string              `json:"collection_id,omitempty"`
	PersonID     ID                  `json:"person_id,omitempty"`
	Q            string              `json:"q,omitempty"`
	NamePrefix   string              `json:"name_prefix,omitempty"`
	Type         string              `json:"type,omitempty"`
	Group        string              `json:"group,omitempty" enum:"work"`
	SkipTotal    bool                `json:"skip_total,omitzero"`
	QueryLimit   int                 `json:"query_limit,omitzero" minimum:"0"`
	Cursor       string              `json:"cursor,omitempty"`
	Seek         int                 `json:"seek,omitempty" minimum:"0" maximum:"10000000"`
}

// CatalogQueryGroup is one rule group of a query.
type CatalogQueryGroup struct {
	Match string             `json:"match" enum:"all,any"`
	Rules []CatalogQueryRule `json:"rules" doc:"Empty, never null"`
}

// CatalogQueryRule is one rule of a query group.
type CatalogQueryRule struct {
	Field string `json:"field" example:"genre"`
	Op    string `json:"op" example:"contains"`
	Value any    `json:"value" doc:"Scalar or array, as the operator requires"`
}

// CatalogItemInput names one item.
type CatalogItemInput struct {
	ID        string `path:"id" doc:"Content id" example:"movie:heat-1995"`
	ImageSize string `query:"image_size" enum:"small,medium,large,original"`
	LibraryID ID     `query:"library_id" doc:"The library the item is being viewed in; picks its presentation when the item is in several"`
	FileID    ID     `query:"file_id" doc:"The version the viewer selected; affects the effective playback answer"`
}

// CatalogSeriesInput names one series.
type CatalogSeriesInput struct {
	ID        string `path:"id" doc:"Series content id" example:"series:severance"`
	ImageSize string `query:"image_size" enum:"small,medium,large,original"`
	LibraryID ID     `query:"library_id" doc:"The library the series is being viewed in"`
}

// CatalogSeasonsInput selects artwork for a season list.
type CatalogSeasonsInput struct {
	CatalogSeriesInput
	IncludeArtwork bool `query:"include_artwork" default:"true" doc:"Include poster URLs and thumbhashes; false skips poster preparation"`
}

// CatalogSeasonInput names one season of a series by number.
type CatalogSeasonInput struct {
	CatalogSeriesInput
	Number int `path:"num" minimum:"0" doc:"Season number; 0 is specials" example:"1"`
}

// --- outputs ---

// CatalogBrowseCollection is one page of the catalog browse.
type CatalogBrowseCollection struct {
	WindowCursor string `json:"window_cursor" doc:"Opaque seed for subsequent explicit window jumps; carries query scope and the initial insertion fence"`
	Collection[CatalogItem]
	Total             int                       `json:"total" doc:"Items in the whole result; an estimate unless total_exact" example:"1240"`
	TotalExact        bool                      `json:"total_exact" example:"true"`
	SearchDiagnostics *CatalogSearchDiagnostics `json:"search_diagnostics,omitempty" doc:"Present when a relevance-sorted search ran through a search provider"`
	EffectiveSort     *CatalogEffectiveSort     `json:"effective_sort,omitempty" doc:"The saved or default order a collection or personal list resolved to"`
}

// CatalogSearchDiagnostics reports how a search was answered.
type CatalogSearchDiagnostics struct {
	ResultWindowLimit   int      `json:"result_window_limit,omitzero" doc:"Candidate limit of the retained ranking window; not an exact global match count"`
	SessionExpiresAt    *Instant `json:"session_expires_at,omitempty" doc:"Fixed expiry of the retained search ranking"`
	Provider            string   `json:"provider" example:"postgres"`
	Mode                string   `json:"mode" doc:"keyword, semantic, or hybrid after any fallback" example:"keyword"`
	SemanticUsed        bool     `json:"semantic_used" example:"false"`
	FallbackReason      string   `json:"fallback_reason,omitempty"`
	IndexPendingUpdates int      `json:"index_pending_updates,omitempty"`
}

// CatalogEffectiveSort is a resolved sort.
type CatalogEffectiveSort struct {
	Field string `json:"field" example:"title"`
	Order string `json:"order" example:"asc"`
}

// CatalogBrowseOutput is the listCatalogItems and queryCatalogItems response.
type CatalogBrowseOutput struct {
	Body CatalogBrowseCollection
}

// catalogBrowsePosition retains source continuation, resolved sort and an
// insertion fence. Offset is the visible window position for explicit jumps.
type catalogBrowsePosition struct {
	After    *catalogpkg.QueryCursor `json:"a,omitempty"`
	Sort     *catalogpkg.QuerySort   `json:"sort,omitempty"`
	Offset   int                     `json:"o"`
	Snapshot string                  `json:"s,omitempty"`
}

// CatalogFilters is the facet document of a scope.
type CatalogFilters struct {
	Genres            []string                 `json:"genres" doc:"Empty, never null"`
	Studios           []string                 `json:"studios"`
	Networks          []string                 `json:"networks"`
	Countries         []string                 `json:"countries"`
	OriginalLanguages []string                 `json:"original_languages"`
	ContentRatings    []string                 `json:"content_ratings"`
	Authors           []string                 `json:"authors" doc:"First 1000 alphabetically; searchCatalogFacet pages the rest"`
	Narrators         []string                 `json:"narrators"`
	Series            []string                 `json:"series"`
	Technical         *CatalogTechnicalFilters `json:"technical,omitempty" doc:"File-derived facets; absent when skip_technical=true"`
}

// CatalogTechnicalFilters are the facets derived from the files themselves.
type CatalogTechnicalFilters struct {
	Resolutions       []string `json:"resolutions" doc:"Empty, never null"`
	AudioLanguages    []string `json:"audio_languages"`
	SubtitleLanguages []string `json:"subtitle_languages"`
}

// CatalogFiltersOutput is the getCatalogFilters response.
type CatalogFiltersOutput struct {
	Body CatalogFilters
}

// CatalogFacetMatches is a facet typeahead answer.
type CatalogFacetMatches struct {
	Matches []string `json:"matches" doc:"Empty, never null"`
	HasMore bool     `json:"has_more" doc:"Whether more values matched than limit"`
}

// CatalogFacetMatchesOutput is the searchCatalogFacet response.
type CatalogFacetMatchesOutput struct {
	Body CatalogFacetMatches
}

// AudiobookGroup is one author, narrator, or series with aggregate stats.
type AudiobookGroup struct {
	Name                 string   `json:"name" example:"Frank Herbert"`
	ItemCount            int      `json:"item_count" example:"6"`
	TotalDurationSeconds int64    `json:"total_duration_seconds" example:"302400"`
	InProgressCount      int      `json:"in_progress_count" doc:"The viewer's books in progress"`
	FinishedCount        int      `json:"finished_count"`
	PosterURLs           []string `json:"poster_urls" doc:"Up to four presigned covers for a stack; empty, never null"`
}

// AudiobookGroupCollection is one page of groups.
type AudiobookGroupCollection struct {
	Collection[AudiobookGroup]
	Total      int  `json:"total" example:"40"`
	TotalExact bool `json:"total_exact" example:"true"`
}

// AudiobookGroupCollectionOutput is the listAudiobookGroups response.
type AudiobookGroupCollectionOutput struct {
	Body AudiobookGroupCollection
}

// WatchRollup is the viewer's progress over an item or a group of episodes.
type WatchRollup struct {
	PositionSeconds float64 `json:"position_seconds,omitempty" doc:"Resume position of the leaf or of the next episode"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	IsInProgress    bool    `json:"is_in_progress,omitempty"`
	WatchedCount    int     `json:"watched_count"`
	UnplayedCount   int     `json:"unplayed_count"`
	InProgressCount int     `json:"in_progress_count"`
	Played          bool    `json:"played"`
	LastFileID      ID      `json:"last_file_id,omitempty" doc:"The version last played"`
	LastResolution  *string `json:"last_resolution,omitempty"`
	LastHDR         *bool   `json:"last_hdr,omitempty"`
	LastCodecVideo  *string `json:"last_codec_video,omitempty"`
	LastEditionKey  *string `json:"last_edition_key,omitempty"`
}

// FileVersion is one playable file of an item.
type FileVersion struct {
	FileID                   ID                                `json:"file_id" example:"120"`
	FileName                 string                            `json:"file_name,omitempty"`
	FilePath                 string                            `json:"file_path,omitempty" doc:"Absent for viewers without file-path visibility"`
	Resolution               string                            `json:"resolution" example:"2160p"`
	CodecVideo               string                            `json:"codec_video" example:"hevc"`
	CodecAudio               string                            `json:"codec_audio" example:"truehd"`
	HDR                      bool                              `json:"hdr"`
	Container                string                            `json:"container" example:"mkv"`
	FileSize                 int64                             `json:"file_size" doc:"Bytes"`
	Duration                 int                               `json:"duration" doc:"Seconds"`
	Bitrate                  int                               `json:"bitrate" doc:"Kilobits per second"`
	AddedAt                  Instant                           `json:"added_at"`
	EditionRaw               string                            `json:"edition_raw,omitempty"`
	EditionKey               string                            `json:"edition_key,omitempty"`
	PresentationKind         string                            `json:"presentation_kind,omitempty"`
	PresentationGroupKey     string                            `json:"presentation_group_key,omitempty"`
	PresentationPartIndex    int                               `json:"presentation_part_index,omitempty"`
	PresentationPartTotal    int                               `json:"presentation_part_total,omitempty"`
	MultiEpisodeStart        int                               `json:"multi_episode_start,omitempty"`
	MultiEpisodeEnd          int                               `json:"multi_episode_end,omitempty"`
	EffectiveAudioTrackIndex *int                              `json:"effective_audio_track_index,omitempty"`
	EffectiveAudioLanguage   string                            `json:"effective_audio_language,omitempty"`
	VideoTracks              []models.VideoTrack               `json:"video_tracks,omitempty"`
	AudioTracks              []models.AudioTrack               `json:"audio_tracks,omitempty"`
	SubtitleTracks           []catalogpkg.VersionSubtitleTrack `json:"subtitle_tracks,omitempty"`
	Chapters                 []catalogpkg.VersionChapter       `json:"chapters,omitempty"`
	Intro                    *catalogpkg.Marker                `json:"intro,omitempty"`
	Credits                  *catalogpkg.Marker                `json:"credits,omitempty"`
	Recap                    *catalogpkg.Marker                `json:"recap,omitempty"`
	Preview                  *catalogpkg.Marker                `json:"preview,omitempty"`
}

// FileVersionCollection is an item's versions.
type FileVersionCollection struct {
	Collection[FileVersion]
}

// FileVersionCollectionOutput is the listCatalogItemVersions response.
type FileVersionCollectionOutput struct {
	Body FileVersionCollection
}

// PlaybackVariant groups the versions that play as one presentation.
type PlaybackVariant struct {
	VariantID            string                `json:"variant_id"`
	EditionRaw           string                `json:"edition_raw,omitempty"`
	EditionKey           string                `json:"edition_key,omitempty"`
	PresentationKind     string                `json:"presentation_kind,omitempty"`
	PresentationGroupKey string                `json:"presentation_group_key,omitempty"`
	PartCount            int                   `json:"part_count"`
	TotalDuration        int                   `json:"total_duration,omitempty" doc:"Seconds"`
	DefaultFileID        ID                    `json:"default_file_id,omitempty"`
	Parts                []PlaybackVariantPart `json:"parts" doc:"Empty, never null"`
}

// PlaybackVariantPart is one part of a multi-part presentation.
type PlaybackVariantPart struct {
	PartIndex     int           `json:"part_index"`
	DefaultFileID ID            `json:"default_file_id,omitempty"`
	TotalDuration int           `json:"total_duration,omitempty"`
	Versions      []FileVersion `json:"versions" doc:"Empty, never null"`
}

// CatalogItemDetail is the detail page of an item: the card plus everything
// the page shows.
type CatalogItemDetail struct {
	Themes *ThemeSongSet `json:"themes,omitempty"`
	CatalogItem
	SortTitle                       string                               `json:"sort_title,omitempty"`
	OriginalTitle                   string                               `json:"original_title,omitempty"`
	Tagline                         string                               `json:"tagline,omitempty"`
	PendingTranslationLanguage      string                               `json:"pending_translation_language,omitempty" doc:"A translation of the overview is queued for this language"`
	ImdbID                          string                               `json:"imdb_id,omitempty"`
	TmdbID                          string                               `json:"tmdb_id,omitempty"`
	TvdbID                          string                               `json:"tvdb_id,omitempty"`
	Cast                            []catalogpkg.CastCredit              `json:"cast" doc:"Empty, never null"`
	Crew                            []catalogpkg.CrewCredit              `json:"crew" doc:"Empty, never null"`
	Countries                       []string                             `json:"countries,omitempty"`
	LockedFields                    []int                                `json:"locked_fields,omitempty" doc:"Metadata fields an editor pinned"`
	FirstAirDate                    *string                              `json:"first_air_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	AirTime                         *string                              `json:"air_time,omitempty"`
	AirTimezone                     *string                              `json:"air_timezone,omitempty"`
	SeasonCount                     *int                                 `json:"season_count,omitempty"`
	EpisodeCount                    *int                                 `json:"episode_count,omitempty"`
	AirDate                         *string                              `json:"air_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	IsSpecials                      bool                                 `json:"is_specials,omitempty"`
	UserData                        *WatchRollup                         `json:"user_data,omitempty"`
	UserRating                      *int                                 `json:"user_rating,omitempty"`
	Versions                        []FileVersion                        `json:"versions" doc:"Empty, never null"`
	PlaybackVariants                []PlaybackVariant                    `json:"playback_variants,omitempty"`
	Videos                          []catalogpkg.ItemVideoInfo           `json:"videos,omitempty" doc:"Trailers and clips"`
	Extras                          []catalogpkg.ItemExtraInfo           `json:"extras,omitempty"`
	FolderPaths                     []string                             `json:"folder_paths,omitempty" doc:"Absent for viewers without file-path visibility"`
	Subtitles                       []catalogpkg.SubtitleInfo            `json:"subtitles" doc:"Empty, never null"`
	Intro                           *catalogpkg.Marker                   `json:"intro,omitempty"`
	Credits                         *catalogpkg.Marker                   `json:"credits,omitempty"`
	Recap                           *catalogpkg.Marker                   `json:"recap,omitempty"`
	Preview                         *catalogpkg.Marker                   `json:"preview,omitempty"`
	EffectiveSubtitleLanguage       *string                              `json:"effective_subtitle_language,omitempty" doc:"The subtitle language the viewer's preferences resolve to for this item"`
	EffectiveSubtitleMode           *string                              `json:"effective_subtitle_mode,omitempty" doc:"The subtitle mode the viewer's preferences resolve to for this item"`
	EffectiveShowForcedSubtitles    *bool                                `json:"effective_show_forced_subtitles,omitempty" doc:"Whether forced subtitles show for this item under the viewer's preferences"`
	EffectiveSubtitleTrackSignature *WatchSubtitleSignature              `json:"effective_subtitle_track_signature,omitempty"`
	EffectiveVersionResolution      *string                              `json:"effective_version_resolution,omitempty"`
	EffectiveVersionHDR             *bool                                `json:"effective_version_hdr,omitempty"`
	EffectiveVersionCodecVideo      *string                              `json:"effective_version_codec_video,omitempty"`
	EffectiveVersionEditionKey      *string                              `json:"effective_version_edition_key,omitempty"`
	Audiobook                       *catalogpkg.AudiobookDetailExtension `json:"audiobook,omitempty"`
	Ebook                           *catalogpkg.EbookDetailExtension     `json:"ebook,omitempty"`
	Manga                           *catalogpkg.MangaDetailExtension     `json:"manga,omitempty"`
}

// CatalogItemDetailOutput is the getCatalogItem response.
type CatalogItemDetailOutput struct {
	Body CatalogItemDetail
}

// MangaChapterFile is one chapter file of a manga series.
type MangaChapterFile struct {
	ContentID    string   `json:"content_id"`
	Title        string   `json:"title"`
	ChapterIndex *float64 `json:"chapter_index,omitempty"`
	Volume       string   `json:"volume,omitempty"`
	FilePath     string   `json:"file_path,omitempty" doc:"Absent for viewers without file-path visibility"`
	FileName     string   `json:"file_name"`
	FileSize     int64    `json:"file_size"`
	Container    string   `json:"container,omitempty"`
}

// MangaFiles is the file listing of a manga series.
type MangaFiles struct {
	Collection[MangaChapterFile]
	FolderPaths []string `json:"folder_paths,omitempty" doc:"Absent for viewers without file-path visibility"`
}

// MangaFilesOutput is the listCatalogItemMangaFiles response.
type MangaFilesOutput struct {
	Body MangaFiles
}

// EpisodeFile is one file of an episode row.
type EpisodeFile struct {
	FileID        ID     `json:"file_id"`
	Resolution    string `json:"resolution,omitempty"`
	CodecVideo    string `json:"codec_video,omitempty"`
	HDR           bool   `json:"hdr"`
	AudioChannels int    `json:"audio_channels,omitempty"`
	Container     string `json:"container,omitempty"`
	FileSize      int64  `json:"file_size"`
}

// Episode is one episode row of a season listing.
type Episode struct {
	ContentID      string              `json:"content_id" example:"episode:severance-s01e01"`
	SeasonNumber   int                 `json:"season_number"`
	EpisodeNumber  int                 `json:"episode_number"`
	Title          string              `json:"title"`
	Overview       string              `json:"overview,omitempty"`
	AirDate        *string             `json:"air_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	Runtime        int                 `json:"runtime" doc:"Minutes"`
	ImdbID         string              `json:"imdb_id,omitempty"`
	TmdbID         string              `json:"tmdb_id,omitempty"`
	TvdbID         string              `json:"tvdb_id,omitempty"`
	StillURL       string              `json:"still_url,omitempty" doc:"Presigned, short-lived"`
	StillThumbhash string              `json:"still_thumbhash,omitempty"`
	UserData       *WatchRollup        `json:"user_data,omitempty"`
	Files          []EpisodeFile       `json:"files,omitempty"`
	OverlaySummary *CatalogItemOverlay `json:"overlay_summary,omitempty"`
}

// EpisodeCollection is the episodes of one season.
type EpisodeCollection struct {
	Collection[Episode]
}

// EpisodeCollectionOutput is the listCatalogItemEpisodes response.
type EpisodeCollectionOutput struct {
	Body EpisodeCollection
}

// Season is one season row.
type Season struct {
	ContentID       string       `json:"content_id" example:"series:severance-S01"`
	PlayContentID   string       `json:"play_content_id,omitempty" doc:"The episode to play next"`
	SeasonNumber    int          `json:"season_number"`
	IsSpecials      bool         `json:"is_specials,omitempty"`
	Title           string       `json:"title" example:"Season 1"`
	Overview        string       `json:"overview,omitempty"`
	AirDate         *string      `json:"air_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	EpisodeCount    int          `json:"episode_count"`
	PosterURL       string       `json:"poster_url,omitempty" doc:"Presigned, short-lived"`
	PosterThumbhash string       `json:"poster_thumbhash,omitempty"`
	UserData        *WatchRollup `json:"user_data,omitempty"`
}

// SeasonCollection is the seasons of a series.
type SeasonCollection struct {
	Collection[Season]
}

// SeasonCollectionOutput is the listSeriesSeasons response.
type SeasonCollectionOutput struct {
	Body SeasonCollection
}

// SeasonOutput is the getSeriesSeason response.
type SeasonOutput struct {
	Body Season
}

// --- registration ---

const (
	opListCatalogItems     = "listCatalogItems"
	opQueryCatalogItems    = "queryCatalogItems"
	locationQueryImageSize = "query.image_size"
	opListAudiobookGroups  = "listAudiobookGroups"
)

func registerCatalogItems(reg *Registry) {
	registerCatalogSearchCapabilities(reg)
	registerThemeSongs(reg)
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog", opListCatalogItems, "catalog",
		"Page the catalog, a section, a collection, a personal list, or a person's credits, filtered and sorted.")),
		func(ctx context.Context, in *CatalogBrowseInput) (*CatalogBrowseOutput, error) {
			return reg.listCatalogItems(ctx, cursors, in)
		})
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/audiobook-groups", opListAudiobookGroups, "catalog",
		"Page the authors, narrators, or series of an audiobook library with aggregate stats and cover stacks.")),
		func(ctx context.Context, in *AudiobookGroupsInput) (*AudiobookGroupCollectionOutput, error) {
			return reg.listAudiobookGroups(ctx, cursors, in)
		})
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/filters", "getCatalogFilters", "catalog",
		"The facet values available in a scope, for filter menus.")), reg.getCatalogFilters)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/filters/search", "searchCatalogFacet", "catalog",
		"Prefix typeahead over one facet of a scope.")), reg.searchCatalogFacet)
	query := humaOp(http.MethodPost, Prefix+"/catalog/query", opQueryCatalogItems, "catalog",
		"Page the catalog by a JSON rule-group query; the body form of the browse.")
	query.DefaultStatus = http.StatusOK
	queryOperation := viewerOperation(query)
	queryOperation.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, queryOperation, func(ctx context.Context, in *CatalogQueryInput) (*CatalogBrowseOutput, error) {
		return reg.queryCatalogItems(ctx, cursors, in)
	})
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/items/{id}", "getCatalogItem", "catalog",
		"The detail page of one item, with the viewer's state.")), reg.getCatalogItem)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/items/{id}/episodes", "listCatalogItemEpisodes", "catalog",
		"The episodes of a season item.")), reg.listCatalogItemEpisodes)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/items/{id}/manga-files", "listCatalogItemMangaFiles", "catalog",
		"The chapter files of a manga series, as file descriptors.")), reg.listCatalogItemMangaFiles)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/items/{id}/versions", "listCatalogItemVersions", "catalog",
		"The playable file versions of an item.")), reg.listCatalogItemVersions)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/series/{id}/seasons", "listSeriesSeasons", "catalog",
		"The seasons of a series with the viewer's rollups.")), reg.listSeriesSeasons)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/series/{id}/seasons/{num}", "getSeriesSeason", "catalog",
		"One season of a series by number.")), reg.getSeriesSeason)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/series/{id}/seasons/{num}/episodes", "listSeasonEpisodes", "catalog",
		"The episodes of one season of a series by number.")), reg.listSeasonEpisodes)
	registerCatalogActions(reg)
}

// --- helpers ---

func (reg *Registry) catalogBrowse() (CatalogBrowseService, *Problem) {
	if reg.deps.CatalogBrowse == nil || reg.deps.CatalogAccess == nil {
		return nil, unavailable("catalog")
	}
	return reg.deps.CatalogBrowse, nil
}

func (reg *Registry) catalogItems() (CatalogItemService, *Problem) {
	if reg.deps.CatalogItems == nil || reg.deps.CatalogAccess == nil {
		return nil, unavailable("catalog")
	}
	return reg.deps.CatalogItems, nil
}

// itemViewer resolves the caller into the seams' viewer: identity from the
// context, access policy from the access seam, artwork size and
// presentation hints from the query. The v2 listener reads no device
// header, so the filter carries no device id.
func (reg *Registry) itemViewer(ctx context.Context, imageSize string, libraryID, fileID ID) (handlers.ItemViewer, *Problem) {
	_, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return handlers.ItemViewer{}, p
	}
	opts := handlers.AccessFilterOptions{}
	if libraryID != "" {
		n, p := libraryID.positive("query.library_id")
		if p != nil {
			return handlers.ItemViewer{}, p
		}
		opts.PresentationLibraryID = &n
		opts.ScopeFilesToLibrary = reg.versionsScopedToLibrary(ctx)
	}
	if fileID != "" {
		n, p := fileID.positive("query.file_id")
		if p != nil {
			return handlers.ItemViewer{}, p
		}
		opts.SelectedFileID = n
	}
	filter, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, opts)
	if err != nil {
		return handlers.ItemViewer{}, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	size, err := imagesize.Parse(imageSize)
	if err != nil {
		size = imagesize.Unset
	}
	filter.ImageSize = size
	return handlers.ItemViewer{Access: filter, ProfileID: profileID}, nil
}

// versionsScopedToLibrary reads catalog.scope_versions_to_library. A read
// failure keeps the default (off) rather than failing the catalog read: the
// setting only narrows a document, it never protects one.
func (reg *Registry) versionsScopedToLibrary(ctx context.Context) bool {
	if reg.deps.CatalogSettings == nil {
		return false
	}
	value, err := reg.deps.CatalogSettings.Get(ctx, config.CatalogScopeVersionsToLibrarySettingKey)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

// positive parses a canonical decimal ID that must name a positive integer.
func (id ID) positive(location string) (int, *Problem) {
	n, err := intOfID(id)
	if err != nil || n <= 0 || strconv.Itoa(n) != string(id) {
		return 0, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: location, Code: codeInvalid, Detail: "expected a positive integer identifier"})
	}
	return n, nil
}

// catalogProblem maps a seam failure: a bad_request from the resolver is a
// 422 at location, a search deadline is a dependency problem with a retry
// hint, the rest follow the status.
func catalogProblem(err error, location string) *Problem {
	if errors.Is(err, catalogpkg.ErrCatalogStorageUnsupported) {
		return NewProblem(TypeCapabilityUnsupported, "The selected user storage does not support this catalog query.")
	}
	if errors.Is(err, catalogpkg.ErrCatalogCursorChanged) {
		return NewProblem(TypeInvalidCursor, "The catalog source changed. Restart from the first page.")
	}
	var apiErr *handlers.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusNotImplemented:
			return NewProblem(TypeCapabilityUnsupported, apiErr.Message)
		case http.StatusServiceUnavailable:
			if apiErr.Code == "catalog_storage_unsupported" {
				return NewProblem(TypeCapabilityUnsupported, "The selected user storage does not support this catalog query.")
			}
		case http.StatusBadRequest:
			if apiErr.Code == "catalog_cursor_changed" {
				return NewProblem(TypeInvalidCursor, "The catalog source changed. Restart from the first page.")
			}
			return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: location, Code: codeInvalid, Detail: apiErr.Message})
		case http.StatusGatewayTimeout:
			return NewProblem(TypeDependencyUnavailable, apiErr.Message).WithRetryAfter(5)
		}
	}
	if errors.Is(err, context.Canceled) {
		return NewProblem(TypeInternalError, "The request was canceled.")
	}
	return serviceProblem(err)
}

// catalogValues renders the browse input in the catalog parser's grammar
// so v2 and v1 build the same CatalogRequest from the same choices.
func (in *CatalogBrowseInput) catalogValues() (url.Values, *Problem) {
	v := url.Values{}
	if in.LibraryID != "" {
		if _, p := in.LibraryID.positive("query.library_id"); p != nil {
			return nil, p
		}
	}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("source", in.Source)
	set("scope", in.Scope)
	set("section_id", in.SectionID)
	set("library_id", string(in.LibraryID))
	set("collection_id", in.CollectionID)
	set("person_id", string(in.PersonID))
	set("q", in.Q)
	set("name_prefix", in.NamePrefix)
	set("match", in.Match)
	if in.QueryLimit > 0 {
		v.Set("query_limit", strconv.Itoa(in.QueryLimit))
	}
	set("type", in.Type)
	set("genre", in.Genre)
	set("status", in.Status)
	if in.YearMin > 0 {
		v.Set("year_min", strconv.Itoa(in.YearMin))
	}
	if in.YearMax > 0 {
		v.Set("year_max", strconv.Itoa(in.YearMax))
	}
	if len(in.ContentRating) > 0 {
		v.Set("content_rating", strings.Join(in.ContentRating, ","))
	}
	if in.Sort != "" {
		fields := catalogSortFields()
		if (in.Source == "" || in.Source == string(catalogpkg.CatalogSourceQuery)) && strings.TrimSpace(in.Q) != "" {
			fields = append(fields, "relevance")
		}
		terms, p := ParseSort(in.Sort, fields)
		if p != nil {
			return nil, p
		}
		if len(terms) != 1 {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationQuerySort, Code: codeInvalidSortField, Detail: "the catalog sorts by one field at a time"})
		}
		v.Set("sort", terms[0].Field)
		if terms[0].Desc {
			v.Set("order", "desc")
		} else {
			v.Set("order", "asc")
		}
	}
	if in.SkipTotal {
		v.Set("include_total", "false")
	}
	return v, nil
}

// catalogSortFields is the sort allowlist: every field the query executor
// sorts by, personalized ones included, since the browse always has a
// profile.
func catalogSortFields() []string {
	set := catalogpkg.QuerySortFieldSet(true)
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	return out
}

func (in *CatalogFiltersInput) catalogValues() url.Values {
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("source", in.Source)
	set("scope", in.Scope)
	set("section_id", in.SectionID)
	set("library_id", string(in.LibraryID))
	set("collection_id", in.CollectionID)
	set("person_id", string(in.PersonID))
	set("type", in.Type)
	return v
}

// parseCatalogRequest runs the shared parser and reports its refusal as a
// 422 on the query parameter the message names; the source when it names
// none, since the source decides what the rest must carry.
func parseCatalogRequest(values url.Values) (catalogpkg.CatalogRequest, *Problem) {
	req, err := catalogpkg.ParseCatalogRequest(values)
	if err != nil {
		location := "query.source"
		for _, name := range []string{"section_id", "collection_id", "person_id", "library_id", "scope", "groups"} {
			if strings.Contains(err.Error(), name) {
				location = "query." + name
				break
			}
		}
		return catalogpkg.CatalogRequest{}, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: location, Code: codeInvalid, Detail: err.Error()})
	}
	return req, nil
}

// --- handlers ---

func (reg *Registry) listCatalogItems(ctx context.Context, cursors *Cursors, in *CatalogBrowseInput) (*CatalogBrowseOutput, error) {
	svc, p := reg.catalogBrowse()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.ImageSize, "", "")
	if p != nil {
		return nil, p
	}
	values, p := in.catalogValues()
	if p != nil {
		return nil, p
	}
	req, p := parseCatalogRequest(values)
	if p != nil {
		return nil, p
	}
	if in.Groups != "" {
		var groups []CatalogQueryGroup
		decoder := json.NewDecoder(strings.NewReader(in.Groups))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&groups); err != nil || !json.Valid([]byte(in.Groups)) {
			return nil, NewProblem(TypeValidationFailed, "Invalid structured filters.").WithErrors(ProblemError{Location: "query.groups", Code: codeInvalid, Detail: "expected a JSON array of rule groups"})
		}
		for _, g := range groups {
			rules := make([]catalogpkg.QueryRule, 0, len(g.Rules))
			for _, rule := range g.Rules {
				rules = append(rules, catalogpkg.QueryRule{Field: rule.Field, Op: rule.Op, Value: rule.Value})
			}
			req.Query.Groups = append(req.Query.Groups, catalogpkg.QueryGroup{Match: g.Match, Rules: rules})
		}
		canonical, _ := json.Marshal(groups)
		values.Set("groups", string(canonical))
		if err := req.ValidateQueryDefinition(); err != nil {
			return nil, NewProblem(TypeValidationFailed, "Invalid structured filters.").WithErrors(ProblemError{Location: "query.groups", Code: codeInvalid, Detail: err.Error()})
		}
	}
	operationID := in.operationID
	if operationID == "" {
		operationID = opListCatalogItems
	}
	values.Del("include_total")
	values.Set("limit", strconv.Itoa(in.Limit))
	claims := claimsFrom(ctx)
	scope := CursorScope{
		OperationID: operationID,
		Security:    strconv.Itoa(claims.UserID) + "/" + viewer.ProfileID + "/" + viewerScopeDigest(ctx),
		Filter:      values.Encode() + "&group=" + in.Group + "&image_size=" + in.ImageSize,
		Sort:        req.Query.Sort.Field + "," + req.Query.Sort.Order,
		Tiebreaker:  tiebreakerOffset,
	}
	var pos catalogBrowsePosition
	if in.Cursor != "" {
		if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
			return nil, p
		}
		if pos.Snapshot != "" {
			if t, err := time.Parse(time.RFC3339Nano, pos.Snapshot); err == nil {
				req.SnapshotAt = &t
			}
		}
	}
	req.Offset = pos.Offset
	if in.Seek > 0 {
		req.Seek = new(in.Seek)
		req.Offset = in.Seek
	}
	req.CursorPaging = true
	req.After = pos.After
	req.ResolvedSort = pos.Sort
	req.Limit = in.Limit
	view, err := svc.Browse(ctx, viewer, req, in.Group == "work")
	if err != nil {
		return nil, catalogProblem(err, "query.source")
	}
	if view.ResolvedSort != nil {
		req.ResolvedSort = view.ResolvedSort
	} else if view.EffectiveSort != nil {
		req.ResolvedSort = &catalogpkg.QuerySort{Field: view.EffectiveSort.Field, Order: view.EffectiveSort.Order}
	}
	next := ""
	if view.HasMore {
		next, err = cursors.Encode(scope, catalogBrowsePosition{Offset: req.Offset + len(view.Items), Snapshot: view.Snapshot, After: view.Next, Sort: req.ResolvedSort})
		if err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	items := make([]CatalogItem, 0, len(view.Items))
	for _, item := range view.Items {
		items = append(items, catalogItemOfListing(item))
	}
	window, err := cursors.Encode(scope, catalogBrowsePosition{Snapshot: view.Snapshot, Sort: req.ResolvedSort, After: view.CursorScope})
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	out := CatalogBrowseCollection{WindowCursor: window, Collection: Paginated(items, next), Total: view.Total, TotalExact: view.TotalExact}
	if d := view.SearchDiagnostics; d != nil {
		out.SearchDiagnostics = &CatalogSearchDiagnostics{Provider: d.Provider, Mode: d.Mode, SemanticUsed: d.SemanticUsed, FallbackReason: d.FallbackReason, IndexPendingUpdates: d.IndexPendingUpdates}
		out.SearchDiagnostics.ResultWindowLimit = d.ResultWindowLimit
		if d.SessionExpiresAt != nil {
			out.SearchDiagnostics.SessionExpiresAt = new(NewInstant(*d.SessionExpiresAt))
		}
	}
	if s := view.EffectiveSort; s != nil {
		out.EffectiveSort = &CatalogEffectiveSort{Field: s.Field, Order: s.Order}
	}
	return &CatalogBrowseOutput{Body: out}, nil
}

func (reg *Registry) listAudiobookGroups(ctx context.Context, cursors *Cursors, in *AudiobookGroupsInput) (*AudiobookGroupCollectionOutput, error) {
	svc, p := reg.catalogBrowse()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.ImageSize, "", "")
	if p != nil {
		return nil, p
	}
	libraryID, p := in.LibraryID.positive("query.library_id")
	if p != nil {
		return nil, p
	}
	groupBy, _ := catalogpkg.ParseAudiobookGroupBy(in.GroupBy)
	claims := claimsFrom(ctx)
	scope := CursorScope{
		OperationID: opListAudiobookGroups,
		Security:    strconv.Itoa(claims.UserID) + "/" + viewer.ProfileID + "/" + viewerScopeDigest(ctx),
		Filter:      "library_id=" + strconv.Itoa(libraryID) + "&group_by=" + in.GroupBy + "&q=" + in.Q + "&image_size=" + in.ImageSize + "&limit=" + strconv.Itoa(in.Limit),
		Sort:        in.Sort,
		Tiebreaker:  "group_key",
	}
	var after *catalogpkg.AudiobookGroupCursor
	if in.Cursor != "" {
		after = new(catalogpkg.AudiobookGroupCursor)
		if p := cursors.Decode(scope, in.Cursor, after); p != nil {
			return nil, p
		}
	}
	query := catalogpkg.AudiobookGroupsQuery{
		LibraryID: libraryID, GroupBy: groupBy, SearchPrefix: strings.TrimSpace(in.Q),
		IncludeTotal: !in.SkipTotal,
		Sort:         in.Sort, Limit: in.Limit, CursorPaging: true, After: after,
	}
	view, err := svc.AudiobookGroups(ctx, viewer, query)
	if err != nil {
		return nil, catalogProblem(err, "query.library_id")
	}
	next := ""
	if view.HasMore {
		if view.Next == nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
		next, err = cursors.Encode(scope, view.Next)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	groups := make([]AudiobookGroup, 0, len(view.Groups))
	for _, g := range view.Groups {
		groups = append(groups, AudiobookGroup{Name: g.Name, ItemCount: g.ItemCount, TotalDurationSeconds: g.TotalDurationSeconds,
			InProgressCount: g.InProgressCount, FinishedCount: g.FinishedCount, PosterURLs: NonNil(g.PosterURLs)})
	}
	return &AudiobookGroupCollectionOutput{Body: AudiobookGroupCollection{Collection: Paginated(groups, next), Total: view.Total, TotalExact: view.TotalExact}}, nil
}

func (reg *Registry) getCatalogFilters(ctx context.Context, in *CatalogFiltersInput) (*CatalogFiltersOutput, error) {
	svc, p := reg.catalogBrowse()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, "", "", "")
	if p != nil {
		return nil, p
	}
	req, p := parseCatalogRequest(in.catalogValues())
	if p != nil {
		return nil, p
	}
	view, err := svc.Filters(ctx, viewer, req, !in.SkipTechnical)
	if err != nil {
		return nil, catalogProblem(err, "query.source")
	}
	out := CatalogFilters{
		Genres: NonNil(view.Genres), Studios: NonNil(view.Studios), Networks: NonNil(view.Networks), Countries: NonNil(view.Countries),
		OriginalLanguages: NonNil(view.OriginalLanguages), ContentRatings: NonNil(view.ContentRatings),
		Authors: NonNil(view.Authors), Narrators: NonNil(view.Narrators), Series: NonNil(view.Series),
	}
	if view.Resolutions != nil {
		out.Technical = &CatalogTechnicalFilters{Resolutions: NonNil(*view.Resolutions), AudioLanguages: NonNil(*view.AudioLanguages), SubtitleLanguages: NonNil(*view.SubtitleLanguages)}
	}
	return &CatalogFiltersOutput{Body: out}, nil
}

func (reg *Registry) searchCatalogFacet(ctx context.Context, in *CatalogFacetSearchInput) (*CatalogFacetMatchesOutput, error) {
	svc, p := reg.catalogBrowse()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, "", "", "")
	if p != nil {
		return nil, p
	}
	req, p := parseCatalogRequest(in.catalogValues())
	if p != nil {
		return nil, p
	}
	view, err := svc.SearchFacet(ctx, viewer, req, in.Facet, in.Q, in.Limit)
	if err != nil {
		return nil, catalogProblem(err, "query.facet")
	}
	return &CatalogFacetMatchesOutput{Body: CatalogFacetMatches{Matches: NonNil(view.Matches), HasMore: view.HasMore}}, nil
}

func (reg *Registry) queryCatalogItems(ctx context.Context, cursors *Cursors, in *CatalogQueryInput) (*CatalogBrowseOutput, error) {
	body := in.Body
	limit := body.Limit
	if limit == 0 {
		limit = 50
	}
	sort := body.Sort
	if body.Order == "desc" && sort != "" {
		sort = "-" + sort
	}
	groups, err := json.Marshal(body.Groups)
	if err != nil {
		return nil, NewProblem(TypeValidationFailed, "Invalid structured filters.")
	}
	output, err := reg.listCatalogItems(ctx, cursors, &CatalogBrowseInput{
		Source: body.Source, Scope: body.Scope, SectionID: body.SectionID,
		LibraryID: body.LibraryID, CollectionID: body.CollectionID, PersonID: body.PersonID,
		Q: body.Q, NamePrefix: body.NamePrefix, Match: body.Match, Type: body.Type,
		Sort: sort, Group: body.Group, SkipTotal: body.SkipTotal, ImageSize: in.ImageSize,
		LimitParam: LimitParam{Limit: limit}, Cursor: body.Cursor, Groups: string(groups),
		QueryLimit: body.QueryLimit, Seek: body.Seek, operationID: opQueryCatalogItems,
	})
	if p, ok := errors.AsType[*Problem](err); ok {
		for i := range p.Errors {
			if strings.HasPrefix(p.Errors[i].Location, "query.") && p.Errors[i].Location != locationQueryImageSize {
				p.Errors[i].Location = "body." + strings.TrimPrefix(p.Errors[i].Location, "query.")
			}
		}
	}
	return output, err
}

func (reg *Registry) getCatalogItem(ctx context.Context, in *CatalogItemInput) (*CatalogItemDetailOutput, error) {
	svc, p := reg.catalogItems()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.ImageSize, in.LibraryID, in.FileID)
	if p != nil {
		return nil, p
	}
	detail, err := svc.ItemDetail(ctx, viewer, in.ID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := catalogItemDetailOf(detail)
	if reg.deps.ThemeSongs != nil && (detail.Type == themeOwnerMovie || detail.Type == themeOwnerSeries || detail.Type == themeOwnerSeason || detail.Type == themeOwnerEpisode) {
		themes, err := reg.deps.ThemeSongs.Discover(ctx, in.ID, true, viewer.Access)
		if err != nil {
			slog.WarnContext(ctx, "catalog theme lookup failed", "component", "apiv2", "item_id", in.ID, "error", err)
			return &CatalogItemDetailOutput{Body: out}, nil
		}
		out.Themes = &ThemeSongSet{OwnerID: themes.OwnerID, Items: []ThemeSong{}}
		for _, song := range themes.Items {
			out.Themes.Items = append(out.Themes.Items, ThemeSong(song))
		}
	}
	return &CatalogItemDetailOutput{Body: out}, nil
}

func (reg *Registry) listCatalogItemVersions(ctx context.Context, in *CatalogItemInput) (*FileVersionCollectionOutput, error) {
	svc, p := reg.catalogItems()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.ImageSize, in.LibraryID, in.FileID)
	if p != nil {
		return nil, p
	}
	versions, err := svc.ItemVersions(ctx, viewer, in.ID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &FileVersionCollectionOutput{Body: FileVersionCollection{Collection: NewCollection(fileVersionsOf(versions))}}, nil
}

func (reg *Registry) listCatalogItemMangaFiles(ctx context.Context, in *CatalogItemInput) (*MangaFilesOutput, error) {
	svc, p := reg.catalogItems()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.ImageSize, in.LibraryID, in.FileID)
	if p != nil {
		return nil, p
	}
	files, err := svc.MangaFiles(ctx, viewer, in.ID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]MangaChapterFile, 0, len(files.Files))
	for _, f := range files.Files {
		items = append(items, MangaChapterFile{ContentID: f.ContentID, Title: f.Title, ChapterIndex: f.ChapterIndex, Volume: f.Volume,
			FilePath: f.FilePath, FileName: f.FileName, FileSize: f.FileSize, Container: f.Container})
	}
	return &MangaFilesOutput{Body: MangaFiles{Collection: NewCollection(items), FolderPaths: files.FolderPaths}}, nil
}

func (reg *Registry) listCatalogItemEpisodes(ctx context.Context, in *CatalogItemInput) (*EpisodeCollectionOutput, error) {
	svc, p := reg.catalogItems()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.ImageSize, in.LibraryID, in.FileID)
	if p != nil {
		return nil, p
	}
	episodes, err := svc.ItemEpisodes(ctx, viewer, in.ID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &EpisodeCollectionOutput{Body: EpisodeCollection{Collection: NewCollection(episodesOf(episodes))}}, nil
}

func (reg *Registry) listSeriesSeasons(ctx context.Context, in *CatalogSeasonsInput) (*SeasonCollectionOutput, error) {
	svc, p := reg.catalogItems()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.ImageSize, in.LibraryID, "")
	if p != nil {
		return nil, p
	}
	seasons, err := svc.SeriesSeasons(ctx, viewer, in.ID, in.IncludeArtwork)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]Season, 0, len(seasons))
	for _, s := range seasons {
		items = append(items, seasonOf(s))
	}
	return &SeasonCollectionOutput{Body: SeasonCollection{Collection: NewCollection(items)}}, nil
}

func (reg *Registry) getSeriesSeason(ctx context.Context, in *CatalogSeasonInput) (*SeasonOutput, error) {
	svc, p := reg.catalogItems()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.ImageSize, in.LibraryID, "")
	if p != nil {
		return nil, p
	}
	season, err := svc.SeriesSeason(ctx, viewer, in.ID, in.Number)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &SeasonOutput{Body: seasonOf(season)}, nil
}

func (reg *Registry) listSeasonEpisodes(ctx context.Context, in *CatalogSeasonInput) (*EpisodeCollectionOutput, error) {
	svc, p := reg.catalogItems()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.ImageSize, in.LibraryID, "")
	if p != nil {
		return nil, p
	}
	episodes, err := svc.SeasonEpisodes(ctx, viewer, in.ID, in.Number)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &EpisodeCollectionOutput{Body: EpisodeCollection{Collection: NewCollection(episodesOf(episodes))}}, nil
}

// --- renderers ---

func watchRollupOf(d *catalogpkg.SeasonUserData) *WatchRollup {
	if d == nil {
		return nil
	}
	out := &WatchRollup{PositionSeconds: d.PositionSeconds, DurationSeconds: d.DurationSeconds, IsInProgress: d.IsInProgress,
		WatchedCount: d.WatchedCount, UnplayedCount: d.UnplayedCount, InProgressCount: d.InProgressCount, Played: d.Played,
		LastResolution: d.LastResolution, LastHDR: d.LastHDR, LastCodecVideo: d.LastCodecVideo, LastEditionKey: d.LastEditionKey}
	if d.LastFileID != nil {
		out.LastFileID = IDFromInt(int64(*d.LastFileID))
	}
	return out
}

func idOfPositive(n int) ID {
	if n > 0 {
		return IDFromInt(int64(n))
	}
	return ""
}

func fileVersionOf(v catalogpkg.FileVersion) FileVersion {
	return FileVersion{
		FileID: IDFromInt(int64(v.FileID)), FileName: v.FileName, FilePath: v.FilePath, Resolution: v.Resolution, CodecVideo: v.CodecVideo,
		CodecAudio: v.CodecAudio, HDR: v.HDR, Container: v.Container, FileSize: v.FileSize, Duration: v.Duration, Bitrate: v.Bitrate,
		AddedAt: NewInstant(v.AddedAt), EditionRaw: v.EditionRaw, EditionKey: v.EditionKey, PresentationKind: v.PresentationKind,
		PresentationGroupKey: v.PresentationGroupKey, PresentationPartIndex: v.PresentationPartIndex, PresentationPartTotal: v.PresentationPartTotal,
		MultiEpisodeStart: v.MultiEpisodeStart, MultiEpisodeEnd: v.MultiEpisodeEnd, EffectiveAudioTrackIndex: v.EffectiveAudioTrackIndex,
		EffectiveAudioLanguage: v.EffectiveAudioLanguage, VideoTracks: v.VideoTracks, AudioTracks: v.AudioTracks, SubtitleTracks: v.SubtitleTracks,
		Chapters: v.Chapters, Intro: v.Intro, Credits: v.Credits, Recap: v.Recap, Preview: v.Preview,
	}
}

func fileVersionsOf(vs []catalogpkg.FileVersion) []FileVersion {
	out := make([]FileVersion, 0, len(vs))
	for _, v := range vs {
		out = append(out, fileVersionOf(v))
	}
	return out
}

func playbackVariantsOf(vs []catalogpkg.PlaybackVariant) []PlaybackVariant {
	if vs == nil {
		return nil
	}
	out := make([]PlaybackVariant, 0, len(vs))
	for _, v := range vs {
		parts := make([]PlaybackVariantPart, 0, len(v.Parts))
		for _, part := range v.Parts {
			parts = append(parts, PlaybackVariantPart{PartIndex: part.PartIndex, DefaultFileID: idOfPositive(part.DefaultFileID), TotalDuration: part.TotalDuration, Versions: fileVersionsOf(part.Versions)})
		}
		out = append(out, PlaybackVariant{VariantID: v.VariantID, EditionRaw: v.EditionRaw, EditionKey: v.EditionKey, PresentationKind: v.PresentationKind,
			PresentationGroupKey: v.PresentationGroupKey, PartCount: v.PartCount, TotalDuration: v.TotalDuration, DefaultFileID: idOfPositive(v.DefaultFileID), Parts: parts})
	}
	return out
}

// catalogItemDetailOf renders the detail document; the card members come
// from the same detail, so a detail never disagrees with its own card. The
// detail service does not load keywords, the original language, or the
// match status, so those card members are empty here as they are in v1.
func catalogItemDetailOf(d *catalogpkg.ItemDetail) CatalogItemDetail {
	card := CatalogItem{
		ContentID: d.ContentID, PlayContentID: d.PlayContentID, Type: d.Type, Title: d.Title,
		SeriesID: d.SeriesID, SeriesTitle: d.SeriesTitle, SeasonNumber: d.SeasonNumber, EpisodeNumber: d.EpisodeNumber,
		Year: d.Year, Runtime: d.Runtime, Genres: NonNil(d.Genres), Keywords: []string{}, Studios: d.Studios, Networks: d.Networks,
		ContentRating: d.ContentRating, AdvisoryAge: d.AdvisoryAge, AdvisorySource: d.AdvisorySource,
		ShowStatus: d.ShowStatus,
		RatingIMDB: d.RatingIMDB, RatingTMDB: d.RatingTMDB, RatingRTCritic: d.RatingRTCritic, RatingRTAudience: d.RatingRTAudience,
		Overview: d.Overview, ReleaseDate: d.ReleaseDate, LastAirDate: d.LastAirDate,
		PosterURL: d.PosterURL, PosterThumbhash: d.PosterThumbhash, BackdropURL: d.BackdropURL, BackdropThumbhash: d.BackdropThumbhash, LogoURL: d.LogoURL,
		OverlaySummary: catalogOverlayOf(d.OverlaySummary), WorkID: d.WorkID, WorkTitle: d.WorkTitle,
	}
	if s := d.UserState; s != nil {
		card.UserState = &CatalogItemUserState{Played: s.Played, IsFavorite: s.IsFavorite, InWatchlist: s.InWatchlist}
	}
	for _, f := range d.WorkFormats {
		card.WorkFormats = append(card.WorkFormats, CatalogWorkFormat{Type: f.Type, ContentID: f.ContentID, LibraryID: idOfPositive(f.LibraryID)})
	}
	out := CatalogItemDetail{
		CatalogItem: card,
		SortTitle:   d.SortTitle, OriginalTitle: d.OriginalTitle, Tagline: d.Tagline, PendingTranslationLanguage: d.PendingTranslationLanguage,
		ImdbID: d.ImdbID, TmdbID: d.TmdbID, TvdbID: d.TvdbID, Cast: NonNil(d.Cast), Crew: NonNil(d.Crew), Countries: d.Countries, LockedFields: d.LockedFields,
		FirstAirDate: d.FirstAirDate, AirTime: d.AirTime, AirTimezone: d.AirTimezone, SeasonCount: d.SeasonCount, EpisodeCount: d.EpisodeCount,
		AirDate: d.AirDate, IsSpecials: d.IsSpecials, UserData: watchRollupOf(d.SeasonUserData), UserRating: d.UserRating,
		Versions: fileVersionsOf(d.Versions), PlaybackVariants: playbackVariantsOf(d.PlaybackVariants), Videos: d.Videos, Extras: d.Extras,
		FolderPaths: d.FolderPaths, Subtitles: NonNil(d.Subtitles), Intro: d.Intro, Credits: d.Credits, Recap: d.Recap, Preview: d.Preview,
		EffectiveVersionResolution: d.EffectiveVersionResolution,
		EffectiveVersionHDR:        d.EffectiveVersionHDR, EffectiveVersionCodecVideo: d.EffectiveVersionCodecVideo, EffectiveVersionEditionKey: d.EffectiveVersionEditionKey,
		Audiobook: d.Audiobook, Ebook: d.Ebook, Manga: d.Manga,
	}
	if d.EffectiveSubtitleTrackSignature != nil {
		out.EffectiveSubtitleTrackSignature = watchSignatureOf(*d.EffectiveSubtitleTrackSignature)
	}
	if d.HasEffectiveSubtitleLang {
		out.EffectiveSubtitleLanguage = &d.EffectiveSubtitleLanguage
	}
	if d.HasEffectiveSubtitleMode {
		out.EffectiveSubtitleMode = &d.EffectiveSubtitleMode
	}
	if d.HasEffectiveShowForcedSubtitles {
		out.EffectiveShowForcedSubtitles = &d.EffectiveShowForcedSubtitles
	}
	return out
}

// datePtr renders a stored YYYY-MM-DD string as an optional member.
func datePtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func episodesOf(views []handlers.EpisodeView) []Episode {
	out := make([]Episode, 0, len(views))
	for _, e := range views {
		ep := Episode{ContentID: e.ContentID, SeasonNumber: e.SeasonNumber, EpisodeNumber: e.EpisodeNumber, Title: e.Title, Overview: e.Overview,
			AirDate: datePtr(e.AirDate), Runtime: e.Runtime, ImdbID: e.ImdbID, TmdbID: e.TmdbID, TvdbID: e.TvdbID, StillURL: e.StillURL, StillThumbhash: e.StillThumbhash,
			UserData: watchRollupOf(e.UserData), OverlaySummary: catalogOverlayOf(e.OverlaySummary)}
		for _, f := range e.Files {
			ep.Files = append(ep.Files, EpisodeFile{FileID: IDFromInt(int64(f.FileID)), Resolution: f.Resolution, CodecVideo: f.CodecVideo, HDR: f.HDR,
				AudioChannels: f.AudioChannels, Container: f.Container, FileSize: f.FileSize})
		}
		out = append(out, ep)
	}
	return out
}

func seasonOf(s handlers.SeasonView) Season {
	return Season{ContentID: s.ContentID, PlayContentID: s.PlayContentID, SeasonNumber: s.SeasonNumber, IsSpecials: s.IsSpecials, Title: s.Title,
		Overview: s.Overview, AirDate: datePtr(s.AirDate), EpisodeCount: s.EpisodeCount, PosterURL: s.PosterURL, PosterThumbhash: s.PosterThumbhash,
		UserData: watchRollupOf(s.UserData)}
}
