package ratingsources

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func ptr[T any](v T) *T { return &v }

// Sources a metadata plugin such as MDBList would declare.
var (
	rtCritic   = models.RatingSourceDefinition{Source: "rt_critic", Name: "RT", Label: "Rotten Tomatoes critics", Scale: 100, Percent: true}
	rtAudience = models.RatingSourceDefinition{Source: "rt_audience", Name: "RT Audience", Label: "Rotten Tomatoes audience", Scale: 100, Percent: true}
	metacritic = models.RatingSourceDefinition{Source: "metacritic", Name: "Metacritic", Label: "Metacritic", Scale: 100}
	letterboxd = models.RatingSourceDefinition{Source: "letterboxd", Name: "Letterboxd", Label: "Letterboxd", Scale: 5}
	kinopoisk  = models.RatingSourceDefinition{Source: "kinopoisk", Name: "Kinopoisk", Label: "Kinopoisk", Scale: 10}
)

func declared(definitions ...models.RatingSourceDefinition) []models.RatingSourceDefinition {
	return definitions
}

func TestBuildShowsIMDbAndTMDBByDefault(t *testing.T) {
	item := Item{
		IMDB: ptr(8.5), TMDB: ptr(8.25), RTCritic: ptr(93), RTAudience: ptr(95),
		Sources: map[string]float64{"metacritic": 87},
	}
	got := Build(item, Selection{}.WithDeclared(declared(rtCritic, rtAudience, metacritic)))
	want := []Rating{
		{Source: "imdb", Name: "IMDb", Score: 85, Display: "8.5"},
		{Source: "tmdb", Name: "TMDB", Score: 82.5, Display: "8.3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v\nwant %+v", got, want)
	}
}

func TestBuildAddsDeclaredSourcesAnAdministratorTurnedOn(t *testing.T) {
	item := Item{
		IMDB: ptr(8.5), RTCritic: ptr(93), RTAudience: ptr(95),
		Sources: map[string]float64{
			"letterboxd": 84,
			"metacritic": 87,
			"trakt":      81,
		},
	}
	sel := NewSelection("rt_critic", "rt_audience", "metacritic", "letterboxd", "trakt").
		WithDeclared(declared(rtCritic, rtAudience, metacritic, letterboxd))
	got := Build(item, sel)
	want := []Rating{
		{Source: "imdb", Name: "IMDb", Score: 85, Display: "8.5"},
		{Source: "rt_critic", Name: "RT", Score: 93, Display: "93%"},
		{Source: "rt_audience", Name: "RT Audience", Score: 95, Display: "95%"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v\nwant %+v (trakt has no declaration, so it stays hidden)", got, want)
	}
}

// Silo names only IMDb and TMDB. A source the administrator turned on still
// shows nothing unless an enabled plugin declares it, Rotten Tomatoes included.
func TestBuildShowsNothingAPluginDoesNotDeclare(t *testing.T) {
	item := Item{IMDB: ptr(8.5), RTCritic: ptr(93), Sources: map[string]float64{"kinopoisk": 72}}
	sel := NewSelection("rt_critic", "kinopoisk")
	if sel.Shows("rt_critic") {
		t.Fatal("Rotten Tomatoes shows without a plugin declaring it")
	}
	got := Build(item, sel)
	if len(got) != 1 || got[0].Source != "imdb" {
		t.Fatalf("Build = %+v, want IMDb only", got)
	}
}

// A title page shows at most MaxTitleRatings ratings, the first ones in
// display order, whatever the administrator turned on.
func TestBuildShowsAtMostThree(t *testing.T) {
	item := Item{IMDB: ptr(8.5), TMDB: ptr(8.25), RTCritic: ptr(93), RTAudience: ptr(95)}
	got := Build(item, NewSelection("rt_critic", "rt_audience").WithDeclared(declared(rtCritic, rtAudience)))
	var sources []string
	for _, r := range got {
		sources = append(sources, r.Source)
	}
	if want := []string{"imdb", "tmdb", "rt_critic"}; !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %q, want %q", sources, want)
	}
}

