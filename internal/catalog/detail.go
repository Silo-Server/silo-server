package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/overlays"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// FileVersionFetcher retrieves media files linked to a content ID.
type FileVersionFetcher interface {
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error)
	GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error)
}

type PlaybackProbeEnsurer interface {
	Ensure(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error)
}

type ChapterThumbnailQueuer interface {
	QueueFileIDs(ctx context.Context, fileIDs []int)
	QueuePriorityFileIDs(ctx context.Context, fileIDs []int)
	QueuePriorityFileAtPosition(ctx context.Context, fileID int, targetSeconds float64)
}

// ImageResolver resolves image paths (potentially plugin-prefixed) to usable URLs.
type ImageResolver interface {
	// ResolveImageURL resolves a single image path. Plugin-prefixed paths (e.g.,
	// "metadb://images/abc/original.jpg") are resolved via the owning plugin's RPC.
	// HTTP(S) URLs pass through unchanged. Empty paths return "".
	// The variant parameter is a semantic size hint: "card", "featured", "full", "original".
	ResolveImageURL(ctx context.Context, path string, variant string) string

	// ResolveImageURLs resolves multiple image paths in a single call. Returns a
	// map from input path to resolved URL. The variant parameter applies to all paths.
	ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string
}

// ResolvedImageURL carries a resolved URL with optional validity metadata.
// A nil ExpiresAt means the URL has no known expiry and must not be stored in
// presign/plugin resolver caches that assume bounded validity.
type ResolvedImageURL struct {
	URL       string
	ExpiresAt *time.Time
}

type expiringImageResolver interface {
	ResolveImageURLWithExpiry(ctx context.Context, path string, variant string) ResolvedImageURL
	ResolveImageURLsWithExpiry(ctx context.Context, paths []string, variant string) map[string]ResolvedImageURL
}

// ItemDetail is the full detail response for a single media item, including
// metadata, file versions, subtitles, intro/credits markers, and presigned image URLs.
type ItemDetail struct {
	ContentID string `json:"content_id"`
	Type      string `json:"type"`

	// Metadata (served inline from Postgres).
	Title            string       `json:"title"`
	SortTitle        string       `json:"sort_title,omitempty"`
	OriginalTitle    string       `json:"original_title,omitempty"`
	Year             int          `json:"year,omitempty"`
	Overview         string       `json:"overview,omitempty"`
	Tagline          string       `json:"tagline,omitempty"`
	Runtime          int          `json:"runtime,omitempty"`
	ContentRating    string       `json:"content_rating,omitempty"`
	Genres           []string     `json:"genres"`
	RatingIMDB       *float64     `json:"rating_imdb,omitempty"`
	RatingTMDB       *float64     `json:"rating_tmdb,omitempty"`
	RatingRTCritic   *int         `json:"rating_rt_critic,omitempty"`
	RatingRTAudience *int         `json:"rating_rt_audience,omitempty"`
	ImdbID           string       `json:"imdb_id,omitempty"`
	TmdbID           string       `json:"tmdb_id,omitempty"`
	TvdbID           string       `json:"tvdb_id,omitempty"`
	Cast             []CastCredit `json:"cast"`
	Crew             []CrewCredit `json:"crew"`
	Studios          []string     `json:"studios"`
	Networks         []string     `json:"networks"`
	Countries        []string     `json:"countries,omitempty"`
	LockedFields     []int        `json:"locked_fields,omitempty"`
	FirstAirDate     *string      `json:"first_air_date,omitempty"`
	LastAirDate      *string      `json:"last_air_date,omitempty"`
	ReleaseDate      *string      `json:"release_date,omitempty"`
	AirTime          *string      `json:"air_time,omitempty"`
	AirTimezone      *string      `json:"air_timezone,omitempty"`
	ShowStatus       string       `json:"show_status,omitempty"`

	// Presigned image URLs.
	PosterURL         string `json:"poster_url,omitempty"`
	PosterThumbhash   string `json:"poster_thumbhash,omitempty"`
	BackdropURL       string `json:"backdrop_url,omitempty"`
	BackdropThumbhash string `json:"backdrop_thumbhash,omitempty"`
	LogoURL           string `json:"logo_url,omitempty"`

	// Series-specific.
	SeasonCount *int `json:"season_count,omitempty"`

	// Season-specific.
	SeriesID       string          `json:"series_id,omitempty"`
	SeriesTitle    string          `json:"series_title,omitempty"`
	SeasonNumber   *int            `json:"season_number,omitempty"`
	EpisodeNumber  *int            `json:"episode_number,omitempty"`
	EpisodeCount   *int            `json:"episode_count,omitempty"`
	AirDate        *string         `json:"air_date,omitempty"`
	IsSpecials     bool            `json:"is_specials,omitempty"`
	SeasonUserData *SeasonUserData `json:"user_data,omitempty"`
	UserState      *ItemUserState  `json:"user_state,omitempty"`
	UserRating     *int            `json:"user_rating,omitempty"`

	// File versions available for playback.
	Versions         []FileVersion     `json:"versions"`
	PlaybackVariants []PlaybackVariant `json:"playback_variants,omitempty"`

	// Root folder paths for series items (admin-only).
	FolderPaths []string `json:"folder_paths,omitempty"`

	// Compact overlay badges derived from the best available file.
	OverlaySummary *models.OverlaySummary `json:"overlay_summary,omitempty"`

	// Aggregated subtitles across all versions.
	Subtitles []SubtitleInfo `json:"subtitles"`

	// Intro/credits/recap/preview markers (from first file that has them).
	Intro   *Marker `json:"intro,omitempty"`
	Credits *Marker `json:"credits,omitempty"`
	Recap   *Marker `json:"recap,omitempty"`
	Preview *Marker `json:"preview,omitempty"`

	// Effective subtitle defaults for episode playback derived from
	// profile, library, and series-level preferences.
	EffectiveSubtitleLanguage       string                            `json:"-"`
	HasEffectiveSubtitleLang        bool                              `json:"-"`
	EffectiveSubtitleMode           string                            `json:"-"`
	HasEffectiveSubtitleMode        bool                              `json:"-"`
	EffectiveShowForcedSubtitles    bool                              `json:"-"`
	HasEffectiveShowForcedSubtitles bool                              `json:"-"`
	EffectiveSubtitleTrackSignature *userstore.SubtitleTrackSignature `json:"effective_subtitle_track_signature,omitempty"`

	// Effective version defaults for episode playback derived from
	// series-level sticky preferences.
	EffectiveVersionResolution *string `json:"effective_version_resolution,omitempty"`
	EffectiveVersionHDR        *bool   `json:"effective_version_hdr,omitempty"`
	EffectiveVersionCodecVideo *string `json:"effective_version_codec_video,omitempty"`
	EffectiveVersionEditionKey *string `json:"effective_version_edition_key,omitempty"`
}

// ItemUserState is per-profile viewer state included in item detail responses.
type ItemUserState struct {
	Played      bool `json:"played"`
	IsFavorite  bool `json:"is_favorite"`
	InWatchlist bool `json:"in_watchlist"`
}

// CastCredit is the item-detail API shape for a cast member.
type CastCredit struct {
	Name           string `json:"name"`
	Character      string `json:"character"`
	Order          int    `json:"order"`
	PersonID       string `json:"person_id,omitempty"`
	TmdbID         string `json:"tmdb_id,omitempty"`
	TvdbID         string `json:"tvdb_id,omitempty"`
	ImdbID         string `json:"imdb_id,omitempty"`
	PlexGUID       string `json:"plex_guid,omitempty"`
	PhotoURL       string `json:"photo_url,omitempty"`
	PhotoThumbhash string `json:"photo_thumbhash,omitempty"`
}

// CrewCredit is the item-detail API shape for a crew member.
type CrewCredit struct {
	Name           string `json:"name"`
	Job            string `json:"job"`
	PersonID       string `json:"person_id,omitempty"`
	TmdbID         string `json:"tmdb_id,omitempty"`
	TvdbID         string `json:"tvdb_id,omitempty"`
	ImdbID         string `json:"imdb_id,omitempty"`
	PlexGUID       string `json:"plex_guid,omitempty"`
	PhotoURL       string `json:"photo_url,omitempty"`
	PhotoThumbhash string `json:"photo_thumbhash,omitempty"`
}

// PersonCredit represents a person's credit on a media item for API responses.
type PersonCredit struct {
	PersonID       int64             `json:"person_id"`
	Name           string            `json:"name"`
	Kind           models.PersonKind `json:"kind"`
	Character      string            `json:"character,omitempty"`
	SortOrder      int               `json:"order"`
	TmdbID         string            `json:"tmdb_id,omitempty"`
	ImdbID         string            `json:"imdb_id,omitempty"`
	TvdbID         string            `json:"tvdb_id,omitempty"`
	PlexGUID       string            `json:"plex_guid,omitempty"`
	PhotoURL       string            `json:"photo_url,omitempty"`
	PhotoThumbhash string            `json:"photo_thumbhash,omitempty"`
}

// FileVersion represents a single file version available for playback.
type FileVersion struct {
	FileID                   int                    `json:"file_id"`
	FileName                 string                 `json:"file_name,omitempty"`
	FilePath                 string                 `json:"file_path,omitempty"`
	Resolution               string                 `json:"resolution"`
	CodecVideo               string                 `json:"codec_video"`
	CodecAudio               string                 `json:"codec_audio"`
	HDR                      bool                   `json:"hdr"`
	Container                string                 `json:"container"`
	FileSize                 int64                  `json:"file_size"`
	Duration                 int                    `json:"duration"`
	Bitrate                  int                    `json:"bitrate"`
	AddedAt                  time.Time              `json:"added_at"`
	EditionRaw               string                 `json:"edition_raw,omitempty"`
	EditionKey               string                 `json:"edition_key,omitempty"`
	PresentationKind         string                 `json:"presentation_kind,omitempty"`
	PresentationGroupKey     string                 `json:"presentation_group_key,omitempty"`
	PresentationPartIndex    int                    `json:"presentation_part_index,omitempty"`
	MultiEpisodeStart        int                    `json:"multi_episode_start,omitempty"`
	MultiEpisodeEnd          int                    `json:"multi_episode_end,omitempty"`
	EffectiveAudioTrackIndex *int                   `json:"effective_audio_track_index,omitempty"`
	EffectiveAudioLanguage   string                 `json:"effective_audio_language,omitempty"`
	VideoTracks              []models.VideoTrack    `json:"video_tracks,omitempty"`
	AudioTracks              []models.AudioTrack    `json:"audio_tracks,omitempty"`
	SubtitleTracks           []VersionSubtitleTrack `json:"subtitle_tracks,omitempty"`
	Chapters                 []VersionChapter       `json:"chapters,omitempty"`
	Intro                    *Marker                `json:"intro,omitempty"`
	Credits                  *Marker                `json:"credits,omitempty"`
	Recap                    *Marker                `json:"recap,omitempty"`
	Preview                  *Marker                `json:"preview,omitempty"`
}

