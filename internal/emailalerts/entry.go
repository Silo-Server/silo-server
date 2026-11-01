package emailalerts

import "time"

// Card is one catalog item as the templates see it: a movie, an episode, or
// (inside Detail) a series. Image fields hold URLs the renderer embeds or
// links.
type Card struct {
	ContentID              string
	Type                   string // movie | episode | series
	Title                  string
	Year                   int
	Overview               string
	AddedAt                time.Time
	ReleaseDate            string // YYYY-MM-DD
	Runtime                int    // minutes
	DurationSeconds        float64
	ContentRating          string
	Genres                 []string
	Studios                []string
	Networks               []string
	RatingTMDB             float64
	RatingIMDB             float64
	PosterURL              string
	BackdropURL            string
	PosterIsEpisodeStill   bool
	BackdropIsEpisodeStill bool
	SeriesID               string
	SeriesTitle            string
	SeasonNumber           *int
	EpisodeNumber          *int
	ShowStatus             string
}

// Detail is the extra context of a series, episode or movie.
type Detail struct {
	Card
	Tagline      string
	FirstAirDate string
	AirDate      string
	SeasonCount  int
	EpisodeCount int
	LogoURL      string
	Cast         []CastMember
}

type CastMember struct {
	Name      string
	Character string
}

func (c Card) added() time.Time { return c.AddedAt }
