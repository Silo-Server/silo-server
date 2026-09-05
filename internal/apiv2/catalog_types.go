package apiv2

import (
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The catalog card. CatalogItem is the one summary of a media item every
// listing surface answers with: library sections, collection listings, and
// (in their sections) the catalog-items and catalog-home operations. A
// surface that knows less leaves the optional members absent; none of them
// invents a member of its own.

// CatalogItem is a media item as a browsing card shows it.
type CatalogItem struct {
	ContentID         string                    `json:"content_id" doc:"Deterministic catalog identifier" example:"movie:heat-1995"`
	PlayContentID     string                    `json:"play_content_id,omitempty" doc:"The item to play when the card is a series or season; absent when the item plays itself"`
	Type              string                    `json:"type" doc:"movie, series, season, episode, audiobook, ebook, podcast, podcast_episode" example:"movie"`
	Title             string                    `json:"title" example:"Heat"`
	SeriesID          string                    `json:"series_id,omitempty" doc:"Owning series of an episode or season"`
	SeriesTitle       string                    `json:"series_title,omitempty"`
	SeasonNumber      *int                      `json:"season_number,omitempty"`
	EpisodeNumber     *int                      `json:"episode_number,omitempty"`
	Year              int                       `json:"year,omitempty" example:"1995"`
	Runtime           int                       `json:"runtime,omitempty" doc:"Minutes" example:"170"`
	Genres            []string                  `json:"genres" doc:"Empty, never null"`
	Keywords          []string                  `json:"keywords" doc:"Empty, never null"`
	Studios           []string                  `json:"studios,omitempty"`
	Networks          []string                  `json:"networks,omitempty"`
	ContentRating     string                    `json:"content_rating,omitempty" example:"R"`
	Status            string                    `json:"status" doc:"Metadata match state of the item" example:"matched"`
	ShowStatus        string                    `json:"show_status,omitempty" doc:"Airing state of a series"`
	RatingIMDB        *float64                  `json:"rating_imdb,omitempty"`
	RatingTMDB        *float64                  `json:"rating_tmdb,omitempty"`
	RatingRTCritic    *int                      `json:"rating_rt_critic,omitempty"`
	RatingRTAudience  *int                      `json:"rating_rt_audience,omitempty"`
	OriginalLanguage  string                    `json:"original_language,omitempty" example:"en"`
	Overview          string                    `json:"overview,omitempty"`
	ReleaseDate       *string                   `json:"release_date,omitempty" doc:"Calendar date, YYYY-MM-DD" example:"1995-12-15"`
	LastAirDate       *string                   `json:"last_air_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	AddedAt           *Instant                  `json:"added_at,omitempty"`
	PositionSeconds   *float64                  `json:"position_seconds,omitempty" doc:"Resume position on a continue-watching card"`
	DurationSeconds   *float64                  `json:"duration_seconds,omitempty"`
	ProgressUpdatedAt *Instant                  `json:"progress_updated_at,omitempty"`
	PosterURL         string                    `json:"poster_url,omitempty" doc:"Presigned, short-lived"`
	PosterThumbhash   string                    `json:"poster_thumbhash,omitempty"`
	BackdropURL       string                    `json:"backdrop_url,omitempty" doc:"Presigned, short-lived"`
	BackdropThumbhash string                    `json:"backdrop_thumbhash,omitempty"`
	LogoURL           string                    `json:"logo_url,omitempty" doc:"Presigned, short-lived"`
	MangaChapterCount *int                      `json:"manga_chapter_count,omitempty"`
	MangaVolumeCount  *int                      `json:"manga_volume_count,omitempty"`
	OverlaySummary    *CatalogItemOverlay       `json:"overlay_summary,omitempty" doc:"Technical badges of the best file"`
	Badges            []string                  `json:"badges,omitempty" doc:"Section-specific badges (new, returning, …)"`
	ItemSource        string                    `json:"item_source,omitempty" doc:"On a continue-watching card: in_progress or next_up"`
	SortMetrics       *CatalogItemSortMetrics   `json:"sort_metrics,omitempty" doc:"The values the listing was sorted by"`
	UserState         *CatalogItemUserState     `json:"user_state,omitempty" doc:"The viewer's flags; absent without a profile"`
	UpcomingEvent     *CatalogItemUpcomingEvent `json:"upcoming_event,omitempty"`
	WorkID            string                    `json:"work_id,omitempty" doc:"The work (book) an audiobook or ebook edition belongs to"`
	WorkTitle         string                    `json:"work_title,omitempty"`
	WorkFormats       []CatalogWorkFormat       `json:"work_formats,omitempty" doc:"Sibling editions of the same work"`
}

// CatalogItemOverlay is the technical summary a card overlays.
type CatalogItemOverlay struct {
	Resolution    string `json:"resolution,omitempty" example:"4K"`
	HDR           string `json:"hdr,omitempty" example:"Dolby Vision"`
	Audio         string `json:"audio,omitempty" example:"TrueHD"`
	AudioChannels string `json:"audio_channels,omitempty" example:"7.1"`
	VideoCodec    string `json:"video_codec,omitempty" example:"H.265"`
	Container     string `json:"container,omitempty" example:"MKV"`
	AspectRatio   string `json:"aspect_ratio,omitempty" example:"2.39:1"`
	ReleaseType   string `json:"release_type,omitempty"`
	Edition       string `json:"edition,omitempty" example:"Director's Cut"`
	MultiAudio    bool   `json:"multi_audio,omitempty" doc:"Two or more audio languages"`
	MultiSub      bool   `json:"multi_sub,omitempty" doc:"At least one subtitle track"`
}

// CatalogItemUserState is the viewer's flags on an item.
type CatalogItemUserState struct {
	Played      bool `json:"played" example:"false"`
	IsFavorite  bool `json:"is_favorite" example:"false"`
	InWatchlist bool `json:"in_watchlist" example:"false"`
}

// CatalogItemUpcomingEvent is the next airing a card announces.
type CatalogItemUpcomingEvent struct {
	Type          string   `json:"type" doc:"premiere, episode, finale, …" example:"episode"`
	AirDate       string   `json:"air_date" doc:"Calendar date, YYYY-MM-DD" example:"2026-01-09"`
	AirTime       *string  `json:"air_time,omitempty" doc:"Local wall-clock time, HH:MM"`
	EpisodeTitle  *string  `json:"episode_title,omitempty"`
	SeasonNumber  *int     `json:"season_number,omitempty"`
	EpisodeNumber *int     `json:"episode_number,omitempty"`
	Badges        []string `json:"badges" doc:"Empty, never null"`
}

// CatalogItemSortMetrics is what a sorted listing sorted on.
type CatalogItemSortMetrics struct {
	ReleaseDate    *string  `json:"release_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	RuntimeMinutes *int     `json:"runtime_minutes,omitempty"`
	Resolution     string   `json:"resolution,omitempty"`
	BitrateKbps    *int     `json:"bitrate_kbps,omitempty"`
	ProgressRatio  *float64 `json:"progress_ratio,omitempty"`
	ViewedAt       string   `json:"viewed_at,omitempty" doc:"As the progress store recorded it"`
	PlayCount      *int     `json:"play_count,omitempty"`
	Author         string   `json:"author,omitempty"`
	Narrator       string   `json:"narrator,omitempty"`
	SeriesName     string   `json:"series_name,omitempty"`
}

// CatalogWorkFormat is one edition of a work.
type CatalogWorkFormat struct {
	Type      string `json:"type" example:"audiobook"`
	ContentID string `json:"content_id" example:"audiobook:dune"`
	LibraryID ID     `json:"library_id,omitempty" doc:"Absent when the edition is not in a library"`
}

func catalogOverlayOf(o *models.OverlaySummary) *CatalogItemOverlay {
	if o == nil {
		return nil
	}
	return &CatalogItemOverlay{Resolution: o.Resolution, HDR: o.HDR, Audio: o.Audio, AudioChannels: o.AudioChannels, VideoCodec: o.VideoCodec,
		Container: o.Container, AspectRatio: o.AspectRatio, ReleaseType: o.ReleaseType, Edition: o.Edition, MultiAudio: o.MultiAudio, MultiSub: o.MultiSub}
}

func catalogUserStateOf(s *handlers.ItemUserStateView) *CatalogItemUserState {
	if s == nil {
		return nil
	}
	return &CatalogItemUserState{Played: s.Played, IsFavorite: s.IsFavorite, InWatchlist: s.InWatchlist}
}

// instantOfRFC3339 parses a stored RFC 3339 stamp; an unparseable one is
// absent rather than a broken member.
func instantOfRFC3339(s *string) *Instant {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return nil
	}
	i := NewInstant(t)
	return &i
}

