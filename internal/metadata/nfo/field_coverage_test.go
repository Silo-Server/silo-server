package nfo

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

func mustParse(t *testing.T, data string) *parsedNFO {
	t.Helper()
	p, err := parseNFOData([]byte(data))
	if err != nil {
		t.Fatalf("parseNFOData() error = %v", err)
	}
	return p
}

func TestParseNFOData_MovieScalarFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		data  string
		check func(t *testing.T, p *parsedNFO)
	}{
		{
			name: "sorttitle",
			data: `<movie><title>The Matrix</title><sorttitle>Matrix 1</sorttitle></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.SortTitle != "Matrix 1" {
					t.Errorf("SortTitle = %q", p.SortTitle)
				}
			},
		},
		{
			name: "outline when plot is absent",
			data: `<movie><title>M</title><outline>Short version.</outline></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.Overview != "Short version." {
					t.Errorf("Overview = %q, want the outline", p.Overview)
				}
			},
		},
		{
			name: "plot wins over outline",
			data: `<movie><title>M</title><outline>Short.</outline><plot>Long.</plot></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.Overview != "Long." {
					t.Errorf("Overview = %q, want the plot", p.Overview)
				}
			},
		},
		{
			name: "certification when mpaa is absent",
			data: `<movie><title>M</title><certification>PG-13</certification></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.ContentRating != "PG-13" {
					t.Errorf("ContentRating = %q, want PG-13", p.ContentRating)
				}
			},
		},
		{
			name: "certification lists ratings by country",
			data: `<movie><title>M</title><certification>US:PG-13 / GB:12A</certification></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.ContentRating != "PG-13" {
					t.Errorf("ContentRating = %q, want PG-13", p.ContentRating)
				}
			},
		},
		{
			name: "mpaa wins over certification",
			data: `<movie><title>M</title><certification>US:PG</certification><mpaa>R</mpaa></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.ContentRating != "R" {
					t.Errorf("ContentRating = %q, want R", p.ContentRating)
				}
			},
		},
		{
			name: "original language code",
			data: `<movie><title>M</title><originallanguage>fra</originallanguage></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.OriginalLanguage != "fr" {
					t.Errorf("OriginalLanguage = %q, want fr", p.OriginalLanguage)
				}
			},
		},
		{
			name: "original language display name is dropped",
			data: `<movie><title>M</title><originallanguage>English</originallanguage></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.OriginalLanguage != "" {
					t.Errorf("OriginalLanguage = %q, want empty", p.OriginalLanguage)
				}
			},
		},
		{
			name: "language is the metadata language, not the original",
			data: `<movie><title>M</title><language>de</language></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.OriginalLanguage != "" {
					t.Errorf("OriginalLanguage = %q, want empty", p.OriginalLanguage)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.check(t, mustParse(t, tt.data))
		})
	}
}

func TestParseRuntimeMinutes(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]int{
		"117":         117,
		" 117 ":       117,
		"117 min":     117,
		"117 mins":    117,
		"117 Minutes": 117,
		"117min":      117,
		"1h 57m":      117,
		"2h":          120,
		"0":           0,
		"":            0,
		"long":        0,
		"117.5":       0,
		"1:57:00":     0,
	} {
		if got := parseRuntimeMinutes(raw); got != want {
			t.Errorf("parseRuntimeMinutes(%q) = %d, want %d", raw, got, want)
		}
	}
	if p := mustParse(t, `<movie><title>M</title><runtime>117 min</runtime></movie>`); p.Runtime != 117 {
		t.Errorf("movie Runtime = %d, want 117", p.Runtime)
	}
	if p := mustParse(t, `<episodedetails><title>E</title><runtime>45 min</runtime></episodedetails>`); p.Runtime != 45 {
		t.Errorf("episode Runtime = %d, want 45", p.Runtime)
	}
}

// Kodi joins several values in one list element with " / ".
func TestParseNFOData_SplitsKodiListSeparator(t *testing.T) {
	t.Parallel()

	p := mustParse(t, `<movie>
  <title>M</title>
  <genre>Action / Comedy</genre>
  <genre>Comedy</genre>
  <genre>Sci-Fi/Fantasy</genre>
  <studio>Studio A / Studio B</studio>
  <country>United States / Canada</country>
  <director>Lana Wachowski / Lilly Wachowski</director>
  <credits>Writer One / Writer Two</credits>
  <writer>Writer Two</writer>
  <writer>Writer Three</writer>
  <tag>a / b</tag>
</movie>`)
	if want := []string{"Action", "Comedy", "Sci-Fi/Fantasy"}; !reflect.DeepEqual(p.Genres, want) {
		t.Errorf("Genres = %#v, want %#v", p.Genres, want)
	}
	if want := []string{"Studio A", "Studio B"}; !reflect.DeepEqual(p.Studios, want) {
		t.Errorf("Studios = %#v, want %#v", p.Studios, want)
	}
	if want := []string{"United States", "Canada"}; !reflect.DeepEqual(p.Countries, want) {
		t.Errorf("Countries = %#v, want %#v", p.Countries, want)
	}
	if want := []string{"a / b"}; !reflect.DeepEqual(p.Keywords, want) {
		t.Errorf("Keywords = %#v, want %#v (tags are not split)", p.Keywords, want)
	}
	var directors, writers []string
	for _, person := range p.People {
		switch person.Kind {
		case models.PersonKindDirector:
			directors = append(directors, person.Name)
		case models.PersonKindWriter:
			writers = append(writers, person.Name)
		}
	}
	if want := []string{"Lana Wachowski", "Lilly Wachowski"}; !reflect.DeepEqual(directors, want) {
		t.Errorf("directors = %#v, want %#v", directors, want)
	}
	// <credits> and <writer> are both writers; a name in both is one credit.
	if want := []string{"Writer One", "Writer Two", "Writer Three"}; !reflect.DeepEqual(writers, want) {
		t.Errorf("writers = %#v, want %#v", writers, want)
	}
	if p.Networks != nil {
		t.Errorf("movie Networks = %#v, want nil", p.Networks)
	}
}

func TestParseNFOData_TVShowStatusNetworksAndNamedSeasons(t *testing.T) {
	t.Parallel()

	p := mustParse(t, `<tvshow>
  <title>Show</title>
  <status>Continuing</status>
  <studio>HBO / Max</studio>
  <namedseason number="1">The Beginning</namedseason>
  <namedseason number="2"> </namedseason>
  <namedseason number="x">Bad</namedseason>
  <namedseason number="0">Extras</namedseason>
  <namedseason number="1">Duplicate</namedseason>
</tvshow>`)
	if p.ShowStatus != "returning" {
		t.Errorf("ShowStatus = %q, want returning", p.ShowStatus)
	}
	if want := []string{"HBO", "Max"}; !reflect.DeepEqual(p.Networks, want) || !reflect.DeepEqual(p.Studios, want) {
		t.Errorf("Networks/Studios = %#v / %#v, want %#v for both", p.Networks, p.Studios, want)
	}
	if want := map[int]string{0: "Extras", 1: "The Beginning"}; !reflect.DeepEqual(p.SeasonNames, want) {
		t.Errorf("SeasonNames = %#v, want %#v", p.SeasonNames, want)
	}
	if ended := mustParse(t, `<tvshow><title>S</title><status>Ended</status></tvshow>`); ended.ShowStatus != "ended" {
		t.Errorf("ShowStatus = %q, want ended", ended.ShowStatus)
	}
}

func TestParseNFOData_Trailers(t *testing.T) {
	t.Parallel()

	p := mustParse(t, `<movie>
  <title>M</title>
  <trailer>plugin://plugin.video.youtube/?action=play_video&amp;videoid=dQw4w9WgXcQ</trailer>
  <trailer>plugin://plugin.video.youtube/play/?video_id=aaaaaaaaaaa</trailer>
  <trailer>https://www.youtube.com/watch?v=dQw4w9WgXcQ</trailer>
  <trailer>https://youtu.be/bbbbbbbbbbb</trailer>
  <trailer>https://www.youtube.com/embed/ccccccccccc</trailer>
  <trailer>https://vimeo.com/123456</trailer>
  <trailer>https://example.com/trailer.mp4</trailer>
  <trailer>plugin://plugin.video.youtube/?action=play_video&amp;videoid=short</trailer>
  <trailer>/media/Movies/M/M-trailer.mkv</trailer>
</movie>`)
	var got [][2]string
	for _, v := range p.Videos {
		if v.Provider != "nfo" || v.Kind != models.ExtraKindTrailer || v.ProviderKey != v.Site+":"+v.SiteKey {
			t.Errorf("video %#v, want an nfo trailer keyed by site", v)
		}
		got = append(got, [2]string{v.Site, v.SiteKey})
	}
	want := [][2]string{
		{"youtube", "dQw4w9WgXcQ"},
		{"youtube", "aaaaaaaaaaa"},
		{"youtube", "bbbbbbbbbbb"},
		{"youtube", "ccccccccccc"},
		{"vimeo", "123456"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("trailers = %v, want %v", got, want)
	}
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
			name: "named imdb wins over the legacy value",
			data: `<movie><rating>5</rating><ratings>
  <rating name="imdb" default="true"><value>8.1</value><votes>700000</votes></rating>
</ratings></movie>`,
			wantIMDB:    8.1,
			wantSources: map[string]metadata.RatingSource{"imdb": {Score: 81, Votes: 700000}},
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

func TestParseNFOData_ActorFields(t *testing.T) {
	t.Parallel()

	p := mustParse(t, `<tvshow>
  <title>Show</title>
  <actor>
    <name>Lead</name><role>Hero</role><order>1</order>
    <thumb>.actors/Lead.jpg</thumb>
    <tmdbid>17419</tmdbid><imdbid>nm0000148</imdbid><tvdbid>312</tvdbid>
  </actor>
  <actor>
    <name>Visitor</name><role>Stranger</role><type>GuestStar</type><sortorder>5</sortorder>
    <tmdbid>tt-not-a-number</tmdbid><imdbid>tt0083658</imdbid>
  </actor>
  <actor><name>Boss</name><role>ignored</role><type>Director</type></actor>
  <actor><name>Unknown Kind</name><type>Puppeteer</type></actor>
</tvshow>`)
	want := []models.ItemPerson{
		{
			Person:           models.Person{Name: "Lead", TmdbID: "17419", ImdbID: "nm0000148", TvdbID: "312"},
			Kind:             models.PersonKindActor,
			Character:        "Hero",
			SortOrder:        1,
			LocalPhotoSource: ".actors/Lead.jpg",
		},
		{
			Person:    models.Person{Name: "Visitor"},
			Kind:      models.PersonKindGuestStar,
			Character: "Stranger",
			SortOrder: 5,
		},
		{Person: models.Person{Name: "Boss"}, Kind: models.PersonKindDirector, SortOrder: 2},
		{Person: models.Person{Name: "Unknown Kind"}, Kind: models.PersonKindActor, SortOrder: 3},
	}
	if !reflect.DeepEqual(p.People, want) {
		t.Errorf("People = %#v\nwant %#v", p.People, want)
	}
}

func TestParseNFOData_ArtReferences(t *testing.T) {
	t.Parallel()

	p := mustParse(t, `<tvshow>
  <title>Show</title>
  <thumb>plain.jpg</thumb>
  <thumb>http://thetvdb.com/banners/graphical/80348-g.jpg</thumb>
  <thumb type="season" season="1">http://thetvdb.com/banners/seasonswide/80348-1.jpg</thumb>
  <thumb aspect="poster" preview="https://img.example.org/w500/p.jpg">https://img.example.org/original/p.jpg</thumb>
  <thumb aspect="ClearLogo">logo.png</thumb>
  <thumb aspect="banner">banner.jpg</thumb>
  <thumb aspect="landscape">landscape.jpg</thumb>
  <thumb aspect="poster" type="season" season="1">season01.jpg</thumb>
  <thumb aspect="poster" type="season" season="-1">all-seasons.jpg</thumb>
  <thumb aspect="poster" season="0">specials.jpg</thumb>
  <thumb aspect="characterart">ignored.png</thumb>
  <fanart url="https://img.example.org/original">
    <thumb preview="/w780/a.jpg">/a.jpg</thumb>
    <thumb>https://other.example.org/b.jpg</thumb>
  </fanart>
</tvshow>`)
	// Thumbs that name their aspect come first; an aspect-less thumb is a
	// poster unless it is a TVDB banner, which older Kodi exports list
	// without an aspect.
	want := []nfoArtRef{
		{Kind: artPoster, Ref: "https://img.example.org/original/p.jpg"},
		{Kind: artLogo, Ref: "logo.png"},
		{Kind: artBanner, Ref: "banner.jpg"},
		{Kind: artLandscape, Ref: "landscape.jpg"},
		{Kind: artPoster, Season: 1, SeasonSet: true, Ref: "season01.jpg"},
		{Kind: artPoster, Season: 0, SeasonSet: true, Ref: "specials.jpg"},
		{Kind: artPoster, Ref: "plain.jpg"},
		{Kind: artBackdrop, Ref: "https://img.example.org/original/a.jpg"},
		{Kind: artBackdrop, Ref: "https://other.example.org/b.jpg"},
	}
	if !reflect.DeepEqual(p.Art, want) {
		t.Errorf("Art = %#v\nwant %#v", p.Art, want)
	}
}

func TestParseEpisodeNFO_DateFallbacksAndThumb(t *testing.T) {
	t.Parallel()

	p := mustParse(t, `<episodedetails>
  <title>E</title>
  <premiered>2020-02-02</premiered>
  <releasedate>2020-03-03</releasedate>
  <outline>Outline only.</outline>
  <thumb>E-still.jpg</thumb>
  <uniqueid type="tvdb" default="true">7654321</uniqueid>
  <uniqueid type="imdb">tt1234567</uniqueid>
</episodedetails>`)
	if p.FirstAirDate != "2020-02-02" {
		t.Errorf("FirstAirDate = %q, want the premiered date", p.FirstAirDate)
	}
	if p.Overview != "Outline only." {
		t.Errorf("Overview = %q, want the outline", p.Overview)
	}
	if want := []nfoArtRef{{Kind: artStill, Ref: "E-still.jpg"}}; !reflect.DeepEqual(p.Art, want) {
		t.Errorf("Art = %#v, want %#v", p.Art, want)
	}
	if want := map[string]string{"tvdb": "7654321", "imdb": "tt1234567"}; !reflect.DeepEqual(p.providerIDs(), want) {
		t.Errorf("providerIDs = %#v, want %#v", p.providerIDs(), want)
	}
	release := mustParse(t, `<episodedetails><title>E</title><releasedate>2020-03-03</releasedate></episodedetails>`)
	if release.FirstAirDate != "2020-03-03" {
		t.Errorf("FirstAirDate = %q, want the release date", release.FirstAirDate)
	}
	aired := mustParse(t, `<episodedetails><aired>2020-01-01</aired><premiered>2020-02-02</premiered></episodedetails>`)
	if aired.FirstAirDate != "2020-01-01" {
		t.Errorf("FirstAirDate = %q, want <aired> first", aired.FirstAirDate)
	}
}

func TestParseSeasonNFO_NameAndAirDate(t *testing.T) {
	t.Parallel()

	p := mustParse(t, `<season><seasonname>Book One</seasonname><premiered>2005-02-21</premiered></season>`)
	if p.Title != "Book One" || p.FirstAirDate != "2005-02-21" {
		t.Errorf("season = %q / %q, want Book One / 2005-02-21", p.Title, p.FirstAirDate)
	}
	both := mustParse(t, `<season><title>Title</title><seasonname>Name</seasonname><aired>2006-03-17</aired></season>`)
	if both.Title != "Title" || both.FirstAirDate != "2006-03-17" {
		t.Errorf("season = %q / %q, want Title / 2006-03-17", both.Title, both.FirstAirDate)
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

// Jellyfin writes a director or writer both as a typed <actor>, with their
// ids and photo, and in <director> or <writer>. That is one credit, the one
// carrying the ids; the same name in another role is still its own credit.
func TestParseNFOData_TypedActorsAndCrewTagsAreOneCredit(t *testing.T) {
	t.Parallel()

	p := mustParse(t, `<movie>
  <title>M</title>
  <actor><name>Ann Director</name><type>Director</type><tmdbid>11</tmdbid><thumb>https://img.example.org/ann.jpg</thumb></actor>
  <actor><name>Will Writer</name><type>Writer</type><imdbid>nm0000022</imdbid></actor>
  <actor><name>Ann Director</name><role>Cameo</role></actor>
  <director>ann director</director>
  <director>Other Director</director>
  <writer>Will Writer</writer>
  <credits>Will Writer / New Writer</credits>
</movie>`)
	type credit struct {
		name string
		kind models.PersonKind
		tmdb string
		imdb string
	}
	var got []credit
	for _, person := range p.People {
		got = append(got, credit{person.Name, person.Kind, person.TmdbID, person.ImdbID})
	}
	want := []credit{
		{"Ann Director", models.PersonKindDirector, "11", ""},
		{"Will Writer", models.PersonKindWriter, "", "nm0000022"},
		{"Ann Director", models.PersonKindActor, "", ""},
		{"Other Director", models.PersonKindDirector, "", ""},
		{"New Writer", models.PersonKindWriter, "", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("credits = %+v\nwant %+v", got, want)
	}
}
