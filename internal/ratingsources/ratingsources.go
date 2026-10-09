// Package ratingsources decides which external ratings Silo shows and builds
// the list every client renders, so a title page reads the same on the web,
// Apple, and Android.
//
// Silo's own sources, IMDb and TMDB, are always shown. Every other source is
// one a metadata plugin declares, and is shown only after an administrator
// turns it on under config.CatalogExtraRatingSourcesSettingKey: the owners of
// those scores restrict how others may display them, so Silo leaves the choice
// to the administrator and ships no list of its own.
package ratingsources

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Selection is the set of sources an administrator turned on in addition to
// the ones that are always shown, and the definitions of the sources metadata
// plugins declared. The zero value shows only IMDb and TMDB.
type Selection struct {
	extra      map[string]struct{}
	declared   []models.RatingSourceDefinition
	declaredID map[string]struct{}
}

// WithDeclared returns the selection with the given plugin-declared source
// definitions, which Build lists after Silo's own sources when shown.
func (s Selection) WithDeclared(declared []models.RatingSourceDefinition) Selection {
	s.declared = declared
	s.declaredID = make(map[string]struct{}, len(declared))
	for _, definition := range declared {
		s.declaredID[definition.Source] = struct{}{}
	}
	return s
}

// NewSelection returns a selection that shows the given extra sources.
func NewSelection(extra ...string) Selection {
	sel := Selection{extra: make(map[string]struct{}, len(extra))}
	for _, source := range extra {
		sel.extra[source] = struct{}{}
	}
	return sel
}

// Shows reports whether clients show ratings from source: always for Silo's
// own, and otherwise only for a source an enabled plugin declares and the
// administrator turned on.
func (s Selection) Shows(source string) bool {
	if models.RatingSourceAlwaysShown(source) {
		return true
	}
	if _, declared := s.declaredID[source]; !declared {
		return false
	}
	_, ok := s.extra[source]
	return ok
}

// Shown returns the sources clients show, in display order: IMDb and TMDB,
// then each declared source the selection turned on, in the order the plugins
// declared them.
func (s Selection) Shown() []models.RatingSourceDefinition {
	var out []models.RatingSourceDefinition
	for _, definition := range append(models.RatingSourceDefinitions(), s.declared...) {
		if s.Shows(definition.Source) {
			out = append(out, definition)
		}
	}
	return out
}

// Sources returns the shown sources among the given names, in the order of
// Shown.
func (s Selection) Sources(names []string) []string {
	present := make(map[string]struct{}, len(names))
	for _, name := range names {
		present[name] = struct{}{}
	}
	var out []string
	for _, definition := range s.Shown() {
		if _, ok := present[definition.Source]; ok {
			out = append(out, definition.Source)
		}
	}
	return out
}

// cacheTTL bounds how long a node serves a cached selection. Every item detail
// and card list reads it, so it is not worth a database round trip per
// request, while an administrator's change still reaches every node within
// seconds.
const cacheTTL = 10 * time.Second

// refreshTimeout bounds one refresh of the cached selection. The refresh runs
// detached from the request that started it, since every request waiting on
// it shares the result.
const refreshTimeout = 5 * time.Second

// DeclaredSource is a rating source a metadata plugin declared.
type DeclaredSource struct {
	models.RatingSourceDefinition
	// Provider names the metadata providers that declare it, such as
	// "NFO Files and MDBList".
	Provider string
}

// DeclaredFunc lists the rating sources the enabled metadata plugins declare,
// in provider order, each source once.
type DeclaredFunc func(ctx context.Context) ([]DeclaredSource, error)

// Policy reads config.CatalogExtraRatingSourcesSettingKey and the sources
// metadata plugins declare, and caches a successful read for cacheTTL.
type Policy struct {
	settings config.SettingReader
	declared DeclaredFunc
	now      func() time.Time

	refresh singleflight.Group

	mu        sync.Mutex
	selection Selection
	// haveDeclared is true once a read of the plugin declarations succeeded,
	// so selection.declared holds real declarations.
	haveDeclared bool
	expires      time.Time
}