// PlaybackVariant is one logical watch choice, optionally spanning multiple ordered parts.
type PlaybackVariant struct {
	VariantID            string                `json:"variant_id"`
	EditionRaw           string                `json:"edition_raw,omitempty"`
	EditionKey           string                `json:"edition_key,omitempty"`
	PresentationKind     string                `json:"presentation_kind,omitempty"`
	PresentationGroupKey string                `json:"presentation_group_key,omitempty"`
	PartCount            int                   `json:"part_count"`
	TotalDuration        int                   `json:"total_duration,omitempty"`
	DefaultFileID        int                   `json:"default_file_id,omitempty"`
	Parts                []PlaybackVariantPart `json:"parts"`
}

// PlaybackVariantPart contains the interchangeable versions for one ordered part.
type PlaybackVariantPart struct {
	PartIndex     int           `json:"part_index"`
	DefaultFileID int           `json:"default_file_id,omitempty"`
	TotalDuration int           `json:"total_duration,omitempty"`
	Versions      []FileVersion `json:"versions"`
}

// VersionChapter represents one chapter on a playable file version.
type VersionChapter struct {
	Index              int     `json:"index"`
	Title              string  `json:"title"`
	StartSeconds       float64 `json:"start_seconds"`
	EndSeconds         float64 `json:"end_seconds"`
	Source             string  `json:"source"`
	ThumbnailURL       string  `json:"thumbnail_url,omitempty"`
	ThumbnailThumbhash string  `json:"thumbnail_thumbhash,omitempty"`
}

// VersionSubtitleTrack represents one embedded or external subtitle track in a file version.
type VersionSubtitleTrack struct {
	Index           int    `json:"index,omitempty"`
	Language        string `json:"language,omitempty"`
	Codec           string `json:"codec,omitempty"`
	Title           string `json:"title,omitempty"`
	EmbeddedTitle   string `json:"embedded_title,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
	Forced          bool   `json:"forced"`
	Default         bool   `json:"default"`
	HearingImpaired bool   `json:"hearing_impaired"`
	External        bool   `json:"external"`
	FileName        string `json:"file_name,omitempty"`
}

// SubtitleInfo represents a subtitle track available for a media item.
type SubtitleInfo struct {
	Source          string `json:"source"` // embedded, external
	Language        string `json:"language"`
	Codec           string `json:"codec,omitempty"`
	Forced          bool   `json:"forced"`
	HearingImpaired bool   `json:"hearing_impaired"`
	Title           string `json:"title,omitempty"`
}

// Marker represents a time range (intro or credits).
type Marker struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// SeasonUserData is the profile-scoped progress payload shared across item,
// season, series, and episode surfaces. Aggregate fields are used for season
// and series responses; leaf fields are used for movies and episodes.
type SeasonUserData struct {
	PositionSeconds float64 `json:"position_seconds,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	IsInProgress    bool    `json:"is_in_progress,omitempty"`
	WatchedCount    int     `json:"watched_count"`
	UnplayedCount   int     `json:"unplayed_count"`
	InProgressCount int     `json:"in_progress_count"`
	Played          bool    `json:"played"`
	LastFileID      *int    `json:"last_file_id,omitempty"`
	LastResolution  *string `json:"last_resolution,omitempty"`
	LastHDR         *bool   `json:"last_hdr,omitempty"`
	LastCodecVideo  *string `json:"last_codec_video,omitempty"`
	LastEditionKey  *string `json:"last_edition_key,omitempty"`
}

// WatchDetail is the playback-oriented payload for /watch/{id}.
type WatchDetail struct {
	ContentID                       string                            `json:"content_id"`
	Type                            string                            `json:"type"`
	Title                           string                            `json:"title"`
	Year                            int                               `json:"year,omitempty"`
	Overview                        string                            `json:"overview,omitempty"`
	EffectiveSubtitleLanguage       string                            `json:"-"`
	HasEffectiveSubtitleLang        bool                              `json:"-"`
	EffectiveSubtitleMode           string                            `json:"-"`
	HasEffectiveSubtitleMode        bool                              `json:"-"`
	EffectiveShowForcedSubtitles    bool                              `json:"-"`
	HasEffectiveShowForcedSubtitles bool                              `json:"-"`
	Versions                        []FileVersion                     `json:"versions"`
	PlaybackVariants                []PlaybackVariant                 `json:"playback_variants,omitempty"`
	Subtitles                       []SubtitleInfo                    `json:"subtitles"`
	Intro                           *Marker                           `json:"intro,omitempty"`
	Credits                         *Marker                           `json:"credits,omitempty"`
	Recap                           *Marker                           `json:"recap,omitempty"`
	Preview                         *Marker                           `json:"preview,omitempty"`
	UserData                        *SeasonUserData                   `json:"user_data,omitempty"`
	SeriesID                        string                            `json:"series_id,omitempty"`
	SeriesTitle                     string                            `json:"series_title,omitempty"`
	SeasonNumber                    int                               `json:"season_number,omitempty"`
	EpisodeNumber                   int                               `json:"episode_number,omitempty"`
	EffectiveSubtitleTrackSignature *userstore.SubtitleTrackSignature `json:"effective_subtitle_track_signature,omitempty"`
	EffectiveVersionResolution      *string                           `json:"effective_version_resolution,omitempty"`
	EffectiveVersionHDR             *bool                             `json:"effective_version_hdr,omitempty"`
	EffectiveVersionCodecVideo      *string                           `json:"effective_version_codec_video,omitempty"`
	EffectiveVersionEditionKey      *string                           `json:"effective_version_edition_key,omitempty"`
}

func (d ItemDetail) MarshalJSON() ([]byte, error) {
	type itemDetailAlias ItemDetail
	payload := struct {
		itemDetailAlias
		EffectiveSubtitleLanguage    *string `json:"effective_subtitle_language,omitempty"`
		EffectiveSubtitleMode        *string `json:"effective_subtitle_mode,omitempty"`
		EffectiveShowForcedSubtitles *bool   `json:"effective_show_forced_subtitles,omitempty"`
	}{
		itemDetailAlias: itemDetailAlias(d),
	}
	if d.HasEffectiveSubtitleLang {
		payload.EffectiveSubtitleLanguage = &d.EffectiveSubtitleLanguage
	}
	if d.HasEffectiveSubtitleMode {
		payload.EffectiveSubtitleMode = &d.EffectiveSubtitleMode
	}
	if d.HasEffectiveShowForcedSubtitles {
		payload.EffectiveShowForcedSubtitles = &d.EffectiveShowForcedSubtitles
	}
	return json.Marshal(payload)
}

func (d WatchDetail) MarshalJSON() ([]byte, error) {
	type watchDetailAlias WatchDetail
	payload := struct {
		watchDetailAlias
		EffectiveSubtitleLanguage    *string `json:"effective_subtitle_language,omitempty"`
		EffectiveSubtitleMode        *string `json:"effective_subtitle_mode,omitempty"`
		EffectiveShowForcedSubtitles *bool   `json:"effective_show_forced_subtitles,omitempty"`
	}{
		watchDetailAlias: watchDetailAlias(d),
	}
	if d.HasEffectiveSubtitleLang {
		payload.EffectiveSubtitleLanguage = &d.EffectiveSubtitleLanguage
	}
	if d.HasEffectiveSubtitleMode {
		payload.EffectiveSubtitleMode = &d.EffectiveSubtitleMode
	}
	if d.HasEffectiveShowForcedSubtitles {
		payload.EffectiveShowForcedSubtitles = &d.EffectiveShowForcedSubtitles
	}
	return json.Marshal(payload)
}

type subtitleDefaults struct {
	Language       string
	HasLanguage    bool
	Mode           string
	HasMode        bool
	ShowForced     bool
	HasShowForced  bool
	TrackSignature *userstore.SubtitleTrackSignature
}

type versionDefaults struct {
	Resolution string
	HDR        bool
	CodecVideo string
	HasAny     bool
}

var ErrWatchTargetNotPlayable = errors.New("watch target is not directly playable")

// IsWatchTargetNotPlayable reports whether the error means the client sent a
// valid content ID that is not directly playable.
func IsWatchTargetNotPlayable(err error) bool {
	return errors.Is(err, ErrWatchTargetNotPlayable)
}

// DetailService builds full item detail responses with presigned URLs.
type DetailService struct {
	itemRepo       *ItemRepository
	episodeRepo    *EpisodeRepository
	seasonRepo     *SeasonRepository
	personRepo     *PersonRepository
	itemLocRepo    *MediaItemLocalizationRepository
	seasonLocRepo  *SeasonLocalizationRepository
	episodeLocRepo *EpisodeLocalizationRepository
	folderRepo     interface {
		GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
	}
	fileFetcher       FileVersionFetcher
	rootClaimRepo     *RootClaimRepository
	groupClaimRepo    *GroupClaimRepository
	imageResolver     ImageResolver
	userStoreProvider userstore.UserStoreProvider
	originalLangFn    func(context.Context, string) string
	probeEnsurer      PlaybackProbeEnsurer
	chapterThumbs     ChapterThumbnailQueuer
}

