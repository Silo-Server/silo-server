package nfo

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

func mustParse(t *testing.T, data string) *parsedNFO {
	t.Helper()
	p, err := parseNFOData([]byte(data))
	if err != nil {
		t.Fatalf("parseNFOData() error = %v", err)
	}
	return p
}

func TestParseNFOData_Ratings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		data        string
		wantIMDB    float64
		wantTMDB    float64
		wantCritic  float64
		wantSources map[string]metadata.RatingSource
	}{
		{
			name:        "legacy rating alone is IMDb with its votes",
			data:        `<movie><rating>7.4</rating><votes>1,234</votes></movie>`,
			wantIMDB:    7.4,
			wantSources: map[string]metadata.RatingSource{"imdb": {Score: 74, Votes: 1234}},
		},
		{
			name: "default named rating keeps the legacy copy out of IMDb",
			data: `<movie><rating>7.6</rating><ratings>
  <rating name="themoviedb" max="10" default="true"><value>7.6</value><votes>1823</votes></rating>
</ratings></movie>`,
			wantTMDB:    7.6,
			wantSources: map[string]metadata.RatingSource{"tmdb": {Score: 76, Votes: 1823}},
		},
		{
			name: "legacy value repeating a named rating is that rating",
			data: `<movie><rating>7.6</rating><ratings>
  <rating name="themoviedb"><value>7.6</value></rating>
</ratings></movie>`,
			wantTMDB:    7.6,
			wantSources: map[string]metadata.RatingSource{"tmdb": {Score: 76}},
		},
		{
			name: "unrecognized default still says the legacy value is not IMDb",
			data: `<movie><rating>6.9</rating><ratings>
  <rating name="filmaffinity" default="true"><value>6.9</value></rating>
</ratings></movie>`,
		},
		{
			// Radarr copies its default source into the bare tag, and that
			// source is TMDB until IMDb has a score (#2311).
			name: "legacy value beside a ratings block is never IMDb",
			data: `<movie><rating>8.0</rating><ratings>
  <rating name="themoviedb"><value>7.6</value></rating>
</ratings></movie>`,
			wantTMDB:    7.6,
			wantSources: map[string]metadata.RatingSource{"tmdb": {Score: 76}},
		},
		{
			name: "declared sources on their scales, unknown names dropped",
			data: `<movie><ratings>
  <rating name="metacritic"><value>72</value></rating>
  <rating name="trakt" max="10"><value>7.5</value><votes>5000</votes></rating>
  <rating name="letterboxd"><value>4</value></rating>
  <rating name="tvdb"><value>8</value></rating>
  <rating name="tomatometerallcritics"><value>93</value></rating>
  <rating name="tomatometerallaudience" max="10"><value>8.5</value></rating>
  <rating name="filmaffinity"><value>6.9</value></rating>
</ratings></movie>`,
			wantCritic: 93,
			wantSources: map[string]metadata.RatingSource{
				"metacritic":  {Score: 72},
				"trakt":       {Score: 75, Votes: 5000},
				"letterboxd":  {Score: 80},
				"tvdb":        {Score: 80},
				"rt_critic":   {Score: 93},
				"rt_audience": {Score: 85},
			},
		},
		{
			name: "first entry for a source wins",
			data: `<movie><ratings>
  <rating name="tmdb"><value>7</value></rating>
  <rating name="themoviedb"><value>9</value></rating>
</ratings></movie>`,
			wantTMDB:    7,
			wantSources: map[string]metadata.RatingSource{"tmdb": {Score: 70}},
		},
		{
			name: "votes take thousands separators, not decimals",
			data: `<movie><ratings>
  <rating name="metacritic"><value>72</value><votes>1.234.567</votes></rating>
  <rating name="trakt"><value>7</value><votes>8.5</votes></rating>
  <rating name="letterboxd"><value>4</value><votes>1,234</votes></rating>
  <rating name="tvdb"><value>8</value><votes>1.2K</votes></rating>
</ratings></movie>`,
			wantSources: map[string]metadata.RatingSource{
				"metacritic": {Score: 72, Votes: 1234567},
				"trakt":      {Score: 70},
				"letterboxd": {Score: 80, Votes: 1234},
				"tvdb":       {Score: 80},
			},
		},
		{
			name:        "Jellyfin criticrating is the Rotten Tomatoes critics score",
			data:        `<movie><criticrating>88</criticrating></movie>`,
			wantCritic:  88,
			wantSources: map[string]metadata.RatingSource{"rt_critic": {Score: 88}},
		},
		{
			name: "named Rotten Tomatoes score wins over criticrating",
			data: `<movie><criticrating>50</criticrating><ratings>
  <rating name="tomatometerallcritics"><value>93</value></rating>
</ratings></movie>`,
			wantCritic:  93,
			wantSources: map[string]metadata.RatingSource{"rt_critic": {Score: 93}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := mustParse(t, tt.data)
			if p.RatingIMDB != tt.wantIMDB || p.RatingTMDB != tt.wantTMDB || p.RatingRTCritic != tt.wantCritic {
				t.Errorf("ratings imdb=%v tmdb=%v rt=%v, want %v/%v/%v",
					p.RatingIMDB, p.RatingTMDB, p.RatingRTCritic, tt.wantIMDB, tt.wantTMDB, tt.wantCritic)
			}
			if !reflect.DeepEqual(p.RatingSources, tt.wantSources) {
				t.Errorf("RatingSources = %#v, want %#v", p.RatingSources, tt.wantSources)
			}
		})
	}
}

// A cached parse must not share the new collections with a caller.
func TestParsedNFOCloneCopiesCollections(t *testing.T) {
	t.Parallel()

	p := mustParse(t, `<tvshow><title>S</title><studio>A</studio><namedseason number="1">One</namedseason>
<trailer>https://youtu.be/bbbbbbbbbbb</trailer><thumb>p.jpg</thumb><ratings><rating name="imdb"><value>7</value></rating></ratings></tvshow>`)
	c := p.clone()
	c.Networks[0] = "changed"
	c.Videos[0].SiteKey = "changed"
	c.Art[0].Ref = "changed"
	c.RatingSources["imdb"] = metadata.RatingSource{Score: 1}
	c.SeasonNames[1] = "changed"
	if p.Networks[0] != "A" || p.Videos[0].SiteKey != "bbbbbbbbbbb" || p.Art[0].Ref != "p.jpg" ||
		p.RatingSources["imdb"].Score != 70 || p.SeasonNames[1] != "One" {
		t.Errorf("clone shares collections with the cached parse: %#v", p)
	}
}
