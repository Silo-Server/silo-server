package models

import (
	"strings"
	"time"
)

// MediaFolder represents a row in the media_folders table.
type MediaFolder struct {
	ID                       int
	Paths                    []string // from media_folder_paths child table
	Type                     string   // movies, series, mixed
	Name                     string
	Enabled                  bool
	MetadataLanguage         string // ISO 639-1 code (e.g. "en", "ja")
	ChapterThumbnailsEnabled bool
	IntroDetectionEnabled    bool
	PosterPath               string     // S3 key for library poster image
	LastScannedAt            *time.Time // nullable
	ScanWarningCode          *string
	ScanWarningMessage       *string
	ScanWarningAt            *time.Time
	AllowEmptyCleanupOnce    bool
	SortOrder                int
}

// MediaFile represents a row in the media_files table.
type MediaFile struct {
	ID                           int
	ContentID                    string // Sonyflake ID (nullable until matched)
	EpisodeID                    string // FK to episodes.content_id (nullable)
	SeasonNumber                 int    // parsed from filename (nullable)
	EpisodeNumber                int    // parsed from filename (nullable)
	MediaFolderID                int
	CanonicalRootPath            string
	ObservedRootPath             string
	ContentGroupKey              string
	GroupKeyVersion              int
	BaseTitle                    string
	BaseYear                     int
	BaseType                     string
	IdentityConfidence           string
	IdentityJSON                 []byte
	FilePath                     string
	FileSize                     int64
	FileModifiedAt               *time.Time
	FileHash                     string // OSHash (16-char hex)
	CodecVideo                   string // h264, hevc, av1
	CodecAudio                   string // aac, opus, flac
	Resolution                   string // 1080p, 2160p
	AudioChannels                int
	HDR                          bool
	Container                    string             // mkv, mp4
	Duration                     int                // seconds
	Bitrate                      int                // kbps
	VideoTracks                  []VideoTrack       // JSONB
	AudioTracks                  []AudioTrack       // JSONB
	SubtitleTracks               []SubtitleTrack    // JSONB
	ExternalSubtitles            []ExternalSubtitle // JSONB
	Chapters                     []MediaChapter     // JSONB; nil means not yet probed for chapters
	ChapterThumbnailRetryAfter   *time.Time
	ChapterThumbnailFailureCount int
	ChapterThumbnailLastError    string
	IntroStart                   *float64
	IntroEnd                     *float64
	CreditsStart                 *float64
	CreditsEnd                   *float64
	RecapStart                   *float64
	RecapEnd                     *float64
	PreviewStart                 *float64
	PreviewEnd                   *float64
	MarkersSource                *string
	MarkersConfidence            *float64
	IntroMarkersSource           *string
	IntroMarkersProvider         *string
	IntroMarkersConfidence       *float64
	IntroMarkersAlgorithm        *string
	IntroMarkersDetectedAt       *time.Time
	CreditsMarkersSource         *string
	CreditsMarkersProvider       *string
	CreditsMarkersConfidence     *float64
	CreditsMarkersAlgorithm      *string
	CreditsMarkersDetectedAt     *time.Time
	RecapMarkersSource           *string
	RecapMarkersProvider         *string
	RecapMarkersConfidence       *float64
	RecapMarkersAlgorithm        *string
	RecapMarkersDetectedAt       *time.Time
	PreviewMarkersSource         *string
	PreviewMarkersProvider       *string
	PreviewMarkersConfidence     *float64
	PreviewMarkersAlgorithm      *string
	PreviewMarkersDetectedAt     *time.Time
	EditionRaw                   string
	EditionKey                   string
	EditionConfidence            *float64
	EditionSource                string
	PresentationKind             string
	PresentationGroupKey         string
	PresentationPartIndex        int
	PresentationPartTotal        int
	MultiEpisodeStart            int
	MultiEpisodeEnd              int
	ProbeSource                  string // arrs, local
	ProbeUpdatedAt               *time.Time
	MatchAttemptedAt             *time.Time
	MissingSince                 *time.Time
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

// MediaChapter represents a single media chapter derived from embedded file metadata.
type MediaChapter struct {
	Index               int        `json:"index"`
	Title               string     `json:"title"`
	StartSeconds        float64    `json:"start_seconds"`
	EndSeconds          float64    `json:"end_seconds"`
	Source              string     `json:"source"`
	ThumbnailPath       string     `json:"thumbnail_path,omitempty"`
	ThumbnailThumbhash  string     `json:"thumbnail_thumbhash,omitempty"`
	ThumbnailRetryAfter *time.Time `json:"thumbnail_retry_after,omitempty"`
	ThumbnailFailedAt   *time.Time `json:"thumbnail_failed_at,omitempty"`
	ThumbnailLastError  string     `json:"thumbnail_last_error,omitempty"`
}

// OverlaySummary is the compact media badge payload shared across API surfaces.
type OverlaySummary struct {
	Resolution    string `json:"resolution,omitempty"`
	HDR           string `json:"hdr,omitempty"`
	Audio         string `json:"audio,omitempty"`
	AudioChannels string `json:"audio_channels,omitempty"` // "Stereo", "5.1", "7.1"
	VideoCodec    string `json:"video_codec,omitempty"`    // "H.264", "H.265", "AV1"
	Container     string `json:"container,omitempty"`      // "MKV", "MP4"
	AspectRatio   string `json:"aspect_ratio,omitempty"`   // "16:9", "2.39:1"
	ReleaseType   string `json:"release_type,omitempty"`
	Edition       string `json:"edition,omitempty"`
	MultiAudio    bool   `json:"multi_audio,omitempty"` // ≥2 distinct audio languages
	MultiSub      bool   `json:"multi_sub,omitempty"`   // ≥1 subtitle track (embedded or external)
}

// VideoTrack represents a probed video stream stored as JSONB.
type VideoTrack struct {
	Title           string `json:"title,omitempty"`
	Codec           string `json:"codec,omitempty"`
	DolbyVision     string `json:"dolby_vision,omitempty"`
	Profile         string `json:"profile,omitempty"`
	Level           int    `json:"level,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	AspectRatio     string `json:"aspect_ratio,omitempty"`
	Interlaced      bool   `json:"interlaced"`
	FrameRate       string `json:"frame_rate,omitempty"`
	Bitrate         int    `json:"bitrate,omitempty"`
	VideoRange      string `json:"video_range,omitempty"`
	ColorPrimaries  string `json:"color_primaries,omitempty"`
	ColorSpace      string `json:"color_space,omitempty"`
	ColorTransfer   string `json:"color_transfer,omitempty"`
	BitDepth        int    `json:"bit_depth,omitempty"`
	PixelFormat     string `json:"pixel_format,omitempty"`
	ReferenceFrames int    `json:"reference_frames,omitempty"`
}

// AudioTrack represents a probed audio stream stored as JSONB.
type AudioTrack struct {
	Title         string `json:"title,omitempty"`
	EmbeddedTitle string `json:"embedded_title,omitempty"`
	Language      string `json:"language,omitempty"`
	Codec         string `json:"codec,omitempty"`
	Layout        string `json:"layout,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	Bitrate       int    `json:"bitrate,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	BitDepth      int    `json:"bit_depth,omitempty"`
	Default       bool   `json:"default"`
}

// SubtitleTrack represents an embedded subtitle track stored as JSONB.
type SubtitleTrack struct {
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

// ExternalSubtitle represents a sidecar subtitle file stored as JSONB.
type ExternalSubtitle struct {
	Path            string `json:"path"`
	Language        string `json:"language"`
	Format          string `json:"format"` // srt, vtt, ass, ssa, sub
	Title           string `json:"title,omitempty"`
	EmbeddedTitle   string `json:"embedded_title,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
	Forced          bool   `json:"forced"`
	Default         bool   `json:"default"`
	HearingImpaired bool   `json:"hearing_impaired"`
}

// PersonKind identifies the role type a person has on a media item.
type PersonKind int

const (
	PersonKindActor     PersonKind = 1
	PersonKindDirector  PersonKind = 2
	PersonKindWriter    PersonKind = 3
	PersonKindProducer  PersonKind = 4
	PersonKindGuestStar PersonKind = 5
	PersonKindComposer  PersonKind = 6
	PersonKindAuthor    PersonKind = 7
	PersonKindNarrator  PersonKind = 8
)

// String returns the Jellyfin-compatible type string for this PersonKind.
func (k PersonKind) String() string {
	switch k {
	case PersonKindActor:
		return "Actor"
	case PersonKindDirector:
		return "Director"
	case PersonKindWriter:
		return "Writer"
	case PersonKindProducer:
		return "Producer"
	case PersonKindGuestStar:
		return "GuestStar"
	case PersonKindComposer:
		return "Composer"
	case PersonKindAuthor:
		return "Author"
	case PersonKindNarrator:
		return "Narrator"
	default:
		return "Unknown"
	}
}

// PersonKindFromJob maps a crew job title to a PersonKind.
func PersonKindFromJob(job string) PersonKind {
	switch strings.ToLower(strings.TrimSpace(job)) {
	case "director":
		return PersonKindDirector
	case "writer", "screenplay", "story", "novel":
		return PersonKindWriter
	case "composer", "original music composer", "music":
		return PersonKindComposer
	default:
		return PersonKindProducer
	}
}

// Person represents a deduplicated person entity.
type Person struct {
	ID             int64
	Name           string
	SortName       string
	Bio            string
	BirthDate      *time.Time
	DeathDate      *time.Time
	Birthplace     string
	Homepage       string
	PhotoPath      string
	PhotoThumbhash string
	TmdbID         string
	ImdbID         string
	TvdbID         string
	PlexGUID       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ItemPerson represents a person's credit on a specific media item.
type ItemPerson struct {
	Person
	Kind      PersonKind
	Character string
	SortOrder int
}

// MediaItem represents a row in the media_items table.
type MediaItem struct {
	ContentID                    string // Sonyflake ID (PK)
	Type                         string // movie, series
	Title                        string
	SortTitle                    string
	DefaultMetadataLanguage      string
	OriginalTitle                string
	Year                         int
	Genres                       []string
	ContentRating                string // PG-13, TV-MA
	Runtime                      int    // minutes
	Overview                     string
	Tagline                      string
	RatingIMDB                   *float64
	RatingTMDB                   *float64
	RatingRTCritic               *int
	RatingRTAudience             *int
	ImdbID                       string
	TmdbID                       string
	TvdbID                       string
	PosterPath                   string // S3 path
	PosterThumbhash              string
	BackdropPath                 string
	BackdropThumbhash            string
	LogoPath                     string
	MetadataS3Path               string
	MetadataEtag                 string
	SeasonCount                  *int // series only
	Studios                      []string
	Networks                     []string
	Countries                    []string
	Keywords                     []string
	OriginalLanguage             string
	ReleaseDate                  *string // ISO date, nullable
	FirstAirDate                 *string // ISO date (series only), nullable
	LastAirDate                  *string // ISO date (series only), nullable
	AirTime                      *string // Series broadcast time (e.g. "20:00"), nullable
	AirTimezone                  *string // Series broadcast timezone (IANA name, e.g. "America/New_York"), nullable
	ShowStatus                   string  // Series lifecycle: "returning", "ended", "cancelled", "in_production", or "" if unknown (series only)
	People                       []ItemPerson
	MatchedAt                    *time.Time
	EpisodeMetadataIncomplete    bool
	EpisodeMetadataLastCheckedAt *time.Time
	LastRefreshed                *time.Time `json:"last_refreshed,omitempty"`
	RefreshFailures              int        `json:"refresh_failures"`
	LockedFields                 []int      `json:"locked_fields"`
	Status                       string     `json:"status"` // pending, matched, unmatched
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
	AddedAt                      *time.Time // populated by browse queries (MIN(mil.first_seen_at))
}

// Season represents a row in the seasons table.
type Season struct {
	ContentID               string
	SeriesID                string
	SeasonNumber            int
	Title                   string
	DefaultMetadataLanguage string
	Overview                string
	AirDate                 *time.Time
	PosterPath              string
	PosterThumbhash         string
	MetadataS3Path          string
	MetadataEtag            string
	MetadataSource          string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// Episode represents a row in the episodes table.
type Episode struct {
	ContentID               string // episode Sonyflake ID (PK)
	SeriesID                string
	SeasonID                string // FK to seasons.content_id
	SeasonNumber            int
	EpisodeNumber           int
	Title                   string
	DefaultMetadataLanguage string
	Overview                string
	AirDate                 *time.Time
	Runtime                 int // minutes
	RatingIMDB              *float64
	RatingTMDB              *float64
	ImdbID                  string
	TmdbID                  string
	TvdbID                  string
	StillPath               string
	StillThumbhash          string
	MetadataS3Path          string
	MetadataEtag            string
	MetadataSource          string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// MediaItemRoot represents a row in the media_item_roots table.
// It records which content_id owns a given canonical root path within a library folder.
type MediaItemRoot struct {
	MediaFolderID     int
	CanonicalRootPath string
	ContentID         string
	FirstSeenAt       time.Time
	LastSeenAt        time.Time
}

// MediaItemGroup represents a row in the media_item_groups table.
type MediaItemGroup struct {
	MediaFolderID   int
	GroupKeyVersion int
	ContentGroupKey string
	ContentID       string
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
}

// MediaItemLibrary represents a row in the media_item_libraries junction table.
type MediaItemLibrary struct {
	ContentID     string
	MediaFolderID int
	FirstSeenAt   time.Time
}

// EpisodeLibrary represents a row in the episode_libraries junction table.
type EpisodeLibrary struct {
	EpisodeID     string
	MediaFolderID int
	FirstSeenAt   time.Time
}

type MediaItemLocalization struct {
	ContentID         string
	Language          string
	Title             string
	SortTitle         string
	Overview          string
	Tagline           string
	PosterPath        string
	PosterThumbhash   string
	BackdropPath      string
	BackdropThumbhash string
	LogoPath          string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type SeasonLocalization struct {
	SeasonContentID string
	Language        string
	Title           string
	Overview        string
	PosterPath      string
	PosterThumbhash string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type EpisodeLocalization struct {
	EpisodeContentID string
	Language         string
	Title            string
	Overview         string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