// NewDetailService creates a new DetailService.
func NewDetailService(
	itemRepo *ItemRepository,
	episodeRepo *EpisodeRepository,
	seasonRepo *SeasonRepository,
	personRepo *PersonRepository,
	fileFetcher FileVersionFetcher,
) *DetailService {
	return &DetailService{
		itemRepo:       itemRepo,
		episodeRepo:    episodeRepo,
		seasonRepo:     seasonRepo,
		personRepo:     personRepo,
		itemLocRepo:    NewMediaItemLocalizationRepository(itemRepo.pool),
		seasonLocRepo:  NewSeasonLocalizationRepository(itemRepo.pool),
		episodeLocRepo: NewEpisodeLocalizationRepository(itemRepo.pool),
		fileFetcher:    fileFetcher,
	}
}

// SetImageResolver sets the plugin-based image resolver for resolving
// plugin-prefixed image paths (e.g., "metadb://images/abc/original.jpg").
func (s *DetailService) SetImageResolver(resolver ImageResolver) {
	s.imageResolver = resolver
}

// SetUserStoreProvider wires in optional per-user preference lookups.
func (s *DetailService) SetUserStoreProvider(provider userstore.UserStoreProvider) {
	s.userStoreProvider = provider
}

func (s *DetailService) SetProbeEnsurer(ensurer PlaybackProbeEnsurer) {
	s.probeEnsurer = ensurer
}

func (s *DetailService) SetChapterThumbnailQueuer(queuer ChapterThumbnailQueuer) {
	s.chapterThumbs = queuer
}

func (s *DetailService) SetFolderRepository(repo interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
}) {
	s.folderRepo = repo
}

// SetRootClaimRepository wires in the root claim repo for series folder path lookups.
func (s *DetailService) SetRootClaimRepository(repo *RootClaimRepository) {
	s.rootClaimRepo = repo
}

func (s *DetailService) SetGroupClaimRepository(repo *GroupClaimRepository) {
	s.groupClaimRepo = repo
}

func cloneMediaItem(item *models.MediaItem) *models.MediaItem {
	if item == nil {
		return nil
	}
	cp := *item
	return &cp
}

func cloneSeason(season *models.Season) *models.Season {
	if season == nil {
		return nil
	}
	cp := *season
	return &cp
}

func cloneEpisode(ep *models.Episode) *models.Episode {
	if ep == nil {
		return nil
	}
	cp := *ep
	return &cp
}

func (s *DetailService) resolvePresentationLanguage(ctx context.Context, filter AccessFilter) (string, error) {
	if strings.TrimSpace(filter.PresentationLanguage) != "" {
		return strings.TrimSpace(filter.PresentationLanguage), nil
	}
	if filter.PresentationLibraryID == nil || s.folderRepo == nil {
		return "", nil
	}
	folder, err := s.folderRepo.GetByID(ctx, *filter.PresentationLibraryID)
	if err != nil {
		if errors.Is(err, ErrFolderNotFound) {
			return "", ErrItemNotFound
		}
		return "", err
	}
	return strings.TrimSpace(folder.MetadataLanguage), nil
}

func (s *DetailService) validatePresentationItemAccess(ctx context.Context, filter AccessFilter, contentID string) error {
	if filter.PresentationLibraryID == nil {
		return nil
	}
	membership, err := s.itemRepo.GetItemsInLibrary(ctx, []string{contentID}, *filter.PresentationLibraryID)
	if err != nil {
		return err
	}
	if !membership[contentID] {
		return ErrItemNotFound
	}
	return nil
}

func sameMetadataLanguage(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func (s *DetailService) LocalizeItemModel(ctx context.Context, item *models.MediaItem, filter AccessFilter) (*models.MediaItem, error) {
	if item == nil {
		return nil, nil
	}
	language, err := s.resolvePresentationLanguage(ctx, filter)
	if err != nil || language == "" || sameMetadataLanguage(item.DefaultMetadataLanguage, language) || s.itemLocRepo == nil {
		return cloneMediaItem(item), err
	}
	loc, err := s.itemLocRepo.Get(ctx, item.ContentID, language)
	if err != nil || loc == nil {
		return cloneMediaItem(item), err
	}
	localized := cloneMediaItem(item)
	localized.Title = loc.Title
	localized.SortTitle = loc.SortTitle
	localized.Overview = loc.Overview
	localized.Tagline = loc.Tagline
	localized.PosterPath = loc.PosterPath
	localized.PosterThumbhash = loc.PosterThumbhash
	localized.BackdropPath = loc.BackdropPath
	localized.BackdropThumbhash = loc.BackdropThumbhash
	localized.LogoPath = loc.LogoPath
	return localized, nil
}

func (s *DetailService) LocalizeSeasonModel(ctx context.Context, season *models.Season, filter AccessFilter) (*models.Season, error) {
	if season == nil {
		return nil, nil
	}
	language, err := s.resolvePresentationLanguage(ctx, filter)
	if err != nil || language == "" || sameMetadataLanguage(season.DefaultMetadataLanguage, language) || s.seasonLocRepo == nil {
		return cloneSeason(season), err
	}
	loc, err := s.seasonLocRepo.Get(ctx, season.ContentID, language)
	if err != nil || loc == nil {
		return cloneSeason(season), err
	}
	localized := cloneSeason(season)
	localized.Title = loc.Title
	localized.Overview = loc.Overview
	localized.PosterPath = loc.PosterPath
	localized.PosterThumbhash = loc.PosterThumbhash
	return localized, nil
}

func (s *DetailService) LocalizeEpisodeModel(ctx context.Context, episode *models.Episode, filter AccessFilter) (*models.Episode, error) {
	if episode == nil {
		return nil, nil
	}
	language, err := s.resolvePresentationLanguage(ctx, filter)
	if err != nil || language == "" || sameMetadataLanguage(episode.DefaultMetadataLanguage, language) || s.episodeLocRepo == nil {
		return cloneEpisode(episode), err
	}
	loc, err := s.episodeLocRepo.Get(ctx, episode.ContentID, language)
	if err != nil || loc == nil {
		return cloneEpisode(episode), err
	}
	localized := cloneEpisode(episode)
	localized.Title = loc.Title
	localized.Overview = loc.Overview
	return localized, nil
}

// GetItemDetail retrieves a full item detail with presigned URLs and file versions.
func (s *DetailService) GetItemDetail(ctx context.Context, contentID string, filter AccessFilter) (*ItemDetail, error) {
	item, err := s.itemRepo.GetByID(ctx, contentID)
	switch {
	case err == nil:
		if err := s.itemRepo.EnsureAccessible(ctx, contentID, filter); err != nil {
			return nil, err
		}
		if err := s.validatePresentationItemAccess(ctx, filter, contentID); err != nil {
			return nil, err
		}
		return s.buildMediaItemDetail(ctx, item, contentID, filter)
	case !errors.Is(err, ErrItemNotFound):
		return nil, err
	}

	if s.seasonRepo == nil {
		return nil, ErrItemNotFound
	}

	season, err := s.seasonRepo.GetByID(ctx, contentID)
	if err == nil {
		if err := s.itemRepo.EnsureAccessible(ctx, season.SeriesID, filter); err != nil {
			return nil, err
		}
		if err := s.validatePresentationItemAccess(ctx, filter, season.SeriesID); err != nil {
			return nil, err
		}
		return s.buildSeasonDetail(ctx, season, filter)
	} else if !errors.Is(err, ErrSeasonNotFound) {
		return nil, err
	}

	if s.episodeRepo == nil {
		return nil, ErrItemNotFound
	}

	episode, err := s.episodeRepo.GetByID(ctx, contentID)
	if err != nil {
		if errors.Is(err, ErrEpisodeNotFound) {
			return nil, ErrItemNotFound
		}
		return nil, err
	}
	if err := s.itemRepo.EnsureAccessible(ctx, episode.SeriesID, filter); err != nil {
		return nil, err
	}
	if err := s.validatePresentationItemAccess(ctx, filter, episode.ContentID); err != nil {
		return nil, err
	}
	seriesCtx, err := s.buildSeriesDetailContext(ctx, episode.SeriesID, filter)
	if err != nil {
		return nil, err
	}
	return s.buildEpisodeDetail(ctx, episode, seriesCtx, filter)
}

// seriesDetailContext caches series-level lookups so a batched episode-detail
// call doesn't redo them per episode.
type seriesDetailContext struct {
	series      *models.MediaItem
	castCredits []CastCredit
	crewCredits []CrewCredit
	versionPref versionDefaults
	backdropURL string
}

// buildSeriesDetailContext loads the parent series row, localizes it, fetches
// its credits, computes the user's series-level version preference, and
// presigns the backdrop URL — the work that buildEpisodeDetail used to do
// inline on every call. Hoisted into a helper so the batch path
// (GetEpisodeDetailsForSeries) can reuse one context across many episodes.
func (s *DetailService) buildSeriesDetailContext(ctx context.Context, seriesID string, filter AccessFilter) (*seriesDetailContext, error) {
	series, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		return nil, fmt.Errorf("loading parent series: %w", err)
	}
	series, err = s.LocalizeItemModel(ctx, series, filter)
	if err != nil {
		return nil, fmt.Errorf("localizing episode series detail: %w", err)
	}
	castCredits, crewCredits := s.fetchCredits(ctx, seriesID)
	return &seriesDetailContext{
		series:      series,
		castCredits: castCredits,
		crewCredits: crewCredits,
		versionPref: s.effectiveVersionDefaults(ctx, filter, seriesID),
		backdropURL: s.PresignImageURL(ctx, series.BackdropPath, "backdrop", ""),
	}, nil
}

