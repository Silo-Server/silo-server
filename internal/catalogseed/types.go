package catalogseed

import "time"

const CurrentBundleVersion = 2

type Manifest struct {
	FormatVersion int       `json:"format_version"`
	ExportedAt    time.Time `json:"exported_at"`
	SchemaVersion int       `json:"schema_version"`
}

type Bundle struct {
	Manifest     Manifest            `json:"manifest"`
	Libraries    []LibraryRecord     `json:"libraries"`
	Items        []ItemRecord        `json:"items"`
	People       []PersonRecord      `json:"people"`
	Embeddings   []EmbeddingRecord   `json:"embeddings"`
	Seasons      []SeasonRecord      `json:"seasons"`
	Episodes     []EpisodeRecord     `json:"episodes"`
	Files        []FileRecord        `json:"files"`
	LibraryLinks []LibraryLinkRecord `json:"library_links"`
}

type LibraryRecord struct {
	ExportedID            int        `json:"exported_id"`
	Paths                 []string   `json:"paths"`
	Type                  string     `json:"type"`
	Name                  string     `json:"name"`
	Enabled               bool       `json:"enabled"`
	LastScannedAt         *time.Time `json:"last_scanned_at,omitempty"`
	ScanWarningCode       *string    `json:"scan_warning_code,omitempty"`
	ScanWarningMessage    *string    `json:"scan_warning_message,omitempty"`
	ScanWarningAt         *time.Time `json:"scan_warning_at,omitempty"`
	AllowEmptyCleanupOnce bool       `json:"allow_empty_cleanup_once"`
}