// catalogItemOfSection renders a section card.
func catalogItemOfSection(v handlers.SectionItemView) CatalogItem {
	item := CatalogItem{
		ContentID: v.ContentID, PlayContentID: v.PlayContentID, Type: v.Type, Title: v.Title,
		SeriesID: v.SeriesID, SeriesTitle: v.SeriesTitle, SeasonNumber: v.SeasonNumber, EpisodeNumber: v.EpisodeNumber,
		Year: v.Year, Runtime: v.Runtime, Genres: NonNil(v.Genres), Keywords: NonNil(v.Keywords), Studios: v.Studios, Networks: v.Networks,
		ContentRating: v.ContentRating, Status: v.Status, ShowStatus: v.ShowStatus,
		RatingIMDB: v.RatingIMDB, RatingTMDB: v.RatingTMDB, RatingRTCritic: v.RatingRTCritic, RatingRTAudience: v.RatingRTAudience,
		OriginalLanguage: v.OriginalLanguage, Overview: v.Overview,
		PositionSeconds: v.PositionSeconds, DurationSeconds: v.DurationSeconds, ProgressUpdatedAt: instantOfRFC3339(v.ProgressUpdatedAt),
		PosterURL: v.PosterURL, PosterThumbhash: v.PosterThumbhash, BackdropURL: v.BackdropURL, BackdropThumbhash: v.BackdropThumbhash, LogoURL: v.LogoURL,
		OverlaySummary: catalogOverlayOf(v.OverlaySummary), Badges: v.Badges, ItemSource: v.ItemSource, UserState: catalogUserStateOf(v.UserState),
	}
	if e := v.UpcomingEvent; e != nil {
		item.UpcomingEvent = &CatalogItemUpcomingEvent{Type: e.Type, AirDate: e.AirDate, AirTime: e.AirTime, EpisodeTitle: e.EpisodeTitle,
			SeasonNumber: e.SeasonNumber, EpisodeNumber: e.EpisodeNumber, Badges: NonNil(e.Badges)}
	}
	return item
}

