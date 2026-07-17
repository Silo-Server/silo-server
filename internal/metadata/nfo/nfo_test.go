package nfo

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// fullMovieNFO is a realistic Kodi/Jellyfin-style export exercising the whole
// Phase-B field set: repeated collections, multi-source ratings, cast with
// role/order, directors and writers, and legacy + modern rating forms.
const fullMovieNFO = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>Blade Runner</title>
  <originaltitle>Blade Runner: The Original</originaltitle>
  <sorttitle>Blade Runner 1</sorttitle>
  <tagline>Man has made his match</tagline>
  <plot>A blade runner must pursue replicants.</plot>
  <runtime>117</runtime>
  <premiered>1982-06-25</premiered>
  <mpaa>R</mpaa>
  <genre>Science Fiction</genre>
  <genre>Thriller</genre>
  <studio>Warner Bros.</studio>
  <studio>The Ladd Company</studio>
  <country>United States</country>
  <country>Hong Kong</country>
  <tag>dystopia</tag>
  <tag>neo-noir</tag>
  <userrating>9</userrating>
  <ratings>
    <rating name="imdb" max="10" default="true">
      <value>8.1</value>
      <votes>700000</votes>
    </rating>
    <rating name="themoviedb" max="10">
      <value>7.9</value>
    </rating>
    <rating name="tomatometerallcritics" max="100">
      <value>89</value>
    </rating>
    <rating name="tomatometerallaudience" max="100">
      <value>91</value>
    </rating>
  </ratings>
  <actor>
    <name>Harrison Ford</name>
    <role>Rick Deckard</role>
    <order>0</order>
    <thumb>https://example.com/ford.jpg</thumb>
  </actor>
  <actor>
    <name>Rutger Hauer</name>
    <role>Roy Batty</role>
    <order>1</order>
  </actor>
  <director>Ridley Scott</director>
  <credits>Hampton Fancher</credits>
  <credits>David Peoples</credits>
  <uniqueid type="imdb">tt0083658</uniqueid>
  <uniqueid type="tmdb" default="true">78</uniqueid>
  <set>
    <name>Blade Runner Collection</name>
  </set>
  <fileinfo>
    <streamdetails>
      <video><codec>h264</codec></video>
    </streamdetails>
  </fileinfo>
</movie>`

// fullTVShowNFO exercises the series root, including <aired> as the
// FirstAirDate fallback and series-shaped collections.
const fullTVShowNFO = `<?xml version="1.0" encoding="utf-8"?>
<tvshow>
  <title>Mr. Robot</title>
  <originaltitle>Mr. Robot (Original)</originaltitle>
  <plot>A cybersecurity engineer by day, hacker by night.</plot>
  <runtime>49</runtime>
  <premiered>2015-06-24</premiered>
  <mpaa>TV-MA</mpaa>
  <genre>Drama</genre>
  <genre>Crime</genre>
  <studio>USA Network</studio>
  <tag>hacking</tag>
  <ratings>
    <rating name="imdb" max="10">
      <value>8.5</value>
    </rating>
  </ratings>
  <actor>
    <name>Rami Malek</name>
    <role>Elliot Alderson</role>
    <order>0</order>
  </actor>
  <uniqueid type="tvdb" default="true">289590</uniqueid>