type ItemRecord struct {
	ContentID         string     `json:"content_id"`
	Type              string     `json:"type"`
	Title             string     `json:"title"`
	SortTitle         string     `json:"sort_title"`
	OriginalTitle     string     `json:"original_title"`
	Year              int        `json:"year"`
	Genres            []string   `json:"genres"`
	ContentRating     string     `json:"content_rating"`
	Runtime           int        `json:"runtime"`
	Overview          string     `json:"overview"`
	Tagline           string     `json:"tagline"`
	RatingIMDB        *float64   `json:"rating_imdb,omitempty"`
	RatingTMDB        *float64   `json:"rating_tmdb,omitempty"`
	RatingRTCritic    *int       `json:"rating_rt_critic,omitempty"`
	RatingRTAudience  *int       `json:"rating_rt_audience,omitempty"`
	ImdbID            string     `json:"imdb_id"`
	TmdbID            string     `json:"tmdb_id"`
	TvdbID            string     `json:"tvdb_id"`
	PosterPath        string     `json:"poster_path"`
	PosterThumbhash   string     `json:"poster_thumbhash"`
	BackdropPath      string     `json:"backdrop_path"`
	BackdropThumbhash string     `json:"backdrop_thumbhash"`
	LogoPath          string     `json:"logo_path"`
	MetadataS3Path    string     `json:"metadata_s3_path"`
	MetadataEtag      string     `json:"metadata_etag"`
	SeasonCount       *int       `json:"season_count,omitempty"`
	Studios           []string   `json:"studios"`
	Networks          []string   `json:"networks"`
	Countries         []string   `json:"countries"`
	Keywords          []string   `json:"keywords"`
	OriginalLanguage  string     `json:"original_language"`
	ReleaseDate       *string    `json:"release_date,omitempty"`
	FirstAirDate      *string    `json:"first_air_date,omitempty"`
	LastAirDate       *string    `json:"last_air_date,omitempty"`
	AirTime           *string    `json:"air_time,omitempty"`
	AirTimezone       *string    `json:"air_timezone,omitempty"`
	MatchedAt         *time.Time `json:"matched_at,omitempty"`
	LastRefreshed     *time.Time `json:"last_refreshed,omitempty"`
	RefreshFailures   int        `json:"refresh_failures"`
	LockedFields      []int      `json:"locked_fields"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type PersonRecord struct {
	ContentID      string `json:"content_id"`
	PersonID       int64  `json:"person_id"`
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	Character      string `json:"character"`
	SortOrder      int    `json:"sort_order"`
	TmdbID         string `json:"tmdb_id"`
	ImdbID         string `json:"imdb_id"`
	TvdbID         string `json:"tvdb_id"`
	PlexGUID       string `json:"plex_guid"`
	PhotoPath      string `json:"photo_path"`
	PhotoThumbhash string `json:"photo_thumbhash"`
}

type EmbeddingRecord struct {
	MediaItemID   string    `json:"media_item_id"`
	Embedding     []float32 `json:"embedding"`
	Model         string    `json:"model"`
	CanonicalText string    `json:"canonical_text"`
}

type SeasonRecord struct {
	ContentID       string     `json:"content_id"`
	SeriesID        string     `json:"series_id"`
	SeasonNumber    int        `json:"season_number"`
	Title           string     `json:"title"`
	Overview        string     `json:"overview"`
	AirDate         *time.Time `json:"air_date,omitempty"`
	PosterPath      string     `json:"poster_path"`
	PosterThumbhash string     `json:"poster_thumbhash"`
	MetadataS3Path  string     `json:"metadata_s3_path"`
	MetadataEtag    string     `json:"metadata_etag"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type EpisodeRecord struct {
	ContentID      string     `json:"content_id"`
	SeriesID       string     `json:"series_id"`
	SeasonID       string     `json:"season_id"`
	SeasonNumber   int        `json:"season_number"`
	EpisodeNumber  int        `json:"episode_number"`
	Title          string     `json:"title"`
	Overview       string     `json:"overview"`
	AirDate        *time.Time `json:"air_date,omitempty"`
	Runtime        int        `json:"runtime"`
	RatingIMDB     *float64   `json:"rating_imdb,omitempty"`
	RatingTMDB     *float64   `json:"rating_tmdb,omitempty"`
	StillPath      string     `json:"still_path"`
	StillThumbhash string     `json:"still_thumbhash"`
	MetadataS3Path string     `json:"metadata_s3_path"`
	MetadataEtag   string     `json:"metadata_etag"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type VideoTrackRecord struct {
	Title           string `json:"title,omitempty"`
	Codec           string `json:"codec,omitempty"`
	DolbyVision     string `json:"dolby_vision,omitempty"`
	DVProfile       int    `json:"dv_profile,omitempty"`
	DVBLCompatID    int    `json:"dv_bl_compat_id,omitempty"`
	DVELPresent     bool   `json:"dv_el_present,omitempty"`
	HDR10Plus       bool   `json:"hdr10_plus,omitempty"`
	Profile         string `json:"profile,omitempty"`
	Level           int    `json:"level,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	AspectRatio     string `json:"aspect_ratio,omitempty"`
	Interlaced      bool   `json:"interlaced"`
	FrameRate       string `json:"frame_rate,omitempty"`
	Bitrate         int    `json:"bitrate,omitempty"`
	VideoRange      string `json:"video_range,omitempty"`
	VideoRangeType  string `json:"video_range_type,omitempty"`
	ColorRange      string `json:"color_range,omitempty"`
	ColorPrimaries  string `json:"color_primaries,omitempty"`
	ColorSpace      string `json:"color_space,omitempty"`
	ColorTransfer   string `json:"color_transfer,omitempty"`
	BitDepth        int    `json:"bit_depth,omitempty"`
	PixelFormat     string `json:"pixel_format,omitempty"`
	ReferenceFrames int    `json:"reference_frames,omitempty"`
}

type AudioTrackRecord struct {
	Title         string `json:"title,omitempty"`
	EmbeddedTitle string `json:"embedded_title,omitempty"`
	Language      string `json:"language,omitempty"`
	Codec         string `json:"codec,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Layout        string `json:"layout,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	Bitrate       int    `json:"bitrate,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	BitDepth      int    `json:"bit_depth,omitempty"`
	Default       bool   `json:"default"`
}

type SubtitleTrackRecord struct {
	Index           int    `json:"index"`
	Language        string `json:"language"`
	Codec           string `json:"codec"`
	Title           string `json:"title,omitempty"`
	EmbeddedTitle   string `json:"embedded_title,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
	Forced          bool   `json:"forced"`
	Default         bool   `json:"default"`
	HearingImpaired bool   `json:"hearing_impaired"`
	External        bool   `json:"external"`
	FileName        string `json:"file_name,omitempty"`
}

type ExternalSubtitleRecord struct {
	Path            string `json:"path"`
	Language        string `json:"language"`
	Format          string `json:"format"`
	Title           string `json:"title,omitempty"`
	EmbeddedTitle   string `json:"embedded_title,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
	Forced          bool   `json:"forced"`
	Default         bool   `json:"default"`
	HearingImpaired bool   `json:"hearing_impaired"`
}

type FileRecord struct {
	ContentID         string                   `json:"content_id"`
	EpisodeID         string                   `json:"episode_id"`
	SeasonNumber      int                      `json:"season_number"`
	EpisodeNumber     int                      `json:"episode_number"`
	MediaFolderID     int                      `json:"media_folder_id"`
	FilePath          string                   `json:"file_path"`
	FileSize          int64                    `json:"file_size"`
	FileHash          string                   `json:"file_hash"`
	CodecVideo        string                   `json:"codec_video"`
	CodecAudio        string                   `json:"codec_audio"`
	Resolution        string                   `json:"resolution"`
	AudioChannels     int                      `json:"audio_channels"`
	HDR               bool                     `json:"hdr"`
	Container         string                   `json:"container"`
	Duration          int                      `json:"duration"`
	Bitrate           int                      `json:"bitrate"`
	VideoTracks       []VideoTrackRecord       `json:"video_tracks"`
	AudioTracks       []AudioTrackRecord       `json:"audio_tracks"`
	SubtitleTracks    []SubtitleTrackRecord    `json:"subtitle_tracks"`
	ExternalSubtitles []ExternalSubtitleRecord `json:"external_subtitles"`
	IntroStart        *float64                 `json:"intro_start,omitempty"`
	IntroEnd          *float64                 `json:"intro_end,omitempty"`
	CreditsStart      *float64                 `json:"credits_start,omitempty"`
	CreditsEnd        *float64                 `json:"credits_end,omitempty"`
	ProbeSource       string                   `json:"probe_source"`
	ProbeUpdatedAt    *time.Time               `json:"probe_updated_at,omitempty"`
	MissingSince      *time.Time               `json:"missing_since,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
}

type LibraryLinkRecord struct {
	ContentID     string    `json:"content_id"`
	MediaFolderID int       `json:"media_folder_id"`
	FirstSeenAt   time.Time `json:"first_seen_at"`
}

type PathRewrite struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type ExportOptions struct {
	LibraryIDs []int `json:"library_ids"`
}

type ExportSummary struct {
	FormatVersion        int `json:"format_version"`
	SchemaVersion        int `json:"schema_version"`
	LibrariesExported    int `json:"libraries_exported"`
	ItemsExported        int `json:"items_exported"`
	PeopleExported       int `json:"people_exported"`
	EmbeddingsExported   int `json:"embeddings_exported"`
	SeasonsExported      int `json:"seasons_exported"`
	EpisodesExported     int `json:"episodes_exported"`
	FilesExported        int `json:"files_exported"`
	LibraryLinksExported int `json:"library_links_exported"`
}

type ExportProgress struct {
	Message string `json:"message"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
}

type ConflictMode string

const (
	ConflictModeSkipExisting ConflictMode = "skip_existing"
	ConflictModeOverwrite    ConflictMode = "overwrite_existing"
)

type ImportOptions struct {
	ConflictMode ConflictMode  `json:"conflict_mode"`
	PathRewrites []PathRewrite `json:"path_rewrites"`
}

type ImportProgress struct {
	Message string `json:"message"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
}

type ImportResult struct {
	LibrariesCreated   int      `json:"libraries_created"`
	LibrariesMatched   int      `json:"libraries_matched"`
	ItemsCreated       int      `json:"items_created"`
	ItemsUpdated       int      `json:"items_updated"`
	SeasonsCreated     int      `json:"seasons_created"`
	SeasonsUpdated     int      `json:"seasons_updated"`
	EpisodesCreated    int      `json:"episodes_created"`
	EpisodesUpdated    int      `json:"episodes_updated"`
	FilesCreated       int      `json:"files_created"`
	FilesUpdated       int      `json:"files_updated"`
	LinksCreated       int      `json:"links_created"`
	CreditsReplaced    int      `json:"credits_replaced"`
	EmbeddingsImported int      `json:"embeddings_imported"`
	Skipped            int      `json:"skipped"`
	UnmatchedRoots     []string `json:"unmatched_roots,omitempty"`
}
