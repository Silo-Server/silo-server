package nfo

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// nfoUniqueID represents a <uniqueid> element inside an NFO XML file.
type nfoUniqueID struct {
	Type    string `xml:"type,attr"`
	Default string `xml:"default,attr"`
	Value   string `xml:",chardata"`
}

// nfoActor represents an <actor> element (Kodi/Jellyfin cast entry).
// <thumb> URLs are intentionally ignored in v1.
type nfoActor struct {
	Name  string `xml:"name"`
	Role  string `xml:"role"`
	Order *int   `xml:"order"`
}

// nfoNamedRating represents a <rating> entry inside a <ratings> block.
type nfoNamedRating struct {
	Name  string `xml:"name,attr"`
	Max   string `xml:"max,attr"`
	Value string `xml:"value"`
}

// nfoRatingsBlock represents the modern multi-source <ratings> container.
type nfoRatingsBlock struct {
	Ratings []nfoNamedRating `xml:"rating"`
}

// nfoCommon holds the fields shared by <movie> and <tvshow> roots.
// encoding/xml flattens the embedded struct so both roots decode it in place.
//
// <set><name> (movie collections) is deliberately not mapped: MetadataResult
// has no collection field yet — see
// docs/superpowers/plans/2026-07-09-local-nfo-metadata-and-artwork.md §9.
type nfoCommon struct {
	Title         string          `xml:"title"`
	OriginalTitle string          `xml:"originaltitle"`
	Tagline       string          `xml:"tagline"`
	Year          int             `xml:"year"`
	Plot          string          `xml:"plot"`
	Runtime       string          `xml:"runtime"`
	Premiered     string          `xml:"premiered"`
	MPAA          string          `xml:"mpaa"`
	Genres        []string        `xml:"genre"`
	Studios       []string        `xml:"studio"`
	Countries     []string        `xml:"country"`
	Tags          []string        `xml:"tag"`
	LegacyRating  string          `xml:"rating"`
	Ratings       nfoRatingsBlock `xml:"ratings"`
	UserRating    string          `xml:"userrating"`
	Actors        []nfoActor      `xml:"actor"`
	Directors     []string        `xml:"director"`
	Credits       []string        `xml:"credits"`
	UniqueIDs     []nfoUniqueID   `xml:"uniqueid"`
}

// nfoMovie represents the <movie> root element.
type nfoMovie struct {
	XMLName xml.Name `xml:"movie"`
	nfoCommon
	ReleaseDate string `xml:"releasedate"`
}

// nfoTVShow represents the <tvshow> root element.
type nfoTVShow struct {
	XMLName xml.Name `xml:"tvshow"`
	nfoCommon
	Aired string `xml:"aired"`
}

// nfoEpisode represents the <episodedetails> root element.
type nfoEpisode struct {
	XMLName   xml.Name      `xml:"episodedetails"`
	Title     string        `xml:"title"`
	Season    int           `xml:"season"`
	Episode   int           `xml:"episode"`
	Plot      string        `xml:"plot"`
	UniqueIDs []nfoUniqueID `xml:"uniqueid"`
}

// parsedNFO holds extracted data from an NFO file. Collections stay nil when
// the NFO declares no entries so downstream MergeFillEmpty early-returns and
// remote providers can fill them (no placeholder emissions).
type parsedNFO struct {
	Title         string
	OriginalTitle string
	Tagline       string
	Year          int
	Overview      string
	Runtime       int    // minutes
	ReleaseDate   string // movies: <premiered> then <releasedate>
	FirstAirDate  string // series: <premiered> then <aired>
	ContentRating string
	Genres        []string
	Studios       []string
	Countries     []string
	Keywords      []string
	// Ratings normalized to their MetadataResult scales: IMDB/TMDB out of
	// 10, Rotten Tomatoes out of 100. <userrating> is parsed but never
	// emitted — MetadataResult has no per-user rating slot.
	RatingIMDB       float64
	RatingTMDB       float64
	RatingRTCritic   float64
	RatingRTAudience float64
	People           []models.ItemPerson
	TmdbID           string
	ImdbID           string
	TvdbID           string
	Type             string // movie, series, episode
	Season           int
	Episode          int
}

