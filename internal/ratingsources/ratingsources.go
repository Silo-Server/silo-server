// Package ratingsources decides which external ratings (IMDb, TMDB, Rotten
// Tomatoes, ...) Silo shows and builds the list every client renders, so a
// title page reads the same on the web, Apple, and Android.
//
// IMDb and TMDB are always shown. Every other source is shown only after an
// administrator turns it on under config.CatalogExtraRatingSourcesSettingKey,
// because the owners of those scores restrict how others may display them.
package ratingsources

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Selection is the set of sources an administrator turned on in addition to
// the ones that are always shown. The zero value shows only those.
type Selection struct {
	extra map[string]struct{}
}

// NewSelection returns a selection that shows the given extra sources.
func NewSelection(extra ...string) Selection {
	sel := Selection{extra: make(map[string]struct{}, len(extra))}
	for _, source := range extra {
		sel.extra[source] = struct{}{}
	}
	return sel
}

// Shows reports whether clients show ratings from source.
func (s Selection) Shows(source string) bool {
	if models.RatingSourceAlwaysShown(source) {
		return true
	}
	_, ok := s.extra[source]
	return ok
}

// Shown returns the sources clients show, in display order: IMDb and TMDB,
// then each source the selection turned on that has a definition.
func (s Selection) Shown() []models.RatingSourceDefinition {
	var out []models.RatingSourceDefinition
	for _, definition := range models.RatingSourceDefinitions() {
		if s.Shows(definition.Source) {
			out = append(out, definition)
		}
	}
	return out
}

// cacheTTL bounds how long a node serves a cached selection. Every item detail
// and card list reads it, so it is not worth a database round trip per
// request, while an administrator's change still reaches every node within
// seconds.
const cacheTTL = 10 * time.Second

// Policy reads config.CatalogExtraRatingSourcesSettingKey through a
// config.CachedSetting: a successful read is cached for cacheTTL, and a failed
// one answers with the selection last read successfully.
type Policy struct {
	setting *config.CachedSetting[Selection]
}

// NewPolicy binds the policy to a server settings reader. A nil reader, or a
// nil policy, shows only the sources that are always shown.
func NewPolicy(settings config.SettingReader) *Policy {
	return &Policy{setting: config.NewCachedSetting(settings, config.CatalogExtraRatingSourcesSettingKey, cacheTTL, func(value string) Selection {
		return NewSelection(config.ParseRatingSourceList(value)...)
	})}
}

// Selection returns the sources to show.
func (p *Policy) Selection(ctx context.Context) Selection {
	if p == nil {
		return Selection{}
	}
	return p.setting.Get(ctx)
}

// Rating is one entry of the list clients render.
type Rating struct {
	Source string
	// Name is the plain-text mark shown next to the score.
	Name string
	// Score is on a common 0-100 scale.
	Score float64
	// Display is the score on the source's own scale, formatted: "8.5",
	// "93%", "4.2".
	Display string
}

// Item carries the ratings stored for one item: the four rating columns on
// media_items and the per-source scores (0-100) metadata providers reported.
type Item struct {
	IMDB       *float64
	TMDB       *float64
	RTCritic   *int
	RTAudience *int
	Sources    map[string]float64
}

// MaxTitleRatings is how many ratings a title page shows at most, on every
// client: the first ones in display order.
const MaxTitleRatings = 3

// Build returns the ratings clients show for item, in display order, at most
// MaxTitleRatings of them.
//
// IMDb, TMDB, and Rotten Tomatoes come from the rating columns, which are also
// what poster badges, browse sorting, and filters read, so every surface shows
// the same number. A per-source row fills in only when its column is empty.
// Values outside a source's scale are dropped rather than shown.
func Build(item Item, sel Selection) []Rating {
	scores := make(map[string]float64, len(item.Sources)+4)
	for source, score := range item.Sources {
		// Written so a NaN score, which fails every comparison, is dropped too.
		if !(score >= 0 && score <= 100) {
			continue
		}
		// IMDb and TMDB have no zero rating; a provider's 0 means "unrated",
		// as the rating columns already treat it.
		if score == 0 && models.RatingSourceAlwaysShown(source) {
			continue
		}
		scores[source] = score
	}
	if v := item.IMDB; v != nil && *v > 0 && *v <= 10 {
		scores[models.RatingSourceIMDB] = *v * 10
	}
	if v := item.TMDB; v != nil && *v > 0 && *v <= 10 {
		scores[models.RatingSourceTMDB] = *v * 10
	}
	if v := item.RTCritic; v != nil && *v >= 0 && *v <= 100 {
		scores[models.RatingSourceRTCritic] = float64(*v)
	}
	if v := item.RTAudience; v != nil && *v >= 0 && *v <= 100 {
		scores[models.RatingSourceRTAudience] = float64(*v)
	}

	var out []Rating
	for _, definition := range sel.Shown() {
		score, ok := scores[definition.Source]
		if !ok {
			continue
		}
		out = append(out, Rating{
			Source:  definition.Source,
			Name:    definition.Name,
			Score:   score,
			Display: Format(score, definition),
		})
		if len(out) == MaxTitleRatings {
			break
		}
	}
	return out
}

// Format renders a 0-100 score on the source's own scale: a percentage, one
// decimal place on scales up to 10, and a whole number otherwise. Halves round
// away from zero (7.35 reads 7.4); the web's card formatting in
// web/src/components/ratings/ratings.ts rounds the same way.
func Format(score float64, definition models.RatingSourceDefinition) string {
	value := score * definition.Scale / 100
	switch {
	case definition.Percent:
		return strconv.Itoa(int(math.Round(value))) + "%"
	case definition.Scale <= 10:
		return strconv.FormatFloat(math.Round(value*10)/10, 'f', 1, 64)
	default:
		return strconv.Itoa(int(math.Round(value)))
	}
}
