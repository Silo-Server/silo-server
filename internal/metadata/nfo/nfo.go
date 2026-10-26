package nfo

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Content types emitted in parsedNFO.Type, matching the metadata package's
// content-type/level vocabulary.
const (
	typeMovie   = "movie"
	typeSeries  = "series"
	typeSeason  = "season"
	typeEpisode = "episode"
)

// nfoUniqueID represents a <uniqueid> element inside an NFO XML file.
type nfoUniqueID struct {
	Type    string `xml:"type,attr"`
	Default string `xml:"default,attr"`
	Value   string `xml:",chardata"`
}

// nfoActor represents an <actor> element (Kodi/Jellyfin cast entry). Kodi
// writes <order>, Jellyfin <sortorder>; Jellyfin's <type> names the credit
// kind (GuestStar, Director, ...), and tinyMediaManager writes the person's
// provider ids. Numeric elements decode as strings throughout and parse with
// parseNFOInt, so one bad value drops only that field instead of failing the
// whole file.
type nfoActor struct {
	Name      string `xml:"name"`
	Role      string `xml:"role"`
	Order     string `xml:"order"`
	SortOrder string `xml:"sortorder"`
	Type      string `xml:"type"`
	Thumb     string `xml:"thumb"`
	TmdbID    string `xml:"tmdbid"`
	ImdbID    string `xml:"imdbid"`
	TvdbID    string `xml:"tvdbid"`
}

// nfoNamedRating represents a <rating> entry inside a <ratings> block.
type nfoNamedRating struct {
	Name  string `xml:"name,attr"`
	Max   string `xml:"max,attr"`
	Value string `xml:"value"`
	Votes string `xml:"votes"`
}

// nfoRatingsBlock represents the modern multi-source <ratings> container.
type nfoRatingsBlock struct {
	Ratings []nfoNamedRating `xml:"rating"`
}

// nfoCommon holds the fields shared by <movie> and <tvshow> roots.
// encoding/xml flattens the embedded struct so both roots decode it in place.
//
// <set><name> (movie collections) is deliberately not mapped: MetadataResult
// has no collection field yet — see docs/architecture/local-nfo-metadata.md.
// <language> is not read either: Jellyfin and Emby write the library's
// metadata language there, not the title's original language.
type nfoCommon struct {
	Title            string          `xml:"title"`
	OriginalTitle    string          `xml:"originaltitle"`
	SortTitle        string          `xml:"sorttitle"`
	Tagline          string          `xml:"tagline"`
	Year             string          `xml:"year"`
	Plot             string          `xml:"plot"`
	Outline          string          `xml:"outline"`
	Runtime          string          `xml:"runtime"`
	Premiered        string          `xml:"premiered"`
	MPAA             string          `xml:"mpaa"`
	Certification    string          `xml:"certification"`
	OriginalLanguage string          `xml:"originallanguage"`
	Genres           []string        `xml:"genre"`
	Studios          []string        `xml:"studio"`
	Countries        []string        `xml:"country"`
	Tags             []string        `xml:"tag"`
	LegacyRating     string          `xml:"rating"`
	LegacyVotes      string          `xml:"votes"`
	CriticRating     string          `xml:"criticrating"`
	Ratings          nfoRatingsBlock `xml:"ratings"`
	UserRating       string          `xml:"userrating"`
	Actors           []nfoActor      `xml:"actor"`
	Directors        []string        `xml:"director"`
	Credits          []string        `xml:"credits"`
	Writers          []string        `xml:"writer"`
	Trailers         []string        `xml:"trailer"`
	Thumbs           []nfoThumb      `xml:"thumb"`
	Fanart           nfoFanart       `xml:"fanart"`
	UniqueIDs        []nfoUniqueID   `xml:"uniqueid"`
	nfoIDElements
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
	Aired        string           `xml:"aired"`
	Status       string           `xml:"status"`
	NamedSeasons []nfoNamedSeason `xml:"namedseason"`
	EpisodeGuide nfoEpisodeGuide  `xml:"episodeguide"`
}

// nfoNamedSeason is a tvshow <namedseason number="N">: the title Kodi shows
// for season N.
type nfoNamedSeason struct {
	Number string `xml:"number,attr"`
	Name   string `xml:",chardata"`
}