// The rating columns win over a provider's per-source row, because poster
// badges, sorting, and filters read the columns: a title page must not show a
// different number from the badge on its poster.
func TestBuildPrefersRatingColumnsOverSourceRows(t *testing.T) {
	item := Item{
		TMDB: ptr(7.625),
		Sources: map[string]float64{
			models.RatingSourceTMDB: 76,
			models.RatingSourceIMDB: 81,
		},
	}
	got := Build(item, Selection{})
	want := []Rating{
		{Source: "imdb", Name: "IMDb", Score: 81, Display: "8.1"},
		{Source: "tmdb", Name: "TMDB", Score: 76.25, Display: "7.6"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v\nwant %+v", got, want)
	}
}

func TestBuildDropsANaNSourceScore(t *testing.T) {
	item := Item{Sources: map[string]float64{models.RatingSourceIMDB: math.NaN()}}
	if got := Build(item, Selection{}); len(got) != 0 {
		t.Fatalf("Build = %+v, want nothing for a NaN score", got)
	}
}

func TestBuildDropsValuesOutsideTheSourceScale(t *testing.T) {
	item := Item{IMDB: ptr(85.0), TMDB: ptr(0.0), RTCritic: ptr(130)}
	if got := Build(item, NewSelection("rt_critic").WithDeclared(declared(rtCritic))); len(got) != 0 {
		t.Fatalf("Build = %+v, want nothing for out-of-scale values", got)
	}
}

func TestBuildTreatsAZeroIMDbOrTMDBRowAsUnrated(t *testing.T) {
	item := Item{Sources: map[string]float64{models.RatingSourceIMDB: 0, models.RatingSourceTMDB: 0, models.RatingSourceRTCritic: 0}}
	got := Build(item, NewSelection("rt_critic").WithDeclared(declared(rtCritic)))
	want := []Rating{{Source: "rt_critic", Name: "RT", Score: 0, Display: "0%"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v, want only the Rotten Tomatoes 0%%", got)
	}
}

func TestBuildIgnoresUnknownSources(t *testing.T) {
	item := Item{Sources: map[string]float64{"kinopoisk": 72}}
	if got := Build(item, NewSelection("kinopoisk")); len(got) != 0 {
		t.Fatalf("Build = %+v, want nothing for a source without a definition, even when the setting names it", got)
	}
}

func TestFormat(t *testing.T) {
	imdb, tmdb := models.RatingSourceDefinitions()[0], models.RatingSourceDefinitions()[1]
	cases := []struct {
		definition models.RatingSourceDefinition
		score      float64
		want       string
	}{
		{imdb, 82.5, "8.3"},
		{imdb, 80, "8.0"},
		// Halves round away from zero, as the web's card formatting does.
		{imdb, 73.5, "7.4"},
		{tmdb, 70.5, "7.1"},
		{rtCritic, 0, "0%"},
		{rtAudience, 99.6, "100%"},
		{metacritic, 87.4, "87"},
		{letterboxd, 84, "4.2"},
		{kinopoisk, 81, "8.1"},
		{models.RatingSourceDefinition{Source: "stars", Scale: 4}, 87.5, "3.5"},
		// A percentage is of the 0-100 score, whatever the scale says.
		{models.RatingSourceDefinition{Source: "approval", Scale: 10, Percent: true}, 72, "72%"},
	}
	for _, tc := range cases {
		if got := Format(tc.score, tc.definition); got != tc.want {
			t.Errorf("Format(%v, %s) = %q, want %q", tc.score, tc.definition.Source, got, tc.want)
		}
	}
}

type stubSettings struct {
	value string
	err   error
	reads int
}

func (s *stubSettings) Get(context.Context, string) (string, error) {
	s.reads++
	return s.value, s.err
}

func declaring(definitions ...models.RatingSourceDefinition) DeclaredFunc {
	return func(context.Context) ([]DeclaredSource, error) {
		out := make([]DeclaredSource, 0, len(definitions))
		for _, definition := range definitions {
			out = append(out, DeclaredSource{RatingSourceDefinition: definition, Provider: "MDBList"})
		}
		return out, nil
	}
}

func TestPolicyCachesAndKeepsLastGoodSelection(t *testing.T) {
	settings := &stubSettings{value: "rt_critic"}
	now := time.Unix(0, 0)
	p := NewPolicy(settings, declaring(rtCritic, rtAudience))
	p.now = func() time.Time { return now }
	ctx := context.Background()

	if !p.Selection(ctx).Shows("rt_critic") || p.Selection(ctx).Shows("rt_audience") {
		t.Fatal("selection does not follow the setting")
	}
	if settings.reads != 1 {
		t.Fatalf("reads = %d, want one read served from cache", settings.reads)
	}

	now = now.Add(cacheTTL)
	settings.err = errors.New("database down")
	if !p.Selection(ctx).Shows("rt_critic") {
		t.Fatal("a failed read dropped the administrator's selection")
	}

	settings.err = nil
	settings.value = ""
	if p.Selection(ctx).Shows("rt_critic") {
		t.Fatal("a failed read was cached; the next read did not retry")
	}

	failing := NewPolicy(&stubSettings{err: errors.New("database down")}, declaring(rtCritic))
	if sel := failing.Selection(ctx); !sel.Shows("imdb") || sel.Shows("rt_critic") {
		t.Fatal("a failed first read must answer with the default")
	}
}

func TestSelectionShownListsDefinedSourcesInDisplayOrder(t *testing.T) {
	var got []string
	sel := NewSelection("metacritic", "kinopoisk", "rt_critic").WithDeclared(declared(rtCritic, rtAudience, metacritic))
	for _, definition := range sel.Shown() {
		got = append(got, definition.Source)
	}
	// kinopoisk is turned on but no plugin declares it; the declared sources
	// follow the order they were declared in.
	want := []string{"imdb", "tmdb", "rt_critic", "metacritic"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Shown = %q, want %q", got, want)
	}
}

func TestSelectionAlwaysShowsIMDbAndTMDB(t *testing.T) {
	var p *Policy
	sel := p.Selection(context.Background())
	if !sel.Shows("imdb") || !sel.Shows("tmdb") || sel.Shows("rt_critic") {
		t.Fatal("the default selection must show IMDb and TMDB only")
	}
}

func TestPolicyReadsDeclaredSources(t *testing.T) {
	p := NewPolicy(&stubSettings{value: "kinopoisk"}, declaring(kinopoisk))

	item := Item{Sources: map[string]float64{"kinopoisk": 72}}
	if got := Build(item, p.Selection(context.Background())); len(got) != 1 || got[0].Display != "7.2" {
		t.Fatalf("Build = %+v, want the declared source", got)
	}

	sources, err := p.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range sources {
		ids = append(ids, s.Source)
	}
	if want := []string{"imdb", "tmdb", "kinopoisk"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("sources = %q, want %q: Silo lists only IMDb, TMDB and what plugins declare", ids, want)
	}
	last := sources[len(sources)-1]
	if last.Provider != "MDBList" || last.AlwaysShown {
		t.Fatalf("last source = %+v, want the plugin's declaration", last)
	}
	if !sources[0].AlwaysShown {
		t.Fatalf("first source = %+v, want IMDb, always shown", sources[0])
	}
}

// A failed read of the plugin declarations keeps the declarations last read,
// so a turned-on source does not blink off because one lookup failed.
func TestPolicyKeepsLastDeclarationsWhenARereadFails(t *testing.T) {
	now := time.Unix(0, 0)
	fail := false
	p := NewPolicy(&stubSettings{value: "rt_critic"}, func(ctx context.Context) ([]DeclaredSource, error) {
		if fail {
			return nil, errors.New("pool exhausted")
		}
		return declaring(rtCritic)(ctx)
	})
	p.now = func() time.Time { return now }
	if !p.Selection(context.Background()).Shows("rt_critic") {
		t.Fatal("rt_critic is declared and turned on")
	}
	now = now.Add(cacheTTL)
	fail = true
	if !p.Selection(context.Background()).Shows("rt_critic") {
		t.Fatal("a failed declaration read dropped a source the administrator turned on")
	}
}

// A failed read of the plugin declarations before any succeeded is not
// cached, so one failed lookup does not hide every plugin rating until the
// cache expires.
func TestPolicyRetriesDeclarationsNeverRead(t *testing.T) {
	fail := true
	reads := 0
	p := NewPolicy(&stubSettings{value: "rt_critic"}, func(ctx context.Context) ([]DeclaredSource, error) {
		reads++
		if fail {
			return nil, errors.New("pool exhausted")
		}
		return declaring(rtCritic)(ctx)
	})
	now := time.Unix(0, 0)
	p.now = func() time.Time { return now }
	if p.Selection(context.Background()).Shows("rt_critic") {
		t.Fatal("rt_critic shows without a declaration read")
	}
	fail = false
	if !p.Selection(context.Background()).Shows("rt_critic") {
		t.Fatal("the failed first read was cached; the next call did not retry")
	}
	p.Selection(context.Background())
	if reads != 2 {
		t.Fatalf("declaration reads = %d, want 2: the successful read is cached", reads)
	}
}

// The cache is shared by every request on the node, so a request that goes
// away mid-read must not fail the read for the others.
func TestPolicyReadIgnoresTheCallersCancellation(t *testing.T) {
	p := NewPolicy(&stubSettings{value: "rt_critic"}, func(ctx context.Context) ([]DeclaredSource, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return declaring(rtCritic)(ctx)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !p.Selection(ctx).Shows("rt_critic") {
		t.Fatal("a canceled request context failed the shared read")
	}
}

func TestSelectionSourcesKeepsShownSourcesInDisplayOrder(t *testing.T) {
	sel := NewSelection("kinopoisk", "rt_critic", "metacritic").WithDeclared(declared(rtCritic, kinopoisk))
	got := sel.Sources([]string{"kinopoisk", "metacritic", "tmdb", "rt_critic", "letterboxd", "imdb"})
	if want := []string{"imdb", "tmdb", "rt_critic", "kinopoisk"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Sources = %q, want %q", got, want)
	}
}