// GetEpisodeDetailsForSeries returns ItemDetails for the requested episodes,
// hoisting series-level lookups so they run once per series instead of per
// episode. Used by the jellycompat /Shows/{id}/Episodes endpoint when clients
// request detail-level fields (Fields=MediaSources,MediaStreams,Chapters,
// People). Episodes that fail per-episode access checks are skipped silently.
func (s *DetailService) GetEpisodeDetailsForSeries(
	ctx context.Context,
	seriesID string,
	episodeContentIDs []string,
	filter AccessFilter,
) (map[string]*ItemDetail, error) {
	result := make(map[string]*ItemDetail, len(episodeContentIDs))
	if len(episodeContentIDs) == 0 || s.episodeRepo == nil {
		return result, nil
	}
	if err := s.itemRepo.EnsureAccessible(ctx, seriesID, filter); err != nil {
		return nil, err
	}
	seriesCtx, err := s.buildSeriesDetailContext(ctx, seriesID, filter)
	if err != nil {
		return nil, err
	}
	for _, contentID := range episodeContentIDs {
		episode, err := s.episodeRepo.GetByID(ctx, contentID)
		if err != nil {
			if errors.Is(err, ErrEpisodeNotFound) {
				continue
			}
			return nil, err
		}
		if err := s.validatePresentationItemAccess(ctx, filter, contentID); err != nil {
			if errors.Is(err, ErrItemNotFound) {
				continue
			}
			return nil, err
		}
		detail, err := s.buildEpisodeDetail(ctx, episode, seriesCtx, filter)
		if err != nil {
			// Skip this episode rather than failing the whole batch — the
			// caller falls back to list-mapping for any contentID missing
			// from the result map, matching the prior per-episode loop's
			// behaviour where one bad detail didn't break the series page.
			continue
		}
		result[contentID] = detail
	}
	return result, nil
}

// fetchCredits returns cast and crew credits for the given content ID.
func (s *DetailService) fetchCredits(ctx context.Context, contentID string) ([]CastCredit, []CrewCredit) {
	if s.personRepo == nil {
		return []CastCredit{}, []CrewCredit{}
	}
	people, err := s.personRepo.ListForItem(ctx, contentID)
	if err != nil {
		people = nil
	}
	credits := s.personCredits(ctx, people)
	return splitCastCrew(credits)
}

func (s *DetailService) buildMediaItemDetail(ctx context.Context, item *models.MediaItem, contentID string, filter AccessFilter) (*ItemDetail, error) {
	localizedItem, err := s.LocalizeItemModel(ctx, item, filter)
	if err != nil {
		return nil, fmt.Errorf("localizing item detail: %w", err)
	}
	item = localizedItem
	castCredits, crewCredits := s.fetchCredits(ctx, contentID)
	detail := &ItemDetail{
		ContentID:         item.ContentID,
		Type:              item.Type,
		Title:             item.Title,
		SortTitle:         item.SortTitle,
		OriginalTitle:     item.OriginalTitle,
		Year:              item.Year,
		Overview:          item.Overview,
		Tagline:           item.Tagline,
		Runtime:           item.Runtime,
		ContentRating:     item.ContentRating,
		Genres:            item.Genres,
		RatingIMDB:        item.RatingIMDB,
		RatingTMDB:        item.RatingTMDB,
		RatingRTCritic:    item.RatingRTCritic,
		RatingRTAudience:  item.RatingRTAudience,
		ImdbID:            item.ImdbID,
		TmdbID:            item.TmdbID,
		TvdbID:            item.TvdbID,
		Cast:              castCredits,
		Crew:              crewCredits,
		Studios:           item.Studios,
		Networks:          item.Networks,
		Countries:         item.Countries,
		LockedFields:      item.LockedFields,
		FirstAirDate:      item.FirstAirDate,
		LastAirDate:       item.LastAirDate,
		ReleaseDate:       item.ReleaseDate,
		AirTime:           item.AirTime,
		AirTimezone:       item.AirTimezone,
		ShowStatus:        item.ShowStatus,
		PosterThumbhash:   item.PosterThumbhash,
		BackdropThumbhash: item.BackdropThumbhash,
		SeasonCount:       item.SeasonCount,
		Versions:          []FileVersion{},
		PlaybackVariants:  []PlaybackVariant{},
		Subtitles:         []SubtitleInfo{},
	}

	// Resolve image URLs: full URLs (TVDB/TMDB) pass through; S3 cached base paths get
	// variant-resolved and presigned.
	detail.PosterURL = s.PresignImageURL(ctx, item.PosterPath, "poster", "")
	detail.BackdropURL = s.PresignImageURL(ctx, item.BackdropPath, "backdrop", "")
	detail.LogoURL = s.PresignImageURL(ctx, item.LogoPath, "logo", "")

	// File versions and subtitle aggregation only apply to movies.
	// For series, each episode file shares the series content_id, so
	// GetByContentID would return every episode — not alternate encodings.
	if item.Type != "series" {
		files, err := s.fileFetcher.GetByContentID(ctx, contentID)
		if err != nil {
			return nil, fmt.Errorf("fetching file versions: %w", err)
		}

		files = FilterMediaFilesByAccess(files, filter)
		files = s.preparePlaybackFiles(ctx, files)
		detail.Versions, detail.PlaybackVariants, detail.Subtitles, detail.Intro, detail.Credits, detail.Recap, detail.Preview = s.buildPlaybackInfo(
			ctx,
			files,
			filter,
			item.ContentID,
		)
		detail.OverlaySummary = overlays.BuildSummary(files)
	}

	// Series folder paths from confirmed claims when available, otherwise from
	// the file links that currently belong to the item. This keeps provisional
	// root-scoped series visible for manual match/admin flows without implying
	// confirmed cross-root ownership.
	if item.Type == "series" {
		if item.Status == "matched" {
			if s.groupClaimRepo != nil {
				paths, err := s.groupClaimRepo.ListObservedRootsByContentID(ctx, contentID)
				if err != nil {
					slog.WarnContext(ctx, "failed to fetch series group locations", "content_id", contentID, "error", err)
				} else if len(paths) > 0 {
					detail.FolderPaths = paths
				}
			}
			if len(detail.FolderPaths) == 0 && s.rootClaimRepo != nil {
				roots, err := s.rootClaimRepo.ListByContentID(ctx, contentID)
				if err != nil {
					slog.WarnContext(ctx, "failed to fetch series root claims", "content_id", contentID, "error", err)
				} else if len(roots) > 0 {
					paths := make([]string, len(roots))
					for i, root := range roots {
						paths[i] = root.CanonicalRootPath
					}
					detail.FolderPaths = paths
				}
			}
		}
		if len(detail.FolderPaths) == 0 && s.fileFetcher != nil {
			files, err := s.fileFetcher.GetByContentID(ctx, contentID)
			if err != nil {
				slog.WarnContext(ctx, "failed to fetch series files for folder paths", "content_id", contentID, "error", err)
			} else {
				detail.FolderPaths = seriesFolderPathsFromFiles(files)
			}
		}
	}

	return detail, nil
}

// personCredits converts ItemPerson slice to PersonCredit slice with presigned URLs.
func (s *DetailService) personCredits(ctx context.Context, people []models.ItemPerson) []PersonCredit {
	credits := make([]PersonCredit, 0, len(people))
	for _, p := range people {
		pc := PersonCredit{
			PersonID:  p.ID,
			Name:      p.Name,
			Kind:      p.Kind,
			Character: p.Character,
			SortOrder: p.SortOrder,
			TmdbID:    p.TmdbID,
			ImdbID:    p.ImdbID,
			TvdbID:    p.TvdbID,
			PlexGUID:  p.PlexGUID,
		}
		if p.PhotoPath != "" && p.PhotoPath != "-" {
			pc.PhotoURL = s.PresignURL(ctx, p.PhotoPath, "featured")
		}
		if p.PhotoThumbhash != "" && p.PhotoThumbhash != "-" {
			pc.PhotoThumbhash = p.PhotoThumbhash
		}
		credits = append(credits, pc)
	}
	return credits
}

// splitCastCrew splits PersonCredits into CastCredit and CrewCredit slices
// for backward-compatible API responses.
func splitCastCrew(credits []PersonCredit) ([]CastCredit, []CrewCredit) {
	var cast []CastCredit
	var crew []CrewCredit
	for _, pc := range credits {
		switch pc.Kind {
		case models.PersonKindActor, models.PersonKindGuestStar:
			cast = append(cast, CastCredit{
				Name:           pc.Name,
				Character:      pc.Character,
				Order:          pc.SortOrder,
				PersonID:       strconv.FormatInt(pc.PersonID, 10),
				TmdbID:         pc.TmdbID,
				ImdbID:         pc.ImdbID,
				TvdbID:         pc.TvdbID,
				PlexGUID:       pc.PlexGUID,
				PhotoURL:       pc.PhotoURL,
				PhotoThumbhash: pc.PhotoThumbhash,
			})
		default:
			crew = append(crew, CrewCredit{
				Name:           pc.Name,
				Job:            pc.Kind.String(),
				PersonID:       strconv.FormatInt(pc.PersonID, 10),
				TmdbID:         pc.TmdbID,
				ImdbID:         pc.ImdbID,
				TvdbID:         pc.TvdbID,
				PlexGUID:       pc.PlexGUID,
				PhotoURL:       pc.PhotoURL,
				PhotoThumbhash: pc.PhotoThumbhash,
			})
		}
	}
	if cast == nil {
		cast = []CastCredit{}
	}
	if crew == nil {
		crew = []CrewCredit{}
	}
	return cast, crew
}