// parseNFOData parses NFO XML data and returns structured results.
func parseNFOData(data []byte) (*parsedNFO, error) {
	// Strip UTF-8 BOM if present.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	rootTag, err := detectRootTag(data)
	if err != nil {
		return nil, fmt.Errorf("nfo: %w", err)
	}

	switch rootTag {
	case "movie":
		return parseMovieNFO(data)
	case "tvshow":
		return parseTVShowNFO(data)
	case "episodedetails":
		return parseEpisodeNFO(data)
	default:
		return nil, fmt.Errorf("nfo: unsupported root element <%s>", rootTag)
	}
}

func detectRootTag(data []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := decoder.Token()
		if err != nil {
			return "", fmt.Errorf("cannot detect root element: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local, nil
		}
	}
}

func parseMovieNFO(data []byte) (*parsedNFO, error) {
	var m nfoMovie
	if err := xmlUnmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("nfo: failed to parse movie: %w", err)
	}
	p := parsedFromCommon(&m.nfoCommon, "movie")
	p.ReleaseDate = firstDate(m.Premiered, m.ReleaseDate)
	deriveYear(p, m.Premiered, m.ReleaseDate)
	return p, nil
}

func parseTVShowNFO(data []byte) (*parsedNFO, error) {
	var s nfoTVShow
	if err := xmlUnmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("nfo: failed to parse tvshow: %w", err)
	}
	p := parsedFromCommon(&s.nfoCommon, "series")
	p.FirstAirDate = firstDate(s.Premiered, s.Aired)
	deriveYear(p, s.Premiered, s.Aired)
	return p, nil
}

func parseEpisodeNFO(data []byte) (*parsedNFO, error) {
	var e nfoEpisode
	if err := xmlUnmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("nfo: failed to parse episodedetails: %w", err)
	}
	p := &parsedNFO{
		Title:    strings.TrimSpace(e.Title),
		Overview: strings.TrimSpace(e.Plot),
		Type:     "episode",
		Season:   e.Season,
		Episode:  e.Episode,
	}
	applyUniqueIDs(p, e.UniqueIDs)
	return p, nil
}

// parsedFromCommon maps the shared <movie>/<tvshow> field set.
func parsedFromCommon(c *nfoCommon, contentType string) *parsedNFO {
	p := &parsedNFO{
		Title:         strings.TrimSpace(c.Title),
		OriginalTitle: strings.TrimSpace(c.OriginalTitle),
		Tagline:       strings.TrimSpace(c.Tagline),
		Year:          c.Year,
		Overview:      strings.TrimSpace(c.Plot),
		ContentRating: strings.TrimSpace(c.MPAA),
		Genres:        cleanStrings(c.Genres),
		Studios:       cleanStrings(c.Studios),
		Countries:     cleanStrings(c.Countries),
		Keywords:      cleanStrings(c.Tags),
		Type:          contentType,
	}
	if minutes, err := strconv.Atoi(strings.TrimSpace(c.Runtime)); err == nil && minutes > 0 {
		p.Runtime = minutes
	}
	applyRatings(p, c)
	applyPeople(p, c)
	applyUniqueIDs(p, c.UniqueIDs)
	return p
}

var nfoDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// firstDate returns the first candidate that looks like an ISO date
// (YYYY-MM-DD); anything else is ignored rather than persisted.
func firstDate(candidates ...string) string {
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if nfoDatePattern.MatchString(c) {
			return c
		}
	}
	return ""
}

