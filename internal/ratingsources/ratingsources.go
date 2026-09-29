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
	"sync"
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

// cacheTTL bounds how long a node serves a cached selection. Every item detail
// and card list reads it, so it is not worth a database round trip per
// request, while an administrator's change still reaches every node within
// seconds.
const cacheTTL = 10 * time.Second

// Policy reads config.CatalogExtraRatingSourcesSettingKey and caches a
// successful read for cacheTTL.
type Policy struct {
	settings config.SettingReader
	now      func() time.Time

	mu        sync.Mutex
	selection Selection
	expires   time.Time
}

// NewPolicy binds the policy to a server settings reader. A nil reader, or a
// nil policy, shows only the sources that are always shown.
func NewPolicy(settings config.SettingReader) *Policy {
	return &Policy{settings: settings, now: time.Now}
}

// Selection returns the sources to show. A read failure answers with the
// selection last read successfully, or the default when none has been; the
// failed read is not cached, so the next call retries.
func (p *Policy) Selection(ctx context.Context) Selection {
	if p == nil || p.settings == nil {
		return Selection{}
	}
	p.mu.Lock()
	sel, fresh := p.selection, p.now().Before(p.expires)
	p.mu.Unlock()
	if fresh {
		return sel
	}

	value, err := p.settings.Get(ctx, config.CatalogExtraRatingSourcesSettingKey)
	if err != nil {
		return sel
	}
	sel = NewSelection(config.ParseRatingSourceList(value)...)

	p.mu.Lock()
	p.selection = sel
	p.expires = p.now().Add(cacheTTL)
	p.mu.Unlock()
	return sel
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

// Build returns the ratings clients show for item, in display order.
//
// IMDb, TMDB, and Rotten Tomatoes come from the rating columns, which are also
// what poster badges, browse sorting, and filters read, so every surface shows
// the same number. A per-source row fills in only when its column is empty.
// Values outside a source's scale are dropped rather than shown.
func Build(item Item, sel Selection) []Rating {
	scores := make(map[string]float64, len(item.Sources)+4)
	for source, score := range item.Sources {
		if score < 0 || score > 100 {
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
	for _, definition := range models.RatingSourceDefinitions() {
		score, ok := scores[definition.Source]
		if !ok || !sel.Shows(definition.Source) {
			continue
		}
		out = append(out, Rating{
			Source:  definition.Source,
			Name:    definition.Name,
			Score:   score,
			Display: Format(score, definition),
		})
	}
	return out
}

// Format renders a 0-100 score on the source's own scale: a percentage, one
// decimal place on scales up to 10, and a whole number otherwise.
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