func seriesFolderPathsFromFiles(files []*models.MediaFile) []string {
	if len(files) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(files))
	out := make([]string, 0, len(files))
	for _, file := range files {
		if file == nil {
			continue
		}
		path := strings.TrimSpace(file.ObservedRootPath)
		if path == "" {
			path = strings.TrimSpace(file.CanonicalRootPath)
		}
		if path == "" && strings.TrimSpace(file.FilePath) != "" {
			path = filepath.Dir(file.FilePath)
		}
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// clearSentinel returns "" for the no-photo sentinel, passing through real values.
func clearSentinel(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

func (s *DetailService) buildSeasonDetail(ctx context.Context, season *models.Season, filter AccessFilter) (*ItemDetail, error) {
	localizedSeason, err := s.LocalizeSeasonModel(ctx, season, filter)
	if err != nil {
		return nil, fmt.Errorf("localizing season detail: %w", err)
	}
	season = localizedSeason
	series, err := s.itemRepo.GetByID(ctx, season.SeriesID)
	if err != nil {
		return nil, fmt.Errorf("loading parent series: %w", err)
	}
	series, err = s.LocalizeItemModel(ctx, series, filter)
	if err != nil {
		return nil, fmt.Errorf("localizing season series detail: %w", err)
	}

	episodes := []*models.Episode{}
	if s.episodeRepo != nil {
		episodes, err = s.episodeRepo.ListBySeasonID(ctx, season.ContentID)
		if err != nil {
			return nil, fmt.Errorf("listing season episodes: %w", err)
		}
	}

	title := season.Title
	if title == "" {
		if season.SeasonNumber == 0 {
			title = "Specials"
		} else {
			title = fmt.Sprintf("Season %d", season.SeasonNumber)
		}
	}

	episodeCount := len(episodes)
	seasonNumber := season.SeasonNumber
	castCredits, crewCredits := s.fetchCredits(ctx, season.SeriesID)
	detail := &ItemDetail{
		ContentID:         season.ContentID,
		Type:              "season",
		Title:             title,
		Overview:          season.Overview,
		PosterThumbhash:   season.PosterThumbhash,
		BackdropThumbhash: series.BackdropThumbhash,
		SeriesID:          season.SeriesID,
		SeriesTitle:       series.Title,
		SeasonNumber:      &seasonNumber,
		EpisodeCount:      &episodeCount,
		IsSpecials:        season.SeasonNumber == 0,
		Cast:              castCredits,
		Crew:              crewCredits,
		Versions:          []FileVersion{},
		PlaybackVariants:  []PlaybackVariant{},
		Subtitles:         []SubtitleInfo{},
	}
	if season.AirDate != nil {
		airDate := season.AirDate.Format("2006-01-02")
		detail.AirDate = &airDate
	}

	detail.PosterURL = s.PresignImageURL(ctx, season.PosterPath, "poster", "")
	detail.BackdropURL = s.PresignImageURL(ctx, series.BackdropPath, "backdrop", "")
	return detail, nil
}

func (s *DetailService) buildEpisodeDetail(ctx context.Context, episode *models.Episode, seriesCtx *seriesDetailContext, filter AccessFilter) (*ItemDetail, error) {
	localizedEpisode, err := s.LocalizeEpisodeModel(ctx, episode, filter)
	if err != nil {
		return nil, fmt.Errorf("localizing episode detail: %w", err)
	}
	episode = localizedEpisode
	series := seriesCtx.series

	seasonNumber := episode.SeasonNumber
	episodeNumber := episode.EpisodeNumber
	detail := &ItemDetail{
		ContentID:         episode.ContentID,
		Type:              "episode",
		Title:             episode.Title,
		Overview:          episode.Overview,
		Runtime:           episode.Runtime,
		RatingIMDB:        episode.RatingIMDB,
		RatingTMDB:        episode.RatingTMDB,
		ImdbID:            episode.ImdbID,
		TmdbID:            episode.TmdbID,
		TvdbID:            episode.TvdbID,
		PosterThumbhash:   episode.StillThumbhash,
		BackdropThumbhash: series.BackdropThumbhash,
		SeriesID:          episode.SeriesID,
		SeriesTitle:       series.Title,
		SeasonNumber:      &seasonNumber,
		EpisodeNumber:     &episodeNumber,
		Cast:              seriesCtx.castCredits,
		Crew:              seriesCtx.crewCredits,
		Versions:          []FileVersion{},
		PlaybackVariants:  []PlaybackVariant{},
		Subtitles:         []SubtitleInfo{},
	}
	if episode.AirDate != nil {
		airDate := episode.AirDate.Format("2006-01-02")
		detail.AirDate = &airDate
	}
	if episode.Title == "" {
		detail.Title = fmt.Sprintf("Episode %d", episode.EpisodeNumber)
	}

	detail.PosterURL = s.PresignImageURL(ctx, episode.StillPath, "still", "")
	detail.BackdropURL = seriesCtx.backdropURL

	files, err := s.fileFetcher.GetByEpisodeID(ctx, episode.ContentID)
	if err != nil {
		return nil, fmt.Errorf("fetching file versions: %w", err)
	}
	files = FilterMediaFilesByAccess(files, filter)
	files = s.preparePlaybackFiles(ctx, files)
	detail.Versions, detail.PlaybackVariants, detail.Subtitles, detail.Intro, detail.Credits, detail.Recap, detail.Preview = s.buildPlaybackInfo(
		ctx,
		files,
		filter,
		episode.SeriesID,
	)
	detail.OverlaySummary = overlays.BuildSummary(files)
	defaults := s.effectiveSubtitleDefaults(ctx, filter, episode.SeriesID, files)
	detail.EffectiveSubtitleLanguage = defaults.Language
	detail.HasEffectiveSubtitleLang = defaults.HasLanguage
	detail.EffectiveSubtitleMode = defaults.Mode
	detail.HasEffectiveSubtitleMode = defaults.HasMode
	detail.EffectiveShowForcedSubtitles = defaults.ShowForced
	detail.HasEffectiveShowForcedSubtitles = defaults.HasShowForced
	detail.EffectiveSubtitleTrackSignature = defaults.TrackSignature
	if seriesCtx.versionPref.HasAny {
		if seriesCtx.versionPref.Resolution != "" {
			detail.EffectiveVersionResolution = stringPtr(seriesCtx.versionPref.Resolution)
		}
		detail.EffectiveVersionHDR = boolPtr(seriesCtx.versionPref.HDR)
		if seriesCtx.versionPref.CodecVideo != "" {
			detail.EffectiveVersionCodecVideo = stringPtr(seriesCtx.versionPref.CodecVideo)
		}
	}

	return detail, nil
}

// GetWatchDetail resolves a directly playable content ID into a normalized
// playback payload. Movies resolve by media item content ID; episodes resolve
// by episode content ID via their linked series and episode file records.
func (s *DetailService) GetWatchDetail(ctx context.Context, contentID string, filter AccessFilter) (*WatchDetail, error) {
	item, err := s.itemRepo.GetByID(ctx, contentID)
	switch {
	case err == nil:
		if err := s.itemRepo.EnsureAccessible(ctx, contentID, filter); err != nil {
			return nil, err
		}
		if err := s.validatePresentationItemAccess(ctx, filter, contentID); err != nil {
			return nil, err
		}
		item, err = s.LocalizeItemModel(ctx, item, filter)
		if err != nil {
			return nil, fmt.Errorf("localizing watch item: %w", err)
		}
		if item.Type == "series" {
			return nil, ErrWatchTargetNotPlayable
		}

		files, err := s.fileFetcher.GetByContentID(ctx, contentID)
		if err != nil {
			return nil, fmt.Errorf("fetching watch file versions: %w", err)
		}
		files = FilterMediaFilesByAccess(files, filter)
		files = s.preparePlaybackFiles(ctx, files)
		s.queueWatchPlaybackFiles(ctx, item.ContentID, item.Type, files)
		detail := s.newWatchDetail(
			ctx,
			item.ContentID,
			item.Type,
			item.Title,
			item.Overview,
			files,
			filter,
			item.ContentID,
		)
		detail.Year = item.Year
		defaults := s.effectiveSubtitleDefaults(ctx, filter, item.ContentID, files)
		detail.EffectiveSubtitleLanguage = defaults.Language
		detail.HasEffectiveSubtitleLang = defaults.HasLanguage
		detail.EffectiveSubtitleMode = defaults.Mode
		detail.HasEffectiveSubtitleMode = defaults.HasMode
		detail.EffectiveShowForcedSubtitles = defaults.ShowForced
		detail.HasEffectiveShowForcedSubtitles = defaults.HasShowForced
		detail.EffectiveSubtitleTrackSignature = defaults.TrackSignature
		return detail, nil
	case !errors.Is(err, ErrItemNotFound):
		return nil, err
	}

	if s.episodeRepo == nil {
		return nil, ErrItemNotFound
	}

	episode, err := s.episodeRepo.GetByID(ctx, contentID)
	if err != nil {
		return nil, err
	}
	if err := s.itemRepo.EnsureAccessible(ctx, episode.SeriesID, filter); err != nil {
		return nil, err
	}
	if err := s.validatePresentationItemAccess(ctx, filter, episode.ContentID); err != nil {
		return nil, err
	}
	episode, err = s.LocalizeEpisodeModel(ctx, episode, filter)
	if err != nil {
		return nil, fmt.Errorf("localizing episode watch detail: %w", err)
	}

	files, err := s.fileFetcher.GetByEpisodeID(ctx, episode.ContentID)
	if err != nil {
		return nil, fmt.Errorf("fetching episode watch file versions: %w", err)
	}

	files = FilterMediaFilesByAccess(files, filter)
	files = s.preparePlaybackFiles(ctx, files)
	s.queueWatchPlaybackFiles(ctx, episode.ContentID, "episode", files)
	detail := s.newWatchDetail(
		ctx,
		episode.ContentID,
		"episode",
		episode.Title,
		episode.Overview,
		files,
		filter,
		episode.SeriesID,
	)
	detail.SeasonNumber = episode.SeasonNumber
	detail.EpisodeNumber = episode.EpisodeNumber
	detail.SeriesID = episode.SeriesID
	defaults := s.effectiveSubtitleDefaults(ctx, filter, episode.SeriesID, files)
	detail.EffectiveSubtitleLanguage = defaults.Language
	detail.HasEffectiveSubtitleLang = defaults.HasLanguage
	detail.EffectiveSubtitleMode = defaults.Mode
	detail.HasEffectiveSubtitleMode = defaults.HasMode
	detail.EffectiveShowForcedSubtitles = defaults.ShowForced
	detail.HasEffectiveShowForcedSubtitles = defaults.HasShowForced
	detail.EffectiveSubtitleTrackSignature = defaults.TrackSignature
	if versionPref := s.effectiveVersionDefaults(ctx, filter, episode.SeriesID); versionPref.HasAny {
		if versionPref.Resolution != "" {
			detail.EffectiveVersionResolution = stringPtr(versionPref.Resolution)
		}
		detail.EffectiveVersionHDR = boolPtr(versionPref.HDR)
		if versionPref.CodecVideo != "" {
			detail.EffectiveVersionCodecVideo = stringPtr(versionPref.CodecVideo)
		}
	}

	if series, err := s.itemRepo.GetByID(ctx, episode.SeriesID); err == nil {
		series, err = s.LocalizeItemModel(ctx, series, filter)
		if err != nil {
			return nil, fmt.Errorf("localizing series watch detail: %w", err)
		}
		detail.SeriesTitle = series.Title
		detail.Year = series.Year
	}

	return detail, nil
}

func (s *DetailService) newWatchDetail(
	ctx context.Context,
	contentID,
	contentType,
	title,
	overview string,
	files []*models.MediaFile,
	filter AccessFilter,
	audioPreferenceContentID string,
) *WatchDetail {
	versions, playbackVariants, subtitles, intro, credits, recap, preview := s.buildPlaybackInfo(
		ctx,
		files,
		filter,
		audioPreferenceContentID,
	)
	return &WatchDetail{
		ContentID:        contentID,
		Type:             contentType,
		Title:            title,
		Overview:         overview,
		Versions:         versions,
		PlaybackVariants: playbackVariants,
		Subtitles:        subtitles,
		Intro:            intro,
		Credits:          credits,
		Recap:            recap,
		Preview:          preview,
	}
}

func (s *DetailService) effectiveSubtitleDefaults(
	ctx context.Context,
	filter AccessFilter,
	seriesID string,
	files []*models.MediaFile,
) subtitleDefaults {
	defaults := subtitleDefaults{}

	if s.userStoreProvider == nil || filter.UserID == 0 || filter.ProfileID == "" {
		return defaults
	}

	store, err := s.userStoreProvider.ForUser(ctx, filter.UserID)
	if err != nil || store == nil {
		return defaults
	}

	if profile, err := store.GetProfile(ctx, filter.ProfileID); err == nil && profile != nil {
		defaults.Language = profile.SubtitleLanguage
		defaults.Mode = profile.SubtitleMode
		defaults.ShowForced = profile.ShowForcedSubtitles
		defaults.HasLanguage = true
		defaults.HasMode = true
		defaults.HasShowForced = true
	}

	if libraryID := preferredPlayableLibraryID(files, filter.SelectedFileID); libraryID > 0 {
		if pref, err := store.GetLibraryPlaybackPreference(ctx, filter.ProfileID, libraryID); err == nil && pref != nil {
			if pref.HasSubtitleLanguage {
				defaults.Language = pref.SubtitleLanguage
				defaults.HasLanguage = true
			}
			if pref.HasSubtitleMode {
				defaults.Mode = pref.SubtitleMode
				defaults.HasMode = true
			}
			if pref.HasShowForcedSubtitles {
				defaults.ShowForced = pref.ShowForcedSubtitles
				defaults.HasShowForced = true
			}
		}
	}

	if seriesID != "" {
		if pref, err := store.GetSubtitlePreference(ctx, filter.ProfileID, seriesID); err == nil && pref != nil {
			defaults.Language = pref.SubtitleLanguage
			defaults.HasLanguage = true
			if pref.SubtitleMode != "" {
				defaults.Mode = pref.SubtitleMode
				defaults.HasMode = true
			}
			if pref.HasShowForcedSubtitles {
				defaults.ShowForced = pref.ShowForcedSubtitles
				defaults.HasShowForced = true
			}
			if pref.TrackSignature != nil && !pref.TrackSignature.IsZero() {
				defaults.TrackSignature = pref.TrackSignature
			}
		}
	}

	return defaults
}

func (s *DetailService) effectiveVersionDefaults(
	ctx context.Context,
	filter AccessFilter,
	seriesID string,
) versionDefaults {
	defaults := versionDefaults{}

	if seriesID == "" || s.userStoreProvider == nil || filter.UserID == 0 || filter.ProfileID == "" {
		return defaults
	}

	store, err := s.userStoreProvider.ForUser(ctx, filter.UserID)
	if err != nil || store == nil {
		return defaults
	}

	pref, err := store.GetSeriesPlaybackPreference(ctx, filter.ProfileID, seriesID)
	if err != nil || pref == nil {
		return defaults
	}

	defaults.Resolution = pref.Resolution
	defaults.HDR = pref.HDR
	defaults.CodecVideo = pref.CodecVideo
	defaults.HasAny = pref.Resolution != "" || pref.CodecVideo != "" || pref.HDR
	return defaults
}

func (s *DetailService) resolveOriginalLanguage(ctx context.Context, contentID string) string {
	if s != nil && s.originalLangFn != nil {
		return strings.TrimSpace(s.originalLangFn(ctx, contentID))
	}
	if s == nil || s.itemRepo == nil || strings.TrimSpace(contentID) == "" {
		return ""
	}
	lang, err := s.itemRepo.GetOriginalLanguage(ctx, contentID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(lang)
}

type effectiveAudioSelection struct {
	Index    int
	Language string
}

func resolveSelectedAudioLanguage(
	file *models.MediaFile,
	index int,
	originalLanguage string,
	useOriginalFallback bool,
) string {
	if file != nil && index >= 0 && index < len(file.AudioTracks) {
		if language := strings.TrimSpace(file.AudioTracks[index].Language); language != "" {
			return language
		}
	}
	if useOriginalFallback {
		return strings.TrimSpace(originalLanguage)
	}
	return ""
}

func (s *DetailService) effectiveAudioSelection(
	ctx context.Context,
	filter AccessFilter,
	audioPreferenceContentID string,
	file *models.MediaFile,
) effectiveAudioSelection {
	if file == nil || len(file.AudioTracks) == 0 {
		return effectiveAudioSelection{}
	}
	if s.userStoreProvider == nil || filter.UserID == 0 || filter.ProfileID == "" {
		index := playback.SelectAudioTrack(file.AudioTracks, "", nil)
		return effectiveAudioSelection{
			Index:    index,
			Language: resolveSelectedAudioLanguage(file, index, "", false),
		}
	}

	store, err := s.userStoreProvider.ForUser(ctx, filter.UserID)
	if err != nil || store == nil {
		index := playback.SelectAudioTrack(file.AudioTracks, "", nil)
		return effectiveAudioSelection{
			Index:    index,
			Language: resolveSelectedAudioLanguage(file, index, "", false),
		}
	}

	var seriesPref *playback.AudioTrackPreference
	if strings.TrimSpace(audioPreferenceContentID) != "" {
		if pref, prefErr := store.GetAudioPreference(ctx, filter.ProfileID, audioPreferenceContentID); prefErr == nil && pref != nil {
			seriesPref = &playback.AudioTrackPreference{
				AudioTrackIndex: pref.AudioTrackIndex,
				AudioLanguage:   pref.AudioLanguage,
				TrackSignature:  pref.TrackSignature,
			}
		}
	}

	preferredLang := ""
	if profile, profileErr := store.GetProfile(ctx, filter.ProfileID); profileErr == nil && profile != nil {
		preferredLang = strings.TrimSpace(profile.Language)
	}

	libraryAudioLang := ""
	if seriesPref == nil {
		if pref, prefErr := store.GetLibraryPlaybackPreference(ctx, filter.ProfileID, file.MediaFolderID); prefErr == nil && pref != nil {
			libraryAudioLang = strings.TrimSpace(pref.AudioLanguage)
		}
	}

	originalLanguage := ""
	resolveOriginalLanguage := func() string {
		if originalLanguage == "" {
			originalLanguage = s.resolveOriginalLanguage(ctx, audioPreferenceContentID)
		}
		return originalLanguage
	}

	seriesUsesOriginal := seriesPref != nil && seriesPref.AudioLanguage == playback.OriginalLanguageSentinel
	profileUsesOriginal := preferredLang == playback.OriginalLanguageSentinel
	libraryUsesOriginal := libraryAudioLang == playback.OriginalLanguageSentinel

	if seriesUsesOriginal {
		seriesPref.AudioLanguage = resolveOriginalLanguage()
	}
	if profileUsesOriginal {
		preferredLang = resolveOriginalLanguage()
	}
	if libraryUsesOriginal {
		libraryAudioLang = resolveOriginalLanguage()
	}
	if libraryAudioLang != "" {
		preferredLang = libraryAudioLang
	}

	useOriginalFallback := seriesUsesOriginal ||
		(libraryUsesOriginal && libraryAudioLang != "") ||
		(profileUsesOriginal && libraryAudioLang == "")

	index := playback.SelectAudioTrack(file.AudioTracks, preferredLang, seriesPref)
	return effectiveAudioSelection{
		Index:    index,
		Language: resolveSelectedAudioLanguage(file, index, originalLanguage, useOriginalFallback),
	}
}

func (s *DetailService) effectiveAudioTrackIndex(
	ctx context.Context,
	filter AccessFilter,
	audioPreferenceContentID string,
	file *models.MediaFile,
) int {
	return s.effectiveAudioSelection(ctx, filter, audioPreferenceContentID, file).Index
}

func stringPtr(value string) *string {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func intPtr(value int) *int {
	return &value
}

func preferredPlayableLibraryID(files []*models.MediaFile, selectedFileID int) int {
	if selectedFileID > 0 {
		for _, file := range files {
			if file != nil && file.ID == selectedFileID {
				return file.MediaFolderID
			}
		}
	}

	best := bestPlayableFile(files)
	if best == nil {
		return 0
	}
	return best.MediaFolderID
}

func bestPlayableFile(files []*models.MediaFile) *models.MediaFile {
	var best *models.MediaFile
	for _, file := range files {
		if file == nil {
			continue
		}
		if best == nil {
			best = file
			continue
		}
		switch access.CompareQuality(file.Resolution, best.Resolution) {
		case 1:
			best = file
		case 0:
			if file.FileSize < best.FileSize {
				best = file
			}
		}
	}
	return best
}

// markerFromRange converts a (start, end) pair into a *Marker, returning nil
// when either bound is missing. Used to lift the four per-file segment kinds
// (intro/credits/recap/preview) into the api response shape.
func markerFromRange(start, end *float64) *Marker {
	if start == nil || end == nil {
		return nil
	}
	return &Marker{Start: *start, End: *end}
}

func (s *DetailService) buildPlaybackInfo(
	ctx context.Context,
	files []*models.MediaFile,
	filter AccessFilter,
	audioPreferenceContentID string,
) ([]FileVersion, []PlaybackVariant, []SubtitleInfo, *Marker, *Marker, *Marker, *Marker) {
	versions := make([]FileVersion, 0, len(files))
	subtitleSet := make(map[string]SubtitleInfo)
	var firstIntro, firstCredits, firstRecap, firstPreview *Marker

	for _, f := range files {
		if f == nil {
			continue
		}
		effectiveAudioSelection := s.effectiveAudioSelection(
			ctx,
			filter,
			audioPreferenceContentID,
			f,
		)
		versionIntro := markerFromRange(f.IntroStart, f.IntroEnd)
		if versionIntro != nil && firstIntro == nil {
			firstIntro = versionIntro
		}
		versionCredits := markerFromRange(f.CreditsStart, f.CreditsEnd)
		if versionCredits != nil && firstCredits == nil {
			firstCredits = versionCredits
		}
		versionRecap := markerFromRange(f.RecapStart, f.RecapEnd)
		if versionRecap != nil && firstRecap == nil {
			firstRecap = versionRecap
		}
		versionPreview := markerFromRange(f.PreviewStart, f.PreviewEnd)
		if versionPreview != nil && firstPreview == nil {
			firstPreview = versionPreview
		}

		versions = append(versions, FileVersion{
			FileID:                   f.ID,
			FileName:                 filepath.Base(f.FilePath),
			FilePath:                 f.FilePath,
			Resolution:               f.Resolution,
			CodecVideo:               f.CodecVideo,
			CodecAudio:               f.CodecAudio,
			HDR:                      f.HDR,
			Container:                f.Container,
			FileSize:                 f.FileSize,
			Duration:                 f.Duration,
			Bitrate:                  f.Bitrate,
			AddedAt:                  f.CreatedAt,
			EditionRaw:               f.EditionRaw,
			EditionKey:               f.EditionKey,
			PresentationKind:         f.PresentationKind,
			PresentationGroupKey:     f.PresentationGroupKey,
			PresentationPartIndex:    f.PresentationPartIndex,
			MultiEpisodeStart:        f.MultiEpisodeStart,
			MultiEpisodeEnd:          f.MultiEpisodeEnd,
			EffectiveAudioTrackIndex: intPtr(effectiveAudioSelection.Index),
			EffectiveAudioLanguage:   effectiveAudioSelection.Language,
			VideoTracks:              append([]models.VideoTrack(nil), f.VideoTracks...),
			AudioTracks:              append([]models.AudioTrack(nil), f.AudioTracks...),
			SubtitleTracks:           buildVersionSubtitleTracks(f),
			Chapters:                 s.buildVersionChapters(ctx, f),
			Intro:                    versionIntro,
			Credits:                  versionCredits,
			Recap:                    versionRecap,
			Preview:                  versionPreview,
		})

		for _, sub := range f.SubtitleTracks {
			key := fmt.Sprintf("embedded:%s:%s:%v:%v", sub.Language, sub.Codec, sub.Forced, sub.HearingImpaired)
			if _, exists := subtitleSet[key]; !exists {
				subtitleSet[key] = SubtitleInfo{
					Source:          "embedded",
					Language:        sub.Language,
					Codec:           sub.Codec,
					Forced:          sub.Forced,
					HearingImpaired: sub.HearingImpaired,
					Title:           sub.Title,
				}
			}
		}

		for _, sub := range f.ExternalSubtitles {
			key := fmt.Sprintf("external:%s:%s:%v:%v", sub.Language, sub.Format, sub.Forced, sub.HearingImpaired)
			if _, exists := subtitleSet[key]; !exists {
				subtitleSet[key] = SubtitleInfo{
					Source:          "external",
					Language:        sub.Language,
					Codec:           sub.Format,
					Forced:          sub.Forced,
					HearingImpaired: sub.HearingImpaired,
				}
			}
		}

	}

	subtitles := make([]SubtitleInfo, 0, len(subtitleSet))
	for _, sub := range subtitleSet {
		subtitles = append(subtitles, sub)
	}

	variants := buildPlaybackVariants(versions, filter.SelectedFileID)
	selectedVersionExists := playbackVersionExists(versions, filter.SelectedFileID)
	pick := func(field func(v FileVersion) *Marker, fallback *Marker) *Marker {
		m := selectedPlaybackMarker(versions, variants, filter.SelectedFileID, field)
		if m == nil && !selectedVersionExists {
			return fallback
		}
		return m
	}
	intro := pick(func(v FileVersion) *Marker { return v.Intro }, firstIntro)
	credits := pick(func(v FileVersion) *Marker { return v.Credits }, firstCredits)
	recap := pick(func(v FileVersion) *Marker { return v.Recap }, firstRecap)
	preview := pick(func(v FileVersion) *Marker { return v.Preview }, firstPreview)

	return versions, variants, subtitles, intro, credits, recap, preview
}

func playbackVersionExists(versions []FileVersion, selectedFileID int) bool {
	if selectedFileID <= 0 {
		return false
	}
	for _, version := range versions {
		if version.FileID == selectedFileID {
			return true
		}
	}
	return false
}

func selectedPlaybackMarker(
	versions []FileVersion,
	variants []PlaybackVariant,
	selectedFileID int,
	marker func(FileVersion) *Marker,
) *Marker {
	if marker == nil {
		return nil
	}
	if selectedFileID > 0 {
		for _, version := range versions {
			if version.FileID == selectedFileID {
				return marker(version)
			}
		}
	}
	for _, variant := range variants {
		if variant.DefaultFileID <= 0 {
			continue
		}
		for _, version := range versions {
			if version.FileID == variant.DefaultFileID {
				if matched := marker(version); matched != nil {
					return matched
				}
				break
			}
		}
	}
	return nil
}

func buildPlaybackVariants(versions []FileVersion, selectedFileID int) []PlaybackVariant {
	if len(versions) == 0 {
		return []PlaybackVariant{}
	}

	type partBucket struct {
		PartIndex int
		Versions  []FileVersion
	}
	type variantBucket struct {
		EditionRaw           string
		EditionKey           string
		PresentationKind     string
		PresentationGroupKey string
		Parts                map[int]*partBucket
	}

	byVariant := make(map[string]*variantBucket)
	order := make([]string, 0)

	for _, version := range versions {
		variantID := playbackVariantID(version)
		bucket, ok := byVariant[variantID]
		if !ok {
			bucket = &variantBucket{
				EditionRaw:           version.EditionRaw,
				EditionKey:           version.EditionKey,
				PresentationKind:     version.PresentationKind,
				PresentationGroupKey: version.PresentationGroupKey,
				Parts:                map[int]*partBucket{},
			}
			byVariant[variantID] = bucket
			order = append(order, variantID)
		}

		partIndex := version.PresentationPartIndex
		if partIndex <= 0 {
			partIndex = 1
		}
		part, ok := bucket.Parts[partIndex]
		if !ok {
			part = &partBucket{PartIndex: partIndex}
			bucket.Parts[partIndex] = part
		}
		part.Versions = append(part.Versions, version)
	}

	variants := make([]PlaybackVariant, 0, len(order))
	for _, variantID := range order {
		bucket := byVariant[variantID]
		partIndexes := make([]int, 0, len(bucket.Parts))
		for partIndex := range bucket.Parts {
			partIndexes = append(partIndexes, partIndex)
		}
		sort.Ints(partIndexes)

		parts := make([]PlaybackVariantPart, 0, len(partIndexes))
		totalDuration := 0
		defaultFileID := 0
		for _, partIndex := range partIndexes {
			part := bucket.Parts[partIndex]
			sortFileVersions(part.Versions)
			defaultVersion := chooseDefaultVariantVersion(part.Versions, selectedFileID)
			if defaultVersion != nil {
				if defaultFileID == 0 {
					defaultFileID = defaultVersion.FileID
				}
			}
			partDuration := 0
			for _, version := range part.Versions {
				if version.Duration > partDuration {
					partDuration = version.Duration
				}
			}
			totalDuration += partDuration
			parts = append(parts, PlaybackVariantPart{
				PartIndex:     partIndex,
				DefaultFileID: fileIDOrZero(defaultVersion),
				TotalDuration: partDuration,
				Versions:      part.Versions,
			})
		}

		variants = append(variants, PlaybackVariant{
			VariantID:            variantID,
			EditionRaw:           bucket.EditionRaw,
			EditionKey:           bucket.EditionKey,
			PresentationKind:     bucket.PresentationKind,
			PresentationGroupKey: bucket.PresentationGroupKey,
			PartCount:            len(parts),
			TotalDuration:        totalDuration,
			DefaultFileID:        defaultFileID,
			Parts:                parts,
		})
	}

	return variants
}

func playbackVariantID(version FileVersion) string {
	editionKey := strings.TrimSpace(version.EditionKey)
	presentationKind := strings.TrimSpace(version.PresentationKind)
	groupKey := strings.TrimSpace(version.PresentationGroupKey)
	if editionKey == "" && presentationKind == "" && groupKey == "" {
		return "default"
	}
	return strings.Join([]string{editionKey, presentationKind, groupKey}, "|")
}

func sortFileVersions(versions []FileVersion) {
	sort.SliceStable(versions, func(i, j int) bool {
		a, b := versions[i], versions[j]
		switch access.CompareQuality(a.Resolution, b.Resolution) {
		case 1:
			return true
		case -1:
			return false
		}
		if a.HDR != b.HDR {
			return a.HDR
		}
		return a.FileSize > b.FileSize
	})
}

func chooseDefaultVariantVersion(versions []FileVersion, selectedFileID int) *FileVersion {
	if selectedFileID > 0 {
		for i := range versions {
			if versions[i].FileID == selectedFileID {
				return &versions[i]
			}
		}
	}
	if len(versions) == 0 {
		return nil
	}
	return &versions[0]
}

func fileIDOrZero(version *FileVersion) int {
	if version == nil {
		return 0
	}
	return version.FileID
}

func (s *DetailService) preparePlaybackFiles(ctx context.Context, files []*models.MediaFile) []*models.MediaFile {
	if len(files) == 0 {
		return files
	}

	prepared := make([]*models.MediaFile, 0, len(files))
	for _, file := range files {
		if file == nil {
			continue
		}
		if s.probeEnsurer != nil {
			ensured, err := s.probeEnsurer.Ensure(ctx, file)
			if err == nil && ensured != nil {
				file = ensured
			}
		}
		prepared = append(prepared, file)
	}

	return prepared
}

func (s *DetailService) queueWatchPlaybackFiles(
	ctx context.Context,
	contentID string,
	contentType string,
	files []*models.MediaFile,
) {
	if s == nil || s.chapterThumbs == nil || len(files) == 0 {
		return
	}

	fileIDs := make([]int, 0, len(files))
	for _, file := range files {
		if file == nil || file.ID <= 0 {
			continue
		}
		fileIDs = append(fileIDs, file.ID)
	}
	if len(fileIDs) == 0 {
		return
	}

	slog.Info(
		"queueing chapter thumbnails",
		"source",
		"watch_detail",
		"content_id",
		contentID,
		"content_type",
		contentType,
		"file_count",
		len(fileIDs),
	)
	s.chapterThumbs.QueueFileIDs(ctx, fileIDs)
}

func (s *DetailService) buildVersionChapters(ctx context.Context, file *models.MediaFile) []VersionChapter {
	if file == nil || len(file.Chapters) == 0 {
		return []VersionChapter{}
	}

	chapters := make([]VersionChapter, 0, len(file.Chapters))
	for _, chapter := range file.Chapters {
		ch := VersionChapter{
			Index:              chapter.Index,
			Title:              chapter.Title,
			StartSeconds:       chapter.StartSeconds,
			EndSeconds:         chapter.EndSeconds,
			Source:             chapter.Source,
			ThumbnailThumbhash: chapter.ThumbnailThumbhash,
		}
		if chapter.ThumbnailPath != "" {
			ch.ThumbnailURL = s.PresignURL(ctx, strings.Replace(chapter.ThumbnailPath, "/original.", "/w300.", 1), "card")
		}
		chapters = append(chapters, ch)
	}
	return chapters
}

func buildVersionSubtitleTracks(file *models.MediaFile) []VersionSubtitleTrack {
	tracks := make([]VersionSubtitleTrack, 0, len(file.SubtitleTracks)+len(file.ExternalSubtitles))
	for _, sub := range file.SubtitleTracks {
		tracks = append(tracks, VersionSubtitleTrack{
			Index:           sub.Index,
			Language:        sub.Language,
			Codec:           sub.Codec,
			Title:           sub.Title,
			EmbeddedTitle:   sub.EmbeddedTitle,
			Resolution:      sub.Resolution,
			Forced:          sub.Forced,
			Default:         sub.Default,
			HearingImpaired: sub.HearingImpaired,
			External:        sub.External,
			FileName:        sub.FileName,
		})
	}
	for _, sub := range file.ExternalSubtitles {
		tracks = append(tracks, VersionSubtitleTrack{
			Language:        sub.Language,
			Codec:           sub.Format,
			Title:           firstNonEmpty(sub.Title, filepath.Base(sub.Path)),
			EmbeddedTitle:   sub.EmbeddedTitle,
			Resolution:      sub.Resolution,
			Forced:          sub.Forced,
			Default:         sub.Default,
			HearingImpaired: sub.HearingImpaired,
			External:        true,
			FileName:        filepath.Base(sub.Path),
		})
	}
	return tracks
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// PresignURL resolves an image path to a usable URL:
//   - Empty/"-" → ""
//   - http:// or https:// → returned as-is (external provider URLs)
//   - {plugin_id}:// prefix → resolved via plugin's ResolveImageURL RPC
//   - Bare path (legacy) → logs warning and returns "" (no longer resolvable)
//
// The variant parameter is a semantic size hint forwarded to plugin resolvers:
// "card", "featured", "full", "original".
func (s *DetailService) PresignURL(ctx context.Context, path string, variant string) string {
	return s.PresignURLWithExpiry(ctx, path, variant).URL
}

// PresignURLWithExpiry resolves an image path and returns expiry metadata when
// the underlying resolver can state the resolved URL validity window.
func (s *DetailService) PresignURLWithExpiry(ctx context.Context, path string, variant string) ResolvedImageURL {
	if path == "" || path == "-" {
		return ResolvedImageURL{}
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return ResolvedImageURL{URL: path}
	}

	if s.imageResolver != nil {
		if resolver, ok := s.imageResolver.(expiringImageResolver); ok {
			return resolver.ResolveImageURLWithExpiry(ctx, path, variant)
		}
		return ResolvedImageURL{URL: s.imageResolver.ResolveImageURL(ctx, path, variant)}
	}

	slog.Warn("image path could not be resolved", "path", path)
	return ResolvedImageURL{}
}

// PresignImageURLs resolves multiple image paths to usable URLs with variant
// support while preserving the original input paths as lookup keys.
func (s *DetailService) PresignImageURLs(ctx context.Context, paths []string, imageType, size string) map[string]string {
	resolvedWithExpiry := s.PresignImageURLsWithExpiry(ctx, paths, imageType, size)
	resolved := make(map[string]string, len(resolvedWithExpiry))
	for path, value := range resolvedWithExpiry {
		resolved[path] = value.URL
	}
	return resolved
}

// PresignImageURLsWithExpiry resolves multiple image paths with image-type size
// normalization while preserving original input paths as lookup keys.
func (s *DetailService) PresignImageURLsWithExpiry(ctx context.Context, paths []string, imageType, size string) map[string]ResolvedImageURL {
	if len(paths) == 0 {
		return map[string]ResolvedImageURL{}
	}

	variant := sizeToVariant(size)
	normalizedPaths := make([]string, 0, len(paths))
	originalsByNormalized := make(map[string][]string, len(paths))
	for _, path := range paths {
		if path == "" || path == "-" {
			continue
		}

		normalized := path
		if !strings.HasPrefix(path, "http://") &&
			!strings.HasPrefix(path, "https://") &&
			!strings.Contains(path, "://") {
			normalized = cachedImageVariantPath(path, imageType, size)
		}

		if _, ok := originalsByNormalized[normalized]; !ok {
			normalizedPaths = append(normalizedPaths, normalized)
		}
		originalsByNormalized[normalized] = append(originalsByNormalized[normalized], path)
	}

	if len(normalizedPaths) == 0 {
		return map[string]ResolvedImageURL{}
	}

	resolvedByNormalized := s.PresignURLsWithExpiry(ctx, normalizedPaths, variant)

	resolved := make(map[string]ResolvedImageURL, len(paths))
	for _, normalized := range normalizedPaths {
		value, ok := resolvedByNormalized[normalized]
		if !ok || value.URL == "" {
			value = s.PresignURLWithExpiry(ctx, normalized, variant)
		}
		for _, original := range originalsByNormalized[normalized] {
			resolved[original] = value
		}
	}

	return resolved
}

// PresignURLsWithExpiry resolves already-normalized image paths in a single
// batch, preserving the input path as the lookup key.
func (s *DetailService) PresignURLsWithExpiry(ctx context.Context, paths []string, variant string) map[string]ResolvedImageURL {
	if len(paths) == 0 {
		return map[string]ResolvedImageURL{}
	}
	resolved := make(map[string]ResolvedImageURL, len(paths))
	resolverPaths := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || path == "-" {
			continue
		}
		if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
			resolved[path] = ResolvedImageURL{URL: path}
			continue
		}
		resolverPaths = append(resolverPaths, path)
	}
	if s.imageResolver != nil {
		if resolver, ok := s.imageResolver.(expiringImageResolver); ok {
			for path, value := range resolver.ResolveImageURLsWithExpiry(ctx, resolverPaths, variant) {
				resolved[path] = value
			}
		} else {
			for path, url := range s.imageResolver.ResolveImageURLs(ctx, resolverPaths, variant) {
				resolved[path] = ResolvedImageURL{URL: url}
			}
		}
	}
	for _, path := range resolverPaths {
		if _, ok := resolved[path]; ok {
			continue
		}
		if s.imageResolver == nil {
			resolved[path] = s.PresignURLWithExpiry(ctx, path, variant)
		}
	}
	return resolved
}

// sizeToVariant maps the existing S3 size hints used by the frontend to
// semantic variant names understood by plugins.
func sizeToVariant(size string) string {
	switch size {
	case "small":
		return "card"
	case "medium":
		return "featured"
	case "original":
		return "original"
	default: // "" (the default in most call sites)
		return "featured"
	}
}

// PresignImageURL resolves an image path to a usable URL with variant support.
// For cached base paths (bare S3 keys without a scheme), appends the appropriate
// variant filename and presigns the result. For http(s) URLs and plugin-prefixed
// paths, delegates to PresignURL with a mapped semantic variant.
func (s *DetailService) PresignImageURL(ctx context.Context, path, imageType, size string) string {
	return s.PresignImageURLWithExpiry(ctx, path, imageType, size).URL
}

// PresignImageURLWithExpiry resolves one image path using the same image type
// and size normalization as PresignImageURL, retaining URL expiry metadata.
func (s *DetailService) PresignImageURLWithExpiry(ctx context.Context, path, imageType, size string) ResolvedImageURL {
	if path == "" || path == "-" {
		return ResolvedImageURL{}
	}
	// Plugin-prefixed paths resolve via plugin with semantic variant.
	if strings.Contains(path, "://") &&
		!strings.HasPrefix(path, "http://") &&
		!strings.HasPrefix(path, "https://") {
		return s.PresignURLWithExpiry(ctx, path, sizeToVariant(size))
	}
	// HTTP URLs pass through.
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return ResolvedImageURL{URL: path}
	}

	return s.PresignURLWithExpiry(ctx, cachedImageVariantPath(path, imageType, size), sizeToVariant(size))
}

func cachedImageVariantPath(path, imageType, size string) string {
	if strings.Contains(path, "://") {
		return path
	}
	variant := cachedImageVariantKey(imageType, size)
	if variant == "" {
		return path
	}
	if strings.Contains(path, "/original.") {
		return strings.Replace(path, "/original.", "/"+variant+".", 1)
	}
	return path
}

func cachedImageVariantKey(imageType, size string) string {
	if size == "original" {
		return "original"
	}

	switch imageType {
	case "backdrop":
		if size == "small" {
			return "w300"
		}
		return "w1920"
	case "logo":
		return "w500"
	case "poster", "still":
		if size == "small" {
			return "w300"
		}
		return "w500"
	default:
		if size == "small" {
			return "w300"
		}
		return "w500"
	}
}