// nfoEpisode represents the <episodedetails> root element. An empty
// Season/Episode means absent, so "declared as 0" (specials) stays
// distinguishable — declared numbers are advisory and checked against the
// filename-derived structure by the provider.
type nfoEpisode struct {
	XMLName      xml.Name        `xml:"episodedetails"`
	Title        string          `xml:"title"`
	Season       string          `xml:"season"`
	Episode      string          `xml:"episode"`
	Plot         string          `xml:"plot"`
	Outline      string          `xml:"outline"`
	Aired        string          `xml:"aired"`
	Premiered    string          `xml:"premiered"`
	ReleaseDate  string          `xml:"releasedate"`
	Runtime      string          `xml:"runtime"`
	LegacyRating string          `xml:"rating"`
	LegacyVotes  string          `xml:"votes"`
	CriticRating string          `xml:"criticrating"`
	Ratings      nfoRatingsBlock `xml:"ratings"`
	Thumbs       []nfoThumb      `xml:"thumb"`
	UniqueIDs    []nfoUniqueID   `xml:"uniqueid"`
}

// nfoSeason represents the <season> root element (Kodi/Jellyfin season
// sidecar). SeasonNumber is a string for the same absent-vs-zero reason as
// nfoEpisode. Some writers name the season in <seasonname> instead of
// <title>.
type nfoSeason struct {
	XMLName      xml.Name `xml:"season"`
	Title        string   `xml:"title"`
	SeasonName   string   `xml:"seasonname"`
	Plot         string   `xml:"plot"`
	Outline      string   `xml:"outline"`
	SeasonNumber string   `xml:"seasonnumber"`
	Premiered    string   `xml:"premiered"`
	Aired        string   `xml:"aired"`
}

// parsedNFO holds extracted data from an NFO file. Collections stay nil when
// the NFO declares no entries so downstream MergeFillEmpty early-returns and
// remote providers can fill them (no placeholder emissions).
type parsedNFO struct {
	Title            string
	OriginalTitle    string
	SortTitle        string
	Tagline          string
	Year             int
	Overview         string
	Runtime          int    // minutes
	ReleaseDate      string // movies: <premiered> then <releasedate>
	FirstAirDate     string // series: <premiered> then <aired>; episodes: <aired>, <premiered>, <releasedate>; seasons: <premiered> then <aired>
	ContentRating    string
	OriginalLanguage string
	ShowStatus       string // series only, normalized
	Genres           []string
	Studios          []string
	Networks         []string // series only: Kodi treats a show's studio as its network
	Countries        []string
	Keywords         []string
	// Ratings normalized to their MetadataResult scales: IMDB/TMDB out of
	// 10, Rotten Tomatoes out of 100. <userrating> is parsed but never
	// emitted — MetadataResult has no per-user rating slot.
	RatingIMDB       float64
	RatingTMDB       float64
	RatingRTCritic   float64
	RatingRTAudience float64
	// RatingSources are the named ratings the file carries on the 0-100
	// scale, keyed by the rating source they map to. See ratings.go.
	RatingSources map[string]metadata.RatingSource
	// People carry the credit's provider ids. An actor's <thumb> rides in
	// LocalPhotoSource unresolved; the provider resolves it against the
	// NFO's folder (see art.go).
	People []models.ItemPerson
	Videos []metadata.RemoteVideo
	TmdbID string
	ImdbID string
	TvdbID string
	Type   string // movie, series, season, episode
	// Art lists the artwork references the file names, in document order.
	// They are unresolved: a local path is relative to the NFO's folder.
	Art []nfoArtRef
	// SeasonNames maps a season number to the title a tvshow <namedseason>
	// gives it.
	SeasonNames map[int]string
	// Season/Episode carry the NFO's declared numbers when the matching Set
	// flag is true. They are advisory only: naming owns structure, so the
	// directory/filename-derived numbers win on conflict.
	Season     int
	SeasonSet  bool
	Episode    int
	EpisodeSet bool
	// MultiEpisode marks a document with more than one <episodedetails>
	// root (multi-episode file). Out of scope in v1: the first block is
	// parsed and the caller warns.
	MultiEpisode bool
	// LinkOnly marks a URL-only NFO: it names the item through provider
	// links and carries no metadata fields.
	LinkOnly bool
}

// parseNFOData parses an NFO file. Besides XML documents it reads the two
// link forms Kodi and Jellyfin accept: a URL-only file (just a provider link)
// and a hybrid file (XML followed by a link).
func parseNFOData(data []byte) (*parsedNFO, error) {
	data = normalizeNFOText(data)
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("nfo: empty file")
	}
	if trimmed[0] != '<' {
		return parseLinkOnlyNFO(string(trimmed))
	}

	rootTag, err := detectRootTag(data)
	if err != nil {
		return nil, fmt.Errorf("nfo: %w", err)
	}

	switch rootTag {
	case typeMovie:
		return parseMovieNFO(data)
	case "tvshow":
		return parseTVShowNFO(data)
	case typeSeason:
		return parseSeasonNFO(data)
	case "episodedetails":
		return parseEpisodeNFO(data)
	default:
		return nil, fmt.Errorf("nfo: unsupported root element <%s>", rootTag)
	}
}