// catalogItemOfListing renders a listing (collection, browse) card.
func catalogItemOfListing(v handlers.CollectionItemView) CatalogItem {
	item := CatalogItem{
		ContentID: v.ContentID, PlayContentID: v.PlayContentID, Type: v.Type, Title: v.Title,
		SeriesID: v.SeriesID, SeriesTitle: v.SeriesTitle, SeasonNumber: v.SeasonNumber, EpisodeNumber: v.EpisodeNumber,
		Year: v.Year, Runtime: v.Runtime, Genres: NonNil(v.Genres), Keywords: NonNil(v.Keywords), Studios: v.Studios, Networks: v.Networks,
		ContentRating: v.ContentRating, Status: v.Status, ShowStatus: v.ShowStatus,
		RatingIMDB: v.RatingIMDB, RatingTMDB: v.RatingTMDB, RatingRTCritic: v.RatingRTCritic, RatingRTAudience: v.RatingRTAudience,
		OriginalLanguage: v.OriginalLanguage, Overview: v.Overview, ReleaseDate: v.ReleaseDate, LastAirDate: v.LastAirDate, AddedAt: instantPtr(v.AddedAt),
		PosterURL: v.PosterURL, PosterThumbhash: v.PosterThumbhash, BackdropURL: v.BackdropURL, BackdropThumbhash: v.BackdropThumbhash,
		MangaChapterCount: v.MangaChapterCount, MangaVolumeCount: v.MangaVolumeCount,
		OverlaySummary: catalogOverlayOf(v.OverlaySummary), UserState: catalogUserStateOf(v.UserState),
		WorkID: v.WorkID, WorkTitle: v.WorkTitle,
	}
	if m := v.SortMetrics; m != nil {
		item.SortMetrics = &CatalogItemSortMetrics{ReleaseDate: m.ReleaseDate, RuntimeMinutes: m.RuntimeMinutes, Resolution: m.Resolution, BitrateKbps: m.BitrateKbps,
			ProgressRatio: m.ProgressRatio, ViewedAt: m.ViewedAt, PlayCount: m.PlayCount, Author: m.Author, Narrator: m.Narrator, SeriesName: m.SeriesName}
	}
	for _, f := range v.WorkFormats {
		wf := CatalogWorkFormat{Type: f.Type, ContentID: f.ContentID}
		if f.LibraryID > 0 {
			wf.LibraryID = IDFromInt(int64(f.LibraryID))
		}
		item.WorkFormats = append(item.WorkFormats, wf)
	}
	return item
}
