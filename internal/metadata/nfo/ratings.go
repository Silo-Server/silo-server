package nfo

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Rating sources the NFO provider declares beyond Silo's own IMDb and TMDB
// (see Provider.RatingSources). Kodi scrapers and tinyMediaManager write them
// as <rating name="..."> entries.
const (
	ratingSourceMetacritic = "metacritic"
	ratingSourceTrakt      = "trakt"
	ratingSourceTVDB       = "tvdb"
	ratingSourceLetterboxd = "letterboxd"
)

// nfoRatingName is what a <rating name> maps to: the rating source and the
// top of its scale when the entry omits max.
type nfoRatingName struct {
	source string
	max    float64
}

// nfoRatingNames maps lower-cased <rating name> spellings to rating sources.
// A name not listed has no source an administrator could show, so its rating
// is not stored.
var nfoRatingNames = map[string]nfoRatingName{
	idKeyIMDB:                {models.RatingSourceIMDB, 10},
	idKeyTMDB:                {models.RatingSourceTMDB, 10},
	"themoviedb":             {models.RatingSourceTMDB, 10},
	"tomatometerallcritics":  {models.RatingSourceRTCritic, 100},
	"rottentomatoes":         {models.RatingSourceRTCritic, 100},
	"tomatometerallaudience": {models.RatingSourceRTAudience, 100},
	ratingSourceMetacritic:   {ratingSourceMetacritic, 100},
	ratingSourceTrakt:        {ratingSourceTrakt, 10},
	idKeyTVDB:                {ratingSourceTVDB, 10},
	"thetvdb":                {ratingSourceTVDB, 10},
	ratingSourceLetterboxd:   {ratingSourceLetterboxd, 5},
}

// nfoRatingInput is the rating material one root element carries: the
// modern <ratings> block, the legacy bare <rating> with its <votes>, and
// Jellyfin's <criticrating> (a Rotten Tomatoes percentage).
type nfoRatingInput struct {
	block       nfoRatingsBlock
	legacy      string
	legacyVotes string
	critic      string
}

// applyRatingValues is the shared ratings mapper for <movie>/<tvshow>
// (via nfoCommon) and <episodedetails> roots.
//
// Each named rating fills its source once (the first entry wins), on its
// headline scale (IMDb and TMDB out of 10, Rotten Tomatoes out of 100) and in
// RatingSources with its votes on 0-100 (see ratingSources for which of those
// are reported). The legacy bare <rating> fills the IMDb slot, with its
// <votes>, only in an NFO without a <ratings> block (Kodi's legacy field
// historically held the IMDb scraper's rating). A writer that emits both, such
// as Radarr, copies its default source into the bare tag, and that source is
// TMDB until IMDb has a score.
func applyRatingValues(p *parsedNFO, in nfoRatingInput) {
	for _, r := range in.block.Ratings {
		spec, known := nfoRatingNames[strings.ToLower(strings.TrimSpace(r.Name))]
		value, ok := parseRatingValue(r.Value)
		if !known || !ok {
			continue
		}
		// An explicit max attribute always wins over the source's default
		// scale; a bare RT score (tomatometer 60) is out of 100, not 10.
		max := spec.max
		if parsed, parsedOK := parseRatingValue(r.Max); parsedOK {
			max = parsed
		}
		p.setRating(spec.source, value, max, parseVotes(r.Votes))
	}
	if len(in.block.Ratings) == 0 {
		if value, ok := parseRatingValue(in.legacy); ok {
			p.setRating(models.RatingSourceIMDB, value, 10, parseVotes(in.legacyVotes))
		}
	}
	if value, ok := parseRatingValue(in.critic); ok {
		p.setRating(models.RatingSourceRTCritic, value, 100, 0)
	}
	// <userrating> is intentionally not emitted; see parsedNFO.
}

// setRating records value (out of max) for source unless the file already
// gave it one: the headline slot for IMDb, TMDB, and Rotten Tomatoes, and the
// per-source row with its votes on 0-100.
func (p *parsedNFO) setRating(source string, value, max float64, votes int64) {
	if _, exists := p.RatingSources[source]; exists {
		return
	}
	if p.RatingSources == nil {
		p.RatingSources = make(map[string]metadata.RatingSource)
	}
	p.RatingSources[source] = metadata.RatingSource{Score: scaleRating(value, max, 100), Votes: votes}
	switch source {
	case models.RatingSourceIMDB:
		p.RatingIMDB = scaleRating(value, max, 10)
	case models.RatingSourceTMDB:
		p.RatingTMDB = scaleRating(value, max, 10)
	case models.RatingSourceRTCritic:
		p.RatingRTCritic = scaleRating(value, max, 100)
	case models.RatingSourceRTAudience:
		p.RatingRTAudience = scaleRating(value, max, 100)
	}
}

func parseRatingValue(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0, false
	}
	return value, true
}

// votesPattern matches a whole vote count, with or without the thousands
// separators some writers keep ("1,234,567", "1.234.567", "1 234 567").
var votesPattern = regexp.MustCompile(`^(?:\d+|\d{1,3}(?:[,. ]\d{3})+)$`)

// parseVotes reads a vote count. Anything else, such as a decimal ("8.5") or
// an abbreviation ("1.2K"), is 0, unknown.
func parseVotes(raw string) int64 {
	value := strings.TrimSpace(raw)
	if !votesPattern.MatchString(value) {
		return 0
	}
	votes, err := strconv.ParseInt(strings.NewReplacer(",", "", ".", "", " ", "").Replace(value), 10, 64)
	if err != nil || votes < 0 {
		return 0
	}
	return votes
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

// ratingSources returns the per-source ratings the file reports to the
// rating-source rows, credited to provider, or nil when there are none.
//
// IMDb and TMDB are left out: their scores already fill the rating columns,
// which title pages read first, and their rows hold the vote counts discovery
// ranks by (the TMDB count is copied onto the item). A count an NFO froze when
// it was written, or one it omits, would replace the remote provider's
// current count there.
func (p *parsedNFO) ratingSources(provider string) map[string]metadata.RatingSource {
	var sources map[string]metadata.RatingSource
	for name, source := range p.RatingSources {
		if models.IsBuiltinRatingSource(name) {
			continue
		}
		if sources == nil {
			sources = make(map[string]metadata.RatingSource, len(p.RatingSources))
		}
		source.Provider = provider
		sources[name] = source
	}
	return sources
}

// ratingSourceDefinitions are the sources beyond IMDb and TMDB the provider
// can report, for an administrator to choose to show. Trakt shows its rating
// as a percentage, like Rotten Tomatoes.
var ratingSourceDefinitions = []models.RatingSourceDefinition{
	{Source: models.RatingSourceRTCritic, Name: "RT", Label: "Rotten Tomatoes critics", Scale: 100, Percent: true},
	{Source: models.RatingSourceRTAudience, Name: "RT Audience", Label: "Rotten Tomatoes audience", Scale: 100, Percent: true},
	{Source: ratingSourceMetacritic, Name: "Metacritic", Label: "Metacritic", Scale: 100},
	{Source: ratingSourceTrakt, Name: "Trakt", Label: "Trakt", Scale: 100, Percent: true},
	{Source: ratingSourceTVDB, Name: "TVDB", Label: "TheTVDB", Scale: 10},
	{Source: ratingSourceLetterboxd, Name: "Letterboxd", Label: "Letterboxd", Scale: 5},
}