</tvshow>`

func TestParseNFOData_FullMovie(t *testing.T) {
	t.Parallel()

	p, err := parseNFOData([]byte(fullMovieNFO))
	if err != nil {
		t.Fatalf("parseNFOData() error = %v", err)
	}
	if p.Type != "movie" {
		t.Errorf("Type = %q, want movie", p.Type)
	}
	if p.Title != "Blade Runner" {
		t.Errorf("Title = %q", p.Title)
	}
	if p.OriginalTitle != "Blade Runner: The Original" {
		t.Errorf("OriginalTitle = %q", p.OriginalTitle)
	}
	if p.Tagline != "Man has made his match" {
		t.Errorf("Tagline = %q", p.Tagline)
	}
	if p.Runtime != 117 {
		t.Errorf("Runtime = %d, want 117", p.Runtime)
	}
	if p.ReleaseDate != "1982-06-25" {
		t.Errorf("ReleaseDate = %q, want 1982-06-25", p.ReleaseDate)
	}
	if p.FirstAirDate != "" {
		t.Errorf("FirstAirDate = %q, want empty on a movie", p.FirstAirDate)
	}
	if p.Year != 1982 {
		t.Errorf("Year = %d, want 1982 (derived from premiered)", p.Year)
	}
	if p.ContentRating != "R" {
		t.Errorf("ContentRating = %q, want R", p.ContentRating)
	}
	if want := []string{"Science Fiction", "Thriller"}; !reflect.DeepEqual(p.Genres, want) {
		t.Errorf("Genres = %#v, want %#v", p.Genres, want)
	}
	if want := []string{"Warner Bros.", "The Ladd Company"}; !reflect.DeepEqual(p.Studios, want) {
		t.Errorf("Studios = %#v, want %#v", p.Studios, want)
	}
	if want := []string{"United States", "Hong Kong"}; !reflect.DeepEqual(p.Countries, want) {
		t.Errorf("Countries = %#v, want %#v", p.Countries, want)
	}
	if want := []string{"dystopia", "neo-noir"}; !reflect.DeepEqual(p.Keywords, want) {
		t.Errorf("Keywords = %#v, want %#v", p.Keywords, want)
	}
	if p.RatingIMDB != 8.1 {
		t.Errorf("RatingIMDB = %v, want 8.1", p.RatingIMDB)
	}
	if p.RatingTMDB != 7.9 {
		t.Errorf("RatingTMDB = %v, want 7.9", p.RatingTMDB)
	}
	if p.RatingRTCritic != 89 {
		t.Errorf("RatingRTCritic = %v, want 89", p.RatingRTCritic)
	}
	if p.RatingRTAudience != 91 {
		t.Errorf("RatingRTAudience = %v, want 91", p.RatingRTAudience)
	}
	wantPeople := []models.ItemPerson{
		{Person: models.Person{Name: "Harrison Ford"}, Kind: models.PersonKindActor, Character: "Rick Deckard", SortOrder: 0},
		{Person: models.Person{Name: "Rutger Hauer"}, Kind: models.PersonKindActor, Character: "Roy Batty", SortOrder: 1},
		{Person: models.Person{Name: "Ridley Scott"}, Kind: models.PersonKindDirector, SortOrder: 0},
		{Person: models.Person{Name: "Hampton Fancher"}, Kind: models.PersonKindWriter, SortOrder: 0},
		{Person: models.Person{Name: "David Peoples"}, Kind: models.PersonKindWriter, SortOrder: 1},
	}
	if !reflect.DeepEqual(p.People, wantPeople) {
		t.Errorf("People = %#v, want %#v", p.People, wantPeople)
	}
	if p.ImdbID != "tt0083658" || p.TmdbID != "78" {
		t.Errorf("ids = imdb %q tmdb %q", p.ImdbID, p.TmdbID)
	}
}

func TestParseNFOData_FullTVShow(t *testing.T) {
	t.Parallel()

	p, err := parseNFOData([]byte(fullTVShowNFO))
	if err != nil {
		t.Fatalf("parseNFOData() error = %v", err)
	}
	if p.Type != "series" {
		t.Errorf("Type = %q, want series", p.Type)
	}
	if p.FirstAirDate != "2015-06-24" {
		t.Errorf("FirstAirDate = %q, want 2015-06-24", p.FirstAirDate)
	}
	if p.ReleaseDate != "" {
		t.Errorf("ReleaseDate = %q, want empty on a series", p.ReleaseDate)
	}
	if p.Year != 2015 {
		t.Errorf("Year = %d, want 2015 (derived from premiered)", p.Year)
	}
	if p.ContentRating != "TV-MA" {
		t.Errorf("ContentRating = %q, want TV-MA", p.ContentRating)
	}
	if want := []string{"Drama", "Crime"}; !reflect.DeepEqual(p.Genres, want) {
		t.Errorf("Genres = %#v, want %#v", p.Genres, want)
	}
	if p.RatingIMDB != 8.5 {
		t.Errorf("RatingIMDB = %v, want 8.5", p.RatingIMDB)
	}
	if len(p.People) != 1 || p.People[0].Name != "Rami Malek" || p.People[0].Character != "Elliot Alderson" {
		t.Errorf("People = %#v", p.People)
	}
	if p.TvdbID != "289590" {
		t.Errorf("TvdbID = %q", p.TvdbID)
	}
}

func TestParseNFOData_TVShowAiredFallsBackToFirstAirDate(t *testing.T) {
	t.Parallel()

	p, err := parseNFOData([]byte(`<tvshow><title>Show</title><aired>2010-09-01</aired></tvshow>`))
	if err != nil {
		t.Fatalf("parseNFOData() error = %v", err)
	}
	if p.FirstAirDate != "2010-09-01" {
		t.Errorf("FirstAirDate = %q, want 2010-09-01 (from <aired>)", p.FirstAirDate)
	}
}

func TestParseNFOData_MovieReleaseDateFallback(t *testing.T) {
	t.Parallel()

	p, err := parseNFOData([]byte(`<movie><title>M</title><releasedate>1999-03-31</releasedate></movie>`))
	if err != nil {
		t.Fatalf("parseNFOData() error = %v", err)
	}
	if p.ReleaseDate != "1999-03-31" {
		t.Errorf("ReleaseDate = %q, want 1999-03-31 (from <releasedate>)", p.ReleaseDate)
	}
}

func TestParseNFOData_TableCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		data    string
		wantErr bool
		check   func(t *testing.T, p *parsedNFO)
	}{
		{
			name: "BOM prefix retained",
			data: "\xEF\xBB\xBF<movie><title>BOM Movie</title></movie>",
			check: func(t *testing.T, p *parsedNFO) {
				if p.Title != "BOM Movie" {
					t.Errorf("Title = %q", p.Title)
				}
			},
		},
		{
			name:    "malformed XML is an error (no NFO)",
			data:    `<movie><title>Broken`,
			wantErr: true,
		},
		{
			name:    "unsupported root element",
			data:    `<musicvideo><title>x</title></musicvideo>`,
			wantErr: true,
		},
		{
			name: "unknown elements ignored",
			data: `<movie><title>X</title><bogusfield>zzz</bogusfield><another attr="1"/></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.Title != "X" {
					t.Errorf("Title = %q", p.Title)
				}
			},
		},
		{
			name: "empty collections stay nil (no placeholder emissions)",
			data: `<movie><title>Bare</title><genre></genre><studio>  </studio><tag/></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.Genres != nil {
					t.Errorf("Genres = %#v, want nil", p.Genres)
				}
				if p.Studios != nil {
					t.Errorf("Studios = %#v, want nil", p.Studios)
				}
				if p.Countries != nil {
					t.Errorf("Countries = %#v, want nil", p.Countries)
				}
				if p.Keywords != nil {
					t.Errorf("Keywords = %#v, want nil", p.Keywords)
				}
				if p.People != nil {
					t.Errorf("People = %#v, want nil", p.People)
				}
			},
		},
		{
			name: "non-numeric runtime ignored",
			data: `<movie><title>X</title><runtime>two hours</runtime></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.Runtime != 0 {
					t.Errorf("Runtime = %d, want 0", p.Runtime)
				}
			},
		},
		{
			name: "runtime with surrounding whitespace",
			data: `<movie><title>X</title><runtime> 95 </runtime></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.Runtime != 95 {
					t.Errorf("Runtime = %d, want 95", p.Runtime)
				}
			},
		},
		{
			name: "non-date premiered ignored",
			data: `<movie><title>X</title><premiered>next summer</premiered></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.ReleaseDate != "" {
					t.Errorf("ReleaseDate = %q, want empty", p.ReleaseDate)
				}
			},
		},
		{
			name: "explicit year wins over premiered-derived year",
			data: `<movie><title>X</title><year>1981</year><premiered>1982-06-25</premiered></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.Year != 1981 {
					t.Errorf("Year = %d, want 1981", p.Year)
				}
			},
		},
		{
			name: "legacy bare rating fills the imdb slot",
			data: `<movie><title>X</title><rating>7.4</rating></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.RatingIMDB != 7.4 {
					t.Errorf("RatingIMDB = %v, want 7.4", p.RatingIMDB)
				}
			},
		},
		{
			name: "named imdb rating wins over legacy bare rating",
			data: `<movie><title>X</title><rating>6.0</rating><ratings><rating name="imdb" max="10"><value>8.2</value></rating></ratings></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.RatingIMDB != 8.2 {
					t.Errorf("RatingIMDB = %v, want 8.2", p.RatingIMDB)
				}
			},
		},
		{
			name: "ratings normalized to their slot scale",
			data: `<movie><title>X</title><ratings>` +
				`<rating name="imdb" max="5"><value>4.0</value></rating>` +
				`<rating name="tomatometerallcritics" max="10"><value>9</value></rating>` +
				`</ratings></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.RatingIMDB != 8.0 {
					t.Errorf("RatingIMDB = %v, want 8.0 (scaled from max=5)", p.RatingIMDB)
				}
				if p.RatingRTCritic != 90 {
					t.Errorf("RatingRTCritic = %v, want 90 (scaled from max=10)", p.RatingRTCritic)
				}
			},
		},
		{
			name: "userrating alone emits no ratings",
			data: `<movie><title>X</title><userrating>9</userrating></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.RatingIMDB != 0 || p.RatingTMDB != 0 || p.RatingRTCritic != 0 || p.RatingRTAudience != 0 {
					t.Errorf("ratings = %v/%v/%v/%v, want all zero", p.RatingIMDB, p.RatingTMDB, p.RatingRTCritic, p.RatingRTAudience)
				}
			},
		},
		{
			name: "actor without order gets file order",
			data: `<movie><title>X</title>` +
				`<actor><name>A One</name><role>R1</role></actor>` +
				`<actor><name>B Two</name></actor></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				want := []models.ItemPerson{
					{Person: models.Person{Name: "A One"}, Kind: models.PersonKindActor, Character: "R1", SortOrder: 0},
					{Person: models.Person{Name: "B Two"}, Kind: models.PersonKindActor, SortOrder: 1},
				}
				if !reflect.DeepEqual(p.People, want) {
					t.Errorf("People = %#v, want %#v", p.People, want)
				}
			},
		},
		{
			name: "actor without a name is dropped",
			data: `<movie><title>X</title><actor><role>Ghost</role></actor></movie>`,
			check: func(t *testing.T, p *parsedNFO) {
				if p.People != nil {
					t.Errorf("People = %#v, want nil", p.People)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := parseNFOData([]byte(tc.data))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseNFOData() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNFOData() error = %v", err)
			}
			if tc.check != nil {
				tc.check(t, p)
			}
		})
	}
}
