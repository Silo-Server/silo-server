package userstore

import "time"

// Profile represents a user profile.
type Profile struct {
	ID                         string
	Name                       string
	Avatar                     string
	PINHash                    string
	IsChild                    bool
	IsPrimary                  bool
	MaxContentRating           string
	QualityPreference          string
	Language                   string
	SubtitleLanguage           string
	SubtitleMode               string
	AutoSkipIntro              bool
	AutoSkipCredits            bool
	AutoSkipRecap              bool
	AutoPlayNextPreview        bool
	ShowForcedSubtitles        bool
	LibraryRestrictionsEnabled bool
	AllowedLibraryIDs          []int
	MaxPlaybackQuality         string
	CreatedAt                  string
	UpdatedAt                  string
}

// DeviceSettingEntry is a server-managed setting override scoped to one
// durable app/device identity for one profile within a user.
type DeviceSettingEntry struct {
	ProfileID      string
	DeviceID       string
	DeviceName     string
	DevicePlatform string
	Key            string
	Value          string
	UpdatedAt      string
}

// UpdateProfileInput holds optional fields for updating a profile.
type UpdateProfileInput struct {
	Name                       *string
	Avatar                     *string
	PIN                        *string
	IsChild                    *bool
	MaxContentRating           *string
	QualityPreference          *string
	Language                   *string
	SubtitleLanguage           *string
	SubtitleMode               *string
	AutoSkipIntro              *bool
	AutoSkipCredits            *bool
	AutoSkipRecap              *bool
	AutoPlayNextPreview        *bool
	ShowForcedSubtitles        *bool
	LibraryRestrictionsEnabled *bool
	AllowedLibraryIDs          *[]int
	MaxPlaybackQuality         *string
}

// WatchProgress represents watch progress for a media item.
type WatchProgress struct {
	ProfileID       string
	MediaItemID     string
	PositionSeconds float64
	DurationSeconds float64
	Completed       bool
	UpdatedAt       string
	LastFileID      *int
	LastResolution  *string
	LastHDR         *bool
	LastCodecVideo  *string
	LastEditionKey  *string
}

const (
	HomeSurfaceContinueWatching = "continue_watching"
	HomeSurfaceNextUp           = "next_up"
)

type HomeItemDismissal struct {
	ProfileID         string
	Surface           string
	MediaItemID       string
	SeriesID          *string
	ProgressUpdatedAt *string
	DismissedAt       string
}

// VersionHints stores the file version traits from the last playback session.
type VersionHints struct {
	FileID     int
	Resolution string
	HDR        bool
	CodecVideo string
	EditionKey string
}

// WatchHistoryEntry represents a single watch history record.
type WatchHistorySource string

const (
	WatchHistorySourceLegacy      WatchHistorySource = "legacy"
	WatchHistorySourceManual      WatchHistorySource = "manual"
	WatchHistorySourcePlayback    WatchHistorySource = "playback"
	WatchHistorySourceImport      WatchHistorySource = "import"
	WatchHistorySourceJellycompat WatchHistorySource = "jellycompat"
	WatchHistorySourceTrakt       WatchHistorySource = "trakt"
	WatchHistorySourceSimkl       WatchHistorySource = "simkl"
	WatchHistorySourceMDBList     WatchHistorySource = "mdblist"
)

// WatchIdentity is the write-once archive payload stored with each history
// row so that StableIdentityResolver can re-resolve a content ID after a
// rescan or catalog rebind without hitting the catalog at query time.
type WatchIdentity struct {
	StableType        string            `json:"stable_type,omitempty"`
	ProviderIDs       map[string]string `json:"provider_ids,omitempty"`
	SeriesProviderIDs map[string]string `json:"series_provider_ids,omitempty"`
	Season            *int              `json:"season,omitempty"`
	Episode           *int              `json:"episode,omitempty"`
}

type WatchHistoryEntry struct {
	ID              string
	ProfileID       string
	MediaItemID     string
	WatchedAt       string
	DurationSeconds float64
	Completed       bool
	Source          WatchHistorySource
	Identity        WatchIdentity
}

type CompletedHistoryQuery struct {
	ProfileID      string
	MediaItemIDs   []string
	IncludeSources []WatchHistorySource
	ExcludeSources []WatchHistorySource
	Limit          int
	Offset         int
}

// Favorite represents a favorited media item.
type Favorite struct {
	ProfileID   string
	MediaItemID string
	AddedAt     string
}

// WatchlistEntry represents a watchlist item.
type WatchlistEntry struct {
	ProfileID   string
	MediaItemID string
	AddedAt     string
}

// Collection represents a user-created collection.
type Collection struct {
	ID                         string
	ProfileID                  string
	CreatorProfileID           string
	Name                       string
	Description                string
	CollectionType             string
	IsShared                   bool
	AllowedProfileIDs          []string
	QueryDefinition            string
	SortConfig                 string
	SourceURL                  string
	SourceConfig               string
	SyncSchedule               *string
	NextSyncAt                 *time.Time
	LastSyncAt                 *time.Time
	LastSyncStatus             string
	LastSyncMessage            string
	ItemCount                  int
	IncludeInServerCollections bool
	PosterURL                  string
	PosterThumbhash            string
	SortOrder                  int
	GroupID                    *string
	CreatedAt                  string
	UpdatedAt                  string
}

