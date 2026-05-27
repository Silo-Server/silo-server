package jellycompat

import (
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// upstream types represent the intermediate data model used between
// catalog/service layer and the Jellyfin DTO mapping layer.

type upstreamUserLibrary struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	PosterURL  string `json:"poster_url,omitempty"`
	PosterPath string `json:"-"`
}

type upstreamListItem struct {
	ContentID         string                  `json:"content_id"`
	Type              string                  `json:"type"`
	Title             string                  `json:"title"`
	SortTitle         string                  `json:"sort_title,omitempty"`
	Year              int                     `json:"year"`
	Genres            []string                `json:"genres"`
	ContentRating     string                  `json:"content_rating"`
	Status            string                  `json:"status"`
	RatingIMDB        *float64                `json:"rating_imdb"`
	Overview          string                  `json:"overview"`
	PosterURL         string                  `json:"poster_url"`
	BackdropURL       string                  `json:"backdrop_url"`
	PosterPath        string                  `json:"-"`
	PosterThumbhash   string                  `json:"-"`
	BackdropPath      string                  `json:"-"`
	BackdropThumbhash string                  `json:"-"`
	LogoPath          string                  `json:"-"`
	StillPath         string                  `json:"-"`
	StillThumbhash    string                  `json:"-"`
	UpdatedAt         time.Time               `json:"-"`
	SeasonCount       *int                    `json:"season_count,omitempty"`
	SeriesID          string                  `json:"series_id,omitempty"`
	SeriesTitle       string                  `json:"series_title,omitempty"`
	SeasonNumber      *int                    `json:"season_number,omitempty"`
	EpisodeNumber     *int                    `json:"episode_number,omitempty"`
	EpisodeCount      *int                    `json:"episode_count,omitempty"`
	Runtime           int                     `json:"runtime,omitempty"`
	AirDate           string                  `json:"air_date,omitempty"`
	IsSpecials        bool                    `json:"is_specials,omitempty"`
	LogoURL           string                  `json:"logo_url,omitempty"`
	StillURL          string                  `json:"still_url,omitempty"`
	Studios           []string                `json:"studios,omitempty"`
	Countries         []string                `json:"countries,omitempty"`
	Tagline           string                  `json:"tagline,omitempty"`
	RatingTMDB        *float64                `json:"rating_tmdb,omitempty"`
	UserData          *catalog.SeasonUserData `json:"user_data,omitempty"`
}

type upstreamBrowseResponse struct {
	Total   int                `json:"total"`
	HasMore bool               `json:"has_more"`
	Items   []upstreamListItem `json:"items"`
}

type upstreamItemDetail struct {
	ContentID         string                  `json:"content_id"`
	Type              string                  `json:"type"`
	Title             string                  `json:"title"`
	SortTitle         string                  `json:"sort_title,omitempty"`
	OriginalTitle     string                  `json:"original_title"`
	Year              int                     `json:"year"`
	Overview          string                  `json:"overview"`
	Tagline           string                  `json:"tagline"`
	Runtime           int                     `json:"runtime"`
	ContentRating     string                  `json:"content_rating"`
	Genres            []string                `json:"genres"`
	RatingIMDB        *float64                `json:"rating_imdb"`
	RatingTMDB        *float64                `json:"rating_tmdb"`
	PosterURL         string                  `json:"poster_url"`
	BackdropURL       string                  `json:"backdrop_url"`
	LogoURL           string                  `json:"logo_url"`
	PosterPath        string                  `json:"-"`
	PosterThumbhash   string                  `json:"-"`
	BackdropPath      string                  `json:"-"`
	BackdropThumbhash string                  `json:"-"`
	LogoPath          string                  `json:"-"`
	UpdatedAt         time.Time               `json:"-"`
	Studios           []string                `json:"studios"`
	Countries         []string                `json:"countries"`
	SeasonCount       *int                    `json:"season_count,omitempty"`
	SeriesID          string                  `json:"series_id,omitempty"`
	SeriesTitle       string                  `json:"series_title,omitempty"`
	SeasonNumber      *int                    `json:"season_number,omitempty"`
	EpisodeNumber     *int                    `json:"episode_number,omitempty"`
	EpisodeCount      *int                    `json:"episode_count,omitempty"`
	AirDate           *string                 `json:"air_date,omitempty"`
	IsSpecials        bool                    `json:"is_specials,omitempty"`
	UserData          *catalog.SeasonUserData `json:"user_data,omitempty"`
	Versions          []catalog.FileVersion   `json:"versions,omitempty"`
	Cast              []catalog.CastCredit    `json:"cast,omitempty"`
	Crew              []catalog.CrewCredit    `json:"crew,omitempty"`
}

type upstreamSeason struct {
	ContentID       string                  `json:"content_id"`
	SeasonNumber    int                     `json:"season_number"`
	IsSpecials      bool                    `json:"is_specials,omitempty"`
	Title           string                  `json:"title"`
	Overview        string                  `json:"overview,omitempty"`
	AirDate         string                  `json:"air_date,omitempty"`
	EpisodeCount    int                     `json:"episode_count"`
	PosterURL       string                  `json:"poster_url,omitempty"`
	PosterPath      string                  `json:"-"`
	PosterThumbhash string                  `json:"-"`
	UpdatedAt       time.Time               `json:"-"`
	UserData        *catalog.SeasonUserData `json:"user_data,omitempty"`
}

type upstreamEpisodeFile struct {
	FileID        int    `json:"file_id"`
	Resolution    string `json:"resolution,omitempty"`
	CodecVideo    string `json:"codec_video,omitempty"`
	HDR           bool   `json:"hdr"`
	AudioChannels int    `json:"audio_channels,omitempty"`
	Container     string `json:"container,omitempty"`
	FileSize      int64  `json:"file_size"`
}

type upstreamEpisode struct {
	ContentID      string                  `json:"content_id"`
	SeasonNumber   int                     `json:"season_number"`
	EpisodeNumber  int                     `json:"episode_number"`
	Title          string                  `json:"title"`
	Overview       string                  `json:"overview,omitempty"`
	AirDate        string                  `json:"air_date,omitempty"`
	Runtime        int                     `json:"runtime"`
	StillURL       string                  `json:"still_url,omitempty"`
	StillPath      string                  `json:"-"`
	StillThumbhash string                  `json:"-"`
	UpdatedAt      time.Time               `json:"-"`
	UserData       *catalog.SeasonUserData `json:"user_data,omitempty"`
	Files          []upstreamEpisodeFile   `json:"files,omitempty"`
	SeriesID       string                  `json:"series_id,omitempty"`
	SeriesTitle    string                  `json:"series_title,omitempty"`
	SeasonID       string                  `json:"season_id,omitempty"`
}

type upstreamProgress struct {
	MediaItemID     string  `json:"media_item_id"`
	PositionSeconds float64 `json:"position_seconds"`
	DurationSeconds float64 `json:"duration_seconds"`
	Completed       bool    `json:"completed"`
	UpdatedAt       string  `json:"updated_at"`
}

type upstreamItemFiltersResponse struct {
	Genres []string `json:"genres"`
}

// upstreamProfile represents a user profile from the store.
type upstreamProfile struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
	HasPIN bool   `json:"has_pin"`
}

// HTTPError wraps a non-2xx response or service error with a status code.
type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("request failed with status %d", e.StatusCode)
}