// NewPolicy binds the policy to a server settings reader and the lister of
// plugin-declared sources. A nil reader, or a nil policy, shows only the
// sources that are always shown; a nil lister shows no plugin sources.
func NewPolicy(settings config.SettingReader, declared DeclaredFunc) *Policy {
	return &Policy{settings: settings, declared: declared, now: time.Now}
}

// Selection returns the sources to show. Once the cache expires, one read per
// node refreshes it for every request that asks meanwhile.
//
// A failed settings read answers with what was last read successfully, or the
// default when nothing has been, and is not cached, so the next call retries.
// A failed read of the plugin declarations keeps the declarations last read
// and is retried after cacheTTL like any other read; until one read succeeds,
// the selection is not cached, so a node never caches an empty list of
// declarations it could not read.
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
	result, _, _ := p.refresh.Do("selection", func() (any, error) {
		return p.read(context.WithoutCancel(ctx)), nil
	})
	sel, _ = result.(Selection)
	return sel
}

// read refreshes the cached selection and returns it.
func (p *Policy) read(ctx context.Context) Selection {
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()

	p.mu.Lock()
	last, haveDeclared := p.selection, p.haveDeclared
	p.mu.Unlock()

	value, err := p.settings.Get(ctx, config.CatalogExtraRatingSourcesSettingKey)
	if err != nil {
		return last
	}
	declared := last.declared
	if p.declared == nil {
		haveDeclared = true
	} else if sources, err := p.declared(ctx); err == nil {
		declared, haveDeclared = definitionsOf(sources), true
	}
	sel := NewSelection(config.ParseRatingSourceList(value)...).WithDeclared(declared)

	p.mu.Lock()
	p.selection = sel
	p.haveDeclared = haveDeclared
	if haveDeclared {
		p.expires = p.now().Add(cacheTTL)
	}
	p.mu.Unlock()
	return sel
}

func definitionsOf(sources []DeclaredSource) []models.RatingSourceDefinition {
	out := make([]models.RatingSourceDefinition, 0, len(sources))
	for _, source := range sources {
		out = append(out, source.RatingSourceDefinition)
	}
	return out
}

// Source is one rating source an administrator can show.
type Source struct {
	models.RatingSourceDefinition
	// AlwaysShown is true for IMDb and TMDB, which no setting hides.
	AlwaysShown bool
	// Provider names the metadata providers that declare the source; empty for
	// Silo's own sources.
	Provider string
}

// Sources lists Silo's own rating sources and those the enabled metadata
// plugins declare, read fresh, in display order.
func (p *Policy) Sources(ctx context.Context) ([]Source, error) {
	var out []Source
	for _, definition := range models.RatingSourceDefinitions() {
		out = append(out, Source{RatingSourceDefinition: definition, AlwaysShown: models.RatingSourceAlwaysShown(definition.Source)})
	}
	if p == nil || p.declared == nil {
		return out, nil
	}
	declared, err := p.declared(ctx)
	if err != nil {
		return nil, err
	}
	for _, source := range declared {
		out = append(out, Source{RatingSourceDefinition: source.RatingSourceDefinition, Provider: source.Provider})
	}
	return out, nil
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
// Plugin-declared sources follow Silo's own, in the order they were declared;
// Rotten Tomatoes shows only when a plugin declares rt_critic or rt_audience.
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

// Format renders a 0-100 score on the source's own scale: a percentage of the
// 0-100 score, one decimal place on scales up to 10, and a whole number
// otherwise. Halves round away from zero (7.35 reads 7.4); the web's card
// formatting in web/src/components/ratings/ratings.ts rounds the same way.
func Format(score float64, definition models.RatingSourceDefinition) string {
	value := score * definition.Scale / 100
	switch {
	case definition.Percent:
		return strconv.Itoa(int(math.Round(score))) + "%"
	case definition.Scale <= 10:
		return strconv.FormatFloat(math.Round(value*10)/10, 'f', 1, 64)
	default:
		return strconv.Itoa(int(math.Round(value)))
	}
}