type GroupSortMode string

const (
	GroupSortManual    GroupSortMode = "manual"
	GroupSortNameAsc   GroupSortMode = "name_asc"
	GroupSortNameDesc  GroupSortMode = "name_desc"
	GroupSortRecent    GroupSortMode = "recent"
	GroupSortMostItems GroupSortMode = "most_items"
)

// CollectionGroup is a per-user bucket that groups personal collections in the
// gallery view. A nil group_id on a collection means it falls into the implicit
// "Ungrouped" section (no row in this table).
type CollectionGroup struct {
	ID              string
	Name            string
	Slug            string
	DefaultSortMode GroupSortMode
	SortOrder       int
	CreatedAt       string
	UpdatedAt       string
}

type CreateCollectionInput struct {
	CreatorProfileID           string
	Name                       string
	Description                string
	CollectionType             string
	IsShared                   bool
	AllowedProfileIDs          []string
	QueryDefinition            string
	SortConfig                 string
	SourceURL                  string
	SourceConfig               string
	SyncSchedule               *string
	NextSyncAt                 *time.Time
	IncludeInServerCollections bool
	PosterURL                  string
}

type UpdateCollectionInput struct {
	ID                         string
	RequestProfileID           string
	Name                       *string
	Description                *string
	IsShared                   *bool
	AllowedProfileIDs          *[]string
	QueryDefinition            *string
	SortConfig                 *string
	SourceURL                  *string
	SourceConfig               *string
	SyncSchedule               *string
	ClearSyncSchedule          bool
	NextSyncAt                 *time.Time
	ClearNextSyncAt            bool
	IncludeInServerCollections *bool
	PosterURL                  *string
	PosterThumbhash            *string
	GroupID                    **string
}

type UpdateCollectionSyncStateInput struct {
	ID         string
	Status     string
	Message    string
	ItemCount  int
	LastSyncAt time.Time
	NextSyncAt *time.Time
}

type CollectionItemReplacement struct {
	MediaItemID string
	Position    int
}

// CollectionItem represents an item within a collection.
type CollectionItem struct {
	CollectionID string
	MediaItemID  string
	Position     int
	AddedAt      string
}

// SettingEntry represents a key-value setting.
type SettingEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// SectionOverride represents a per-profile section customization.
type SectionOverride struct {
	ID          string
	ProfileID   string
	Scope       string
	LibraryID   string
	SectionID   string // empty for user-added sections
	Position    *int
	Hidden      bool
	Removed     bool
	SectionType string
	Title       string
	Featured    *bool
	ItemLimit   *int
	Config      string // JSON string
	CreatedAt   string
	UpdatedAt   string

	// IsUserAdded marks this override as a profile-built recipe instance
	// rather than a customization of an admin section. When true, SectionID
	// is empty and UserSectionType / UserConfig / UserTitle take precedence
	// over the legacy SectionType / Config / Title fields.
	IsUserAdded     bool
	UserSectionType string
	UserConfig      string // JSON string
	UserTitle       string
}

// SubtitlePreference stores per-series subtitle settings.
type SubtitlePreference struct {
	ProfileID              string                  `json:"profile_id"`
	SeriesID               string                  `json:"series_id"`
	SubtitleLanguage       string                  `json:"subtitle_language"`
	SubtitleTrackIndex     int                     `json:"subtitle_track_index"`
	ExternalSubtitlePath   string                  `json:"external_subtitle_path"`
	SubtitleMode           string                  `json:"subtitle_mode"`
	TrackSignature         *SubtitleTrackSignature `json:"track_signature,omitempty"`
	ShowForcedSubtitles    bool                    `json:"show_forced_subtitles"`
	HasShowForcedSubtitles bool                    `json:"-"`
	UpdatedAt              string                  `json:"updated_at"`
}

// AudioPreference stores per-series audio track settings.
type AudioPreference struct {
	ProfileID       string               `json:"profile_id"`
	SeriesID        string               `json:"series_id"`
	AudioTrackIndex int                  `json:"audio_track_index"`
	AudioLanguage   string               `json:"audio_language"`
	TrackSignature  *AudioTrackSignature `json:"track_signature,omitempty"`
	UpdatedAt       string               `json:"updated_at"`
}

// SeriesPlaybackPreference stores per-series file version traits.
type SeriesPlaybackPreference struct {
	ProfileID  string `json:"profile_id"`
	SeriesID   string `json:"series_id"`
	Resolution string `json:"resolution"`
	HDR        bool   `json:"hdr"`
	CodecVideo string `json:"codec_video"`
	UpdatedAt  string `json:"updated_at"`
}

// LibraryPlaybackPreference stores per-library playback settings.
type LibraryPlaybackPreference struct {
	ProfileID              string `json:"profile_id"`
	LibraryID              int    `json:"library_id"`
	AudioLanguage          string `json:"audio_language"`
	HasAudioLanguage       bool   `json:"-"`
	SubtitleLanguage       string `json:"subtitle_language"`
	HasSubtitleLanguage    bool   `json:"-"`
	SubtitleMode           string `json:"subtitle_mode"`
	HasSubtitleMode        bool   `json:"-"`
	ShowForcedSubtitles    bool   `json:"show_forced_subtitles"`
	HasShowForcedSubtitles bool   `json:"-"`
	UpdatedAt              string `json:"updated_at"`
}