// parseLinkOnlyNFO reads a URL-only NFO: provider ids and no fields.
func parseLinkOnlyNFO(text string) (*parsedNFO, error) {
	links := scanProviderLinks(text)
	if links.empty() {
		return nil, fmt.Errorf("nfo: no XML root element or provider link")
	}
	p := &parsedNFO{Type: linkOnlyNFOType(links), LinkOnly: true}
	applyProviderLinks(p, links, p.Type)
	if len(p.providerIDs()) == 0 {
		return nil, fmt.Errorf("nfo: provider links name no usable id")
	}
	return p, nil
}

func detectRootTag(data []byte) (string, error) {
	decoder := newNFODecoder(data)
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
	trailing, _, err := decodeNFORoot(data, &m)
	if err != nil {
		return nil, fmt.Errorf("nfo: failed to parse movie: %w", err)
	}
	p := parsedFromCommon(&m.nfoCommon, typeMovie)
	p.ReleaseDate = firstDate(m.Premiered, m.ReleaseDate)
	deriveYear(p, m.Premiered, m.ReleaseDate)
	applyProviderLinks(p, scanProviderLinks(trailing), typeMovie)
	return p, nil
}

func parseTVShowNFO(data []byte) (*parsedNFO, error) {
	var s nfoTVShow
	trailing, _, err := decodeNFORoot(data, &s)
	if err != nil {
		return nil, fmt.Errorf("nfo: failed to parse tvshow: %w", err)
	}
	p := parsedFromCommon(&s.nfoCommon, typeSeries)
	p.FirstAirDate = firstDate(s.Premiered, s.Aired)
	deriveYear(p, s.Premiered, s.Aired)
	p.ShowStatus = metadata.NormalizeShowStatus(s.Status)
	// Kodi has no network element: a show's <studio> is its network.
	p.Networks = slices.Clone(p.Studios)
	p.SeasonNames = namedSeasons(s.NamedSeasons)
	applyEpisodeGuide(p, s.EpisodeGuide)
	applyProviderLinks(p, scanProviderLinks(trailing), typeSeries)
	return p, nil
}

func parseSeasonNFO(data []byte) (*parsedNFO, error) {
	var s nfoSeason
	if _, _, err := decodeNFORoot(data, &s); err != nil {
		return nil, fmt.Errorf("nfo: failed to parse season: %w", err)
	}
	p := &parsedNFO{
		Title:        firstNonBlank(s.Title, s.SeasonName),
		Overview:     firstNonBlank(s.Plot, s.Outline),
		FirstAirDate: firstDate(s.Premiered, s.Aired),
		Type:         typeSeason,
	}
	p.Season, p.SeasonSet = parseNFOInt(s.SeasonNumber)
	return p, nil
}

func parseEpisodeNFO(data []byte) (*parsedNFO, error) {
	var e nfoEpisode
	_, moreEpisodes, err := decodeNFORoot(data, &e)
	if err != nil {
		return nil, fmt.Errorf("nfo: failed to parse episodedetails: %w", err)
	}
	p := &parsedNFO{
		Title:        strings.TrimSpace(e.Title),
		Overview:     firstNonBlank(e.Plot, e.Outline),
		FirstAirDate: firstDate(e.Aired, e.Premiered, e.ReleaseDate),
		Runtime:      parseRuntimeMinutes(e.Runtime),
		Type:         typeEpisode,
		MultiEpisode: moreEpisodes,
	}
	p.Season, p.SeasonSet = parseNFOInt(e.Season)
	p.Episode, p.EpisodeSet = parseNFOInt(e.Episode)
	applyRatingValues(p, nfoRatingInput{block: e.Ratings, legacy: e.LegacyRating, legacyVotes: e.LegacyVotes, critic: e.CriticRating})
	applyUniqueIDs(p, e.UniqueIDs)
	p.Art = episodeArtRefs(e.Thumbs)
	return p, nil
}

