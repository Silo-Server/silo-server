package ratingsources

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func ptr[T any](v T) *T { return &v }

func TestBuildShowsIMDbAndTMDBByDefault(t *testing.T) {
	item := Item{
		IMDB: ptr(8.5), TMDB: ptr(8.25), RTCritic: ptr(93), RTAudience: ptr(95),
		Sources: map[string]float64{models.RatingSourceMetacritic: 87},
	}
	got := Build(item, Selection{})
	want := []Rating{
		{Source: "imdb", Name: "IMDb", Score: 85, Display: "8.5"},
		{Source: "tmdb", Name: "TMDB", Score: 82.5, Display: "8.3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v\nwant %+v", got, want)
	}
}

func TestBuildAddsSourcesAnAdministratorTurnedOn(t *testing.T) {
	item := Item{
		IMDB: ptr(8.5), RTCritic: ptr(93), RTAudience: ptr(95),
		Sources: map[string]float64{
			models.RatingSourceLetterboxd: 84,
			models.RatingSourceMetacritic: 87,
			models.RatingSourceTrakt:      81,
		},
	}
	got := Build(item, NewSelection("rt_critic", "rt_audience", "metacritic", "letterboxd"))
	want := []Rating{
		{Source: "imdb", Name: "IMDb", Score: 85, Display: "8.5"},
		{Source: "rt_critic", Name: "RT", Score: 93, Display: "93%"},
		{Source: "rt_audience", Name: "RT Audience", Score: 95, Display: "95%"},
		{Source: "metacritic", Name: "Metacritic", Score: 87, Display: "87"},
		{Source: "letterboxd", Name: "Letterboxd", Score: 84, Display: "4.2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v\nwant %+v", got, want)
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
	if got := Build(item, NewSelection("rt_critic")); len(got) != 0 {
		t.Fatalf("Build = %+v, want nothing for out-of-scale values", got)
	}
}

func TestBuildTreatsAZeroIMDbOrTMDBRowAsUnrated(t *testing.T) {
	item := Item{Sources: map[string]float64{models.RatingSourceIMDB: 0, models.RatingSourceTMDB: 0, models.RatingSourceRTCritic: 0}}
	got := Build(item, NewSelection("rt_critic"))
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
	def := func(source string) models.RatingSourceDefinition {
		d, ok := models.LookupRatingSource(source)
		if !ok {
			t.Fatalf("no definition for %s", source)
		}
		return d
	}
	cases := []struct {
		source string
		score  float64
		want   string
	}{
		{"imdb", 82.5, "8.3"},
		{"imdb", 80, "8.0"},
		// Halves round away from zero, as the web's card formatting does.
		{"imdb", 73.5, "7.4"},
		{"tmdb", 70.5, "7.1"},
		{"rt_critic", 0, "0%"},
		{"rt_audience", 99.6, "100%"},
		{"metacritic", 87.4, "87"},
		{"metacritic_user", 81, "8.1"},
		{"letterboxd", 84, "4.2"},
		{"rogerebert", 87.5, "3.5"},
		{"trakt", 80.6, "81%"},
		{"mdblist", 77, "77"},
	}
	for _, tc := range cases {
		if got := Format(tc.score, def(tc.source)); got != tc.want {
			t.Errorf("Format(%v, %s) = %q, want %q", tc.score, tc.source, got, tc.want)
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

// The cache itself (TTL, last good value on a failed read, no cached
// failure) is config.CachedSetting's and is tested there.
func TestPolicyFollowsTheSettingAndCachesIt(t *testing.T) {
	settings := &stubSettings{value: "rt_critic"}
	p := NewPolicy(settings)
	ctx := context.Background()

	if !p.Selection(ctx).Shows("rt_critic") || p.Selection(ctx).Shows("rt_audience") {
		t.Fatal("selection does not follow the setting")
	}
	if settings.reads != 1 {
		t.Fatalf("reads = %d, want one read served from cache", settings.reads)
	}

	failing := NewPolicy(&stubSettings{err: errors.New("database down")})
	if sel := failing.Selection(ctx); !sel.Shows("imdb") || sel.Shows("rt_critic") {
		t.Fatal("a failed first read must answer with the default")
	}
}

func TestSelectionShownListsDefinedSourcesInDisplayOrder(t *testing.T) {
	var got []string
	for _, definition := range NewSelection("metacritic", "kinopoisk", "rt_critic").Shown() {
		got = append(got, definition.Source)
	}
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