// deriveYear fills Year when the NFO omits <year>, from the first candidate
// carrying a leading 4-digit year. Kodi exports commonly ship only <premiered>,
// sometimes as a bare year (2021) or a datetime (2021-05-01T20:00:00) that
// firstDate's strict ISO match rejects for storage; the year is still
// recoverable here for display.
func deriveYear(p *parsedNFO, candidates ...string) {
	if p.Year != 0 {
		return
	}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if len(c) < 4 {
			continue
		}
		if year, err := strconv.Atoi(c[:4]); err == nil && year > 0 {
			p.Year = year
			return
		}
	}
}

// applyRatings maps the modern multi-source <ratings> block with the legacy
// bare <rating> as an IMDB-slot fallback (Kodi's legacy field historically
// held the scraper's IMDB rating).
func applyRatings(p *parsedNFO, c *nfoCommon) {
	for _, r := range c.Ratings.Ratings {
		value, ok := parseRatingValue(r.Value)
		if !ok {
			continue
		}
		max := 10.0
		if parsed, parsedOK := parseRatingValue(r.Max); parsedOK {
			max = parsed
		}
		switch strings.ToLower(strings.TrimSpace(r.Name)) {
		case "imdb":
			p.RatingIMDB = scaleRating(value, max, 10)
		case "tmdb", "themoviedb":
			p.RatingTMDB = scaleRating(value, max, 10)
		case "tomatometerallcritics", "rottentomatoes":
			p.RatingRTCritic = scaleRating(value, max, 100)
		case "tomatometerallaudience":
			p.RatingRTAudience = scaleRating(value, max, 100)
		}
	}
	if p.RatingIMDB == 0 {
		if value, ok := parseRatingValue(c.LegacyRating); ok {
			p.RatingIMDB = scaleRating(value, 10, 10)
		}
	}
	// <userrating> is intentionally not emitted; see parsedNFO.
}

func parseRatingValue(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func scaleRating(value, max, targetMax float64) float64 {
	if max <= 0 {
		max = targetMax
	}
	scaled := value * targetMax / max
	if scaled > targetMax {
		return targetMax
	}
	return scaled
}

// applyPeople maps <actor> entries to cast and <director>/<credits> to crew.
func applyPeople(p *parsedNFO, c *nfoCommon) {
	people := make([]models.ItemPerson, 0, len(c.Actors)+len(c.Directors)+len(c.Credits))
	for i, actor := range c.Actors {
		name := strings.TrimSpace(actor.Name)
		if name == "" {
			continue
		}
		order := i
		if actor.Order != nil {
			order = *actor.Order
		}
		people = append(people, models.ItemPerson{
			Person:    models.Person{Name: name},
			Kind:      models.PersonKindActor,
			Character: strings.TrimSpace(actor.Role),
			SortOrder: order,
		})
	}
	people = append(people, crewPeople(c.Directors, models.PersonKindDirector)...)
	people = append(people, crewPeople(c.Credits, models.PersonKindWriter)...)
	if len(people) > 0 {
		p.People = people
	}
}

func crewPeople(names []string, kind models.PersonKind) []models.ItemPerson {
	people := make([]models.ItemPerson, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		people = append(people, models.ItemPerson{
			Person:    models.Person{Name: name},
			Kind:      kind,
			SortOrder: len(people),
		})
	}
	return people
}

// cleanStrings trims entries and drops empties; returns nil (not an empty
// slice) when nothing remains so merge early-returns apply.
func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func xmlUnmarshal(data []byte, v any) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	return decoder.Decode(v)
}

func applyUniqueIDs(p *parsedNFO, ids []nfoUniqueID) {
	for _, uid := range ids {
		val := strings.TrimSpace(uid.Value)
		switch strings.ToLower(strings.TrimSpace(uid.Type)) {
		case "imdb":
			p.ImdbID = val
		case "tmdb", "themoviedb":
			p.TmdbID = val
		case "tvdb":
			p.TvdbID = val
		}
	}
}