// parsedFromCommon maps the shared <movie>/<tvshow> field set. Kodi joins
// several values in one element with " / ", so list elements are split on
// it.
func parsedFromCommon(c *nfoCommon, contentType string) *parsedNFO {
	p := &parsedNFO{
		Title:            strings.TrimSpace(c.Title),
		OriginalTitle:    strings.TrimSpace(c.OriginalTitle),
		SortTitle:        strings.TrimSpace(c.SortTitle),
		Tagline:          strings.TrimSpace(c.Tagline),
		Overview:         firstNonBlank(c.Plot, c.Outline),
		Runtime:          parseRuntimeMinutes(c.Runtime),
		ContentRating:    firstNonBlank(c.MPAA, certificationRating(c.Certification)),
		OriginalLanguage: originalLanguage(c.OriginalLanguage),
		Genres:           splitNFOList(c.Genres),
		Studios:          splitNFOList(c.Studios),
		Countries:        splitNFOList(c.Countries),
		Keywords:         cleanStrings(c.Tags),
		Videos:           trailerVideos(c.Trailers),
		Type:             contentType,
	}
	if year, ok := parseNFOInt(c.Year); ok && year > 0 {
		p.Year = year
	}
	applyRatingValues(p, nfoRatingInput{block: c.Ratings, legacy: c.LegacyRating, legacyVotes: c.LegacyVotes, critic: c.CriticRating})
	applyPeople(p, c)
	applyUniqueIDs(p, c.UniqueIDs)
	applyIDElements(p, &c.nfoIDElements)
	p.Art = itemArtRefs(c.Thumbs, c.Fanart)
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

// runtimePattern matches the runtime spellings NFO writers use: "117",
// "117 min", "117 minutes", and "1h 57m" (hours and minutes).
var runtimePattern = regexp.MustCompile(`(?i)^(?:(\d+)\s*h(?:rs?|ours?)?\.?\s*)?(?:(\d+)\s*(?:m|mins?|minutes?)?\.?)?$`)

// parseRuntimeMinutes reads a <runtime> in minutes. A value in any other
// shape, or zero, gives 0, which leaves runtime to other providers.
func parseRuntimeMinutes(raw string) int {
	match := runtimePattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return 0
	}
	hours, _ := strconv.Atoi(match[1])
	minutes, _ := strconv.Atoi(match[2])
	if hours > 100 || minutes > 100000 {
		return 0
	}
	return hours*60 + minutes
}

// certificationPrefix matches the country code Kodi and tinyMediaManager put
// before each rating in <certification> ("US:PG-13").
var certificationPrefix = regexp.MustCompile(`^[A-Za-z]{2,3}:\s*`)

// certificationRating reads a <certification>, which writers fill with
// country-tagged ratings joined by Kodi's separator ("US:PG-13 / GB:12A"):
// the first rating, without its country code, as <mpaa> would hold it.
func certificationRating(raw string) string {
	for _, part := range strings.Split(raw, kodiListSeparator) {
		rating := certificationPrefix.ReplaceAllString(strings.TrimSpace(part), "")
		if rating = strings.TrimSpace(rating); rating != "" {
			return rating
		}
	}
	return ""
}

// originalLanguage keeps an <originallanguage> that is a language code
// ("en", "fra", "pt-BR"), stored as its ISO 639 base language like the
// remote providers'. A display name ("English") names no code and is dropped.
func originalLanguage(raw string) string {
	if lang.CanonicalTag(raw) == "" {
		return ""
	}
	return lang.Canonical(raw)
}

// namedSeasons maps tvshow <namedseason number="N"> entries to their titles.
// The first entry for a number wins.
func namedSeasons(entries []nfoNamedSeason) map[int]string {
	var names map[int]string
	for _, entry := range entries {
		number, ok := parseNFOInt(entry.Number)
		name := strings.TrimSpace(entry.Name)
		if !ok || number < 0 || name == "" {
			continue
		}
		if names == nil {
			names = make(map[int]string)
		}
		if _, dup := names[number]; !dup {
			names[number] = name
		}
	}
	return names
}

// kodiListSeparator joins several values in one Kodi list element
// ("Action / Comedy").
const kodiListSeparator = " / "

// splitNFOList cleans a list element's values and splits each on Kodi's
// " / " separator, dropping empties and repeats. It returns nil when nothing
// remains.
func splitNFOList(values []string) []string {
	var parts []string
	for _, v := range values {
		parts = append(parts, strings.Split(v, kodiListSeparator)...)
	}
	out := cleanStrings(parts)
	if out == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(out))
	unique := out[:0]
	for _, v := range out {
		key := strings.ToLower(v)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, v)
	}
	return unique
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

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
