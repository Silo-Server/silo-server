package nfo

import (
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
)

func latin1(t *testing.T, s string) string {
	t.Helper()
	encoded, err := charmap.ISO8859_1.NewEncoder().String(s)
	if err != nil {
		t.Fatalf("encode ISO-8859-1: %v", err)
	}
	return encoded
}

func utf16WithBOM(s string, bigEndian bool) string {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+2*len(units))
	if bigEndian {
		out = append(out, 0xFE, 0xFF)
	} else {
		out = append(out, 0xFF, 0xFE)
	}
	for _, u := range units {
		if bigEndian {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	return string(out)
}

// Real-world NFO files that Kodi and Jellyfin read must not count as absent:
// legacy charsets, HTML entities, stray ampersands, non-numeric numbers, and
// the link-only and legacy id forms.
func TestParseNFOData_RealWorldForms(t *testing.T) {
	t.Parallel()

	type want struct {
		title, overview, typ string
		year                 int
		tmdb, imdb, tvdb     string
		linkOnly             bool
	}
	tests := []struct {
		name    string
		data    func(t *testing.T) string
		want    want
		wantErr bool
	}{
		{
			name: "windows-1252 prolog",
			data: func(t *testing.T) string {
				return latin1(t, `<?xml version="1.0" encoding="windows-1252"?>`+
					`<movie><title>Amélie</title><uniqueid type="tmdb">194</uniqueid></movie>`)
			},
			want: want{title: "Amélie", typ: typeMovie, tmdb: "194"},
		},
		{
			name: "ISO-8859-1 prolog",
			data: func(t *testing.T) string {
				return latin1(t, `<?xml version="1.0" encoding="ISO-8859-1" standalone="yes"?>`+
					`<movie><title>Léon</title><plot>Mathilda à Paris</plot></movie>`)
			},
			want: want{title: "Léon", overview: "Mathilda à Paris", typ: typeMovie},
		},
		{
			name: "undeclared Latin-1 bytes read as Windows-1252",
			data: func(t *testing.T) string {
				return latin1(t, `<movie><title>Crème brûlée</title></movie>`)
			},
			want: want{title: "Crème brûlée", typ: typeMovie},
		},
		{
			name: "Latin-1 bytes under a UTF-8 prolog read as Windows-1252",
			data: func(t *testing.T) string {
				return latin1(t, `<?xml version="1.0" encoding="UTF-8"?><movie><title>Ça</title></movie>`)
			},
			want: want{title: "Ça", typ: typeMovie},
		},
		{
			name: "unknown charset label on UTF-8 text",
			data: func(*testing.T) string {
				return `<?xml version="1.0" encoding="x-made-up"?><movie><title>Zoë</title></movie>`
			},
			want: want{title: "Zoë", typ: typeMovie},
		},
		{
			name: "UTF-16 little-endian with BOM",
			data: func(*testing.T) string {
				return utf16WithBOM(`<?xml version="1.0" encoding="UTF-16"?><movie><title>Ægir</title></movie>`, false)
			},
			want: want{title: "Ægir", typ: typeMovie},
		},
		{
			name: "UTF-16 big-endian with BOM",
			data: func(*testing.T) string {
				return utf16WithBOM(`<movie><title>Ørsted</title></movie>`, true)
			},
			want: want{title: "Ørsted", typ: typeMovie},
		},
		{
			name: "UTF-8 text under a stale ISO-8859-1 declaration stays UTF-8",
			data: func(*testing.T) string {
				return `<?xml version="1.0" encoding="ISO-8859-1"?><movie><title>Amélie</title></movie>`
			},
			want: want{title: "Amélie", typ: typeMovie},
		},
		{
			name: "UTF-8 BOM outranks a legacy declaration",
			data: func(*testing.T) string {
				return "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="windows-1252"?><movie><title>Zoë</title></movie>`
			},
			want: want{title: "Zoë", typ: typeMovie},
		},
		{
			name: "UTF-8 BOM before Latin-1 bytes falls back to Windows-1252",
			data: func(t *testing.T) string { return "\xEF\xBB\xBF" + latin1(t, `<movie><title>Amélie</title></movie>`) },
			want: want{title: "Amélie", typ: typeMovie},
		},
		{
			name: "UTF-16 BOM with a UCS-2 declaration",
			data: func(*testing.T) string {
				return utf16WithBOM(`<?xml version="1.0" encoding="UCS-2"?><movie><title>Ægir</title></movie>`, false)
			},
			want: want{title: "Ægir", typ: typeMovie},
		},
		{
			name: "ASCII text under a legacy declaration",
			data: func(*testing.T) string {
				return `<?xml version="1.0" encoding="ISO-8859-1"?><movie><title>Plain &eacute;</title></movie>`
			},
			want: want{title: "Plain é", typ: typeMovie},
		},
		{
			name: "control characters XML forbids are dropped",
			data: func(*testing.T) string {
				return "\x00<movie><title>A\x0bB</title><plot>one\x08two</plot></movie>\x1a"
			},
			want: want{title: "AB", overview: "onetwo", typ: typeMovie},
		},
		{
			name: "control characters are dropped under a legacy declaration too",
			data: func(t *testing.T) string {
				return latin1(t, `<?xml version="1.0" encoding="ISO-8859-1"?><movie><title>Caf`+"\x0b"+`é</title></movie>`+"\x1a")
			},
			want: want{title: "Café", typ: typeMovie},
		},
		{
			name: "a control byte before a stale legacy declaration does not hide it",
			data: func(*testing.T) string {
				return "\x00" + `<?xml version="1.0" encoding="windows-1252"?><movie><title>Amélie</title></movie>`
			},
			want: want{title: "Amélie", typ: typeMovie},
		},
		{
			name: "a control byte before a genuine Shift_JIS declaration does not hide it",
			data: func(t *testing.T) string {
				encoded, err := japanese.ShiftJIS.NewEncoder().String(
					`<?xml version="1.0" encoding="Shift_JIS"?><movie><title>東京物語</title></movie>`)
				if err != nil {
					t.Fatalf("encode Shift_JIS: %v", err)
				}
				return "\x00" + encoded
			},
			want: want{title: "東京物語", typ: typeMovie},
		},
		{
			name: "a control character before a UTF-16 declaration does not hide it",
			data: func(*testing.T) string {
				return utf16WithBOM("\x00"+`<?xml version="1.0" encoding="UTF-16"?><movie><title>Ægir</title></movie>`, false)
			},
			want: want{title: "Ægir", typ: typeMovie},
		},
		{
			name: "an HTML <br> in a plot keeps the text after it",
			data: func(*testing.T) string { return `<movie><title>X</title><plot>Line one<br>Line two</plot></movie>` },
			want: want{title: "X", overview: "Line oneLine two", typ: typeMovie},
		},
		{
			name: "HTML entity",
			data: func(*testing.T) string {
				return `<movie><title>L&eacute;on</title><plot>caf&eacute; &amp; cr&egrave;me</plot></movie>`
			},
			want: want{title: "Léon", overview: "café & crème", typ: typeMovie},
		},
		{
			name: "bare ampersand",
			data: func(*testing.T) string {
				return `<movie><title>Tom & Jerry</title><plot>A & B</plot></movie>`
			},
			want: want{title: "Tom & Jerry", overview: "A & B", typ: typeMovie},
		},
		{
			name: "non-numeric year keeps the other fields",
			data: func(*testing.T) string {
				return `<movie><title>Inception</title><year>TBD</year><premiered>2010-07-16</premiered>` +
					`<actor><name>Leonardo DiCaprio</name><order>first</order></actor>` +
					`<uniqueid type="tmdb">27205</uniqueid></movie>`
			},
			want: want{title: "Inception", typ: typeMovie, year: 2010, tmdb: "27205"},
		},
		{
			name: "year with trailing text",
			data: func(*testing.T) string { return `<movie><title>X</title><year>1999 (approx)</year></movie>` },
			want: want{title: "X", typ: typeMovie, year: 1999},
		},
		{
			name:    "truncated XML is still an error",
			data:    func(*testing.T) string { return `<movie><title>Broken` },
			wantErr: true,
		},
		{
			name:    "empty file",
			data:    func(*testing.T) string { return " \n\t" },
			wantErr: true,
		},
		{
			name:    "plain text without a provider link",
			data:    func(*testing.T) string { return "not xml at all" },
			wantErr: true,
		},
		{
			name: "URL-only TMDB movie link",
			data: func(*testing.T) string { return "https://www.themoviedb.org/movie/78-blade-runner\n" },
			want: want{typ: typeMovie, tmdb: "78", linkOnly: true},
		},
		{
			name: "URL-only TMDB TV link",
			data: func(*testing.T) string { return "https://www.themoviedb.org/tv/1396-breaking-bad?language=en-US" },
			want: want{typ: typeSeries, tmdb: "1396", linkOnly: true},
		},
		{
			name: "URL-only IMDb link leaves the type open",
			data: func(*testing.T) string { return "http://www.imdb.com/title/tt0083658/" },
			want: want{imdb: "tt0083658", linkOnly: true},
		},
		{
			name: "URL-only bare IMDb id",
			data: func(*testing.T) string { return "tt0133093" },
			want: want{imdb: "tt0133093", linkOnly: true},
		},
		{
			name: "URL-only old TVDB series link",
			data: func(*testing.T) string { return "http://thetvdb.com/?tab=series&id=81189&lid=7" },
			want: want{typ: typeSeries, tvdb: "81189", linkOnly: true},
		},
		{
			name: "URL-only TVDB link with seriesid",
			data: func(*testing.T) string {
				return "https://thetvdb.com/index.php?tab=episode&seriesid=121361&seasonid=364731&id=3254641"
			},
			want: want{typ: typeSeries, tvdb: "121361", linkOnly: true},
		},
		{
			name:    "URL-only TVDB slug link carries no id",
			data:    func(*testing.T) string { return "https://thetvdb.com/series/breaking-bad" },
			wantErr: true,
		},
		{
			name: "URL-only lookalike domains are not provider links",
			data: func(*testing.T) string {
				return "https://evilthemoviedb.org/movie/603 http://xthetvdb.com/series/81189"
			},
			wantErr: true,
		},
		{
			name: "URL-only movie and TV links together keep only IMDb",
			data: func(*testing.T) string {
				return "https://www.themoviedb.org/movie/78 https://www.themoviedb.org/tv/1396 https://www.imdb.com/title/tt0083658/"
			},
			want: want{imdb: "tt0083658", linkOnly: true},
		},
		{
			name: "hybrid movie: XML then IMDb link",
			data: func(*testing.T) string {
				return "<movie><title>The Godfather</title></movie>\nhttps://www.imdb.com/title/tt0068646/\n"
			},
			want: want{title: "The Godfather", typ: typeMovie, imdb: "tt0068646"},
		},
		{
			name: "hybrid movie ignores a TMDB TV link",
			data: func(*testing.T) string {
				return "<movie><title>X</title></movie>\nhttps://www.themoviedb.org/tv/1396\n"
			},
			want: want{title: "X", typ: typeMovie},
		},
		{
			name: "hybrid uniqueid wins over the trailing link",
			data: func(*testing.T) string {
				return `<movie><title>X</title><uniqueid type="imdb">tt0000001</uniqueid></movie>` +
					"\nhttps://www.imdb.com/title/tt0000002/"
			},
			want: want{title: "X", typ: typeMovie, imdb: "tt0000001"},
		},
		{
			name: "hybrid tvshow: XML then TVDB link with an escaped ampersand",
			data: func(*testing.T) string {
				return "<tvshow><title>Show</title></tvshow>\nhttp://thetvdb.com/?tab=series&amp;id=79126"
			},
			want: want{title: "Show", typ: typeSeries, tvdb: "79126"},
		},
		{
			name: "legacy movie <id> with an IMDb id",
			data: func(*testing.T) string { return `<movie><title>X</title><id>tt0111161</id></movie>` },
			want: want{title: "X", typ: typeMovie, imdb: "tt0111161"},
		},
		{
			name: "legacy movie <id> numeric content is ignored",
			data: func(*testing.T) string { return `<movie><title>X</title><id>278</id></movie>` },
			want: want{title: "X", typ: typeMovie},
		},
		{
			name: "legacy <id> attributes",
			data: func(*testing.T) string {
				return `<movie><title>X</title><id TMDB="278" imdb="tt0111161">278</id></movie>`
			},
			want: want{title: "X", typ: typeMovie, tmdb: "278", imdb: "tt0111161"},
		},
		{
			name: "legacy tvshow numeric <id> names no provider",
			data: func(*testing.T) string { return `<tvshow><title>Show</title><id>81189</id></tvshow>` },
			want: want{title: "Show", typ: typeSeries},
		},
		{
			name: "tvshow <id> repeating the default TMDB uniqueid is not a TVDB id",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><uniqueid type="tmdb" default="true">1399</uniqueid><id>1399</id></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tmdb: "1399"},
		},
		{
			name: "id elements",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><imdb_id>tt0903747</imdb_id><tmdbid>1396</tmdbid><tvdbid>81189</tvdbid></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tmdb: "1396", imdb: "tt0903747", tvdb: "81189"},
		},
		{
			name: "imdbid element",
			data: func(*testing.T) string { return `<movie><title>X</title><imdbid>tt0133093</imdbid></movie>` },
			want: want{title: "X", typ: typeMovie, imdb: "tt0133093"},
		},
		{
			name: "uniqueid wins over id elements",
			data: func(*testing.T) string {
				return `<movie><title>X</title><tmdbid>1</tmdbid><uniqueid type="tmdb">2</uniqueid><id>tt0000003</id><uniqueid type="imdb">tt0000004</uniqueid></movie>`
			},
			want: want{title: "X", typ: typeMovie, tmdb: "2", imdb: "tt0000004"},
		},
		{
			name: "untyped uniqueid sniffs an IMDb id",
			data: func(*testing.T) string { return `<movie><title>X</title><uniqueid>tt0083658</uniqueid></movie>` },
			want: want{title: "X", typ: typeMovie, imdb: "tt0083658"},
		},
		{
			name: "unknown-type uniqueid sniffs an IMDb id; numeric stays ambiguous",
			data: func(*testing.T) string {
				return `<movie><title>X</title><uniqueid type="unknown">78</uniqueid><uniqueid type="unknown">tt0083658</uniqueid></movie>`
			},
			want: want{title: "X", typ: typeMovie, imdb: "tt0083658"},
		},
		{
			name: "uniqueid type aliases",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><uniqueid type="TheMovieDB">1396</uniqueid>` +
					`<uniqueid type="TheTVDB">81189</uniqueid><uniqueid type="IMDB">tt0903747</uniqueid></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tmdb: "1396", imdb: "tt0903747", tvdb: "81189"},
		},
		{
			name: "uniqueid type tmdb_id",
			data: func(*testing.T) string {
				return `<movie><title>X</title><uniqueid type="tmdb_id">603</uniqueid></movie>`
			},
			want: want{title: "X", typ: typeMovie, tmdb: "603"},
		},
		{
			name: "a malformed stronger id does not block a valid weaker form",
			data: func(*testing.T) string {
				return `<movie><title>X</title><uniqueid type="tmdb" default="true">N/A</uniqueid>` +
					`<uniqueid type="imdb">none</uniqueid><tmdbid>603</tmdbid><id>tt0133093</id></movie>`
			},
			want: want{title: "X", typ: typeMovie, tmdb: "603", imdb: "tt0133093"},
		},
		{
			name: "TVDB link whose seriesid is a placeholder",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><episodeguide><url>https://thetvdb.com/?seriesid=0&amp;tab=series&amp;id=81189</url></episodeguide></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tvdb: "81189"},
		},
		{
			name: "URL-only NFO with a placeholder link before a valid one",
			data: func(*testing.T) string {
				return "https://www.themoviedb.org/movie/0\nhttps://www.themoviedb.org/movie/603\ntt0000000 tt0133093"
			},
			want: want{typ: typeMovie, tmdb: "603", imdb: "tt0133093", linkOnly: true},
		},
		{
			name: "an upper-case IMDb id is stored in lower case",
			data: func(*testing.T) string {
				return `<movie><title>X</title><uniqueid type="imdb">TT0133093</uniqueid></movie>`
			},
			want: want{title: "X", typ: typeMovie, imdb: "tt0133093"},
		},
		{
			name: "a placeholder IMDb id does not block a valid weaker form",
			data: func(*testing.T) string {
				return `<movie><title>X</title><uniqueid type="imdb" default="true">tt0000000</uniqueid>` +
					`<imdbid>tt0133093</imdbid></movie>`
			},
			want: want{title: "X", typ: typeMovie, imdb: "tt0133093"},
		},
		{
			name: "a zero placeholder id does not block a valid weaker form",
			data: func(*testing.T) string {
				return `<tvshow><title>X</title><uniqueid type="tmdb" default="true">0</uniqueid>` +
					`<uniqueid type="tvdb">000</uniqueid><tmdbid>1396</tmdbid><tvdbid>81189</tvdbid></tvshow>`
			},
			want: want{title: "X", typ: typeSeries, tmdb: "1396", tvdb: "81189"},
		},
		{
			name: "default uniqueid wins over an earlier one of the same type",
			data: func(*testing.T) string {
				return `<movie><title>X</title><uniqueid type="tmdb">1</uniqueid><uniqueid type="tmdb" default="true">2</uniqueid></movie>`
			},
			want: want{title: "X", typ: typeMovie, tmdb: "2"},
		},
		{
			name: "episodeguide JSON",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><episodeguide>{"tvdb": "81189", "tmdb": 1396, "imdb": "tt0903747"}</episodeguide></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tmdb: "1396", imdb: "tt0903747", tvdb: "81189"},
		},
		{
			name: "episodeguide JSON with a fractional id leaves the slot to a valid form",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><episodeguide>{"tmdb": 1399.5, "tvdb": 81189}</episodeguide></tvshow>` +
					"\nhttps://www.themoviedb.org/tv/1396"
			},
			want: want{title: "Show", typ: typeSeries, tmdb: "1396", tvdb: "81189"},
		},
		{
			name: "episodeguide JSON written with entities",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><episodeguide>{&quot;tvdb&quot;:&quot;121361&quot;}</episodeguide></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tvdb: "121361"},
		},
		{
			name: "episodeguide TVDB API URL",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><episodeguide><url cache="76156.xml">http://thetvdb.com/api/1D62F2F90030C444/series/76156/all/en.zip</url></episodeguide></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tvdb: "76156"},
		},
		{
			name: "episodeguide TMDB API URL",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><episodeguide><url cache="tmdb-1399-en.json">http://api.tmdb.org/3/tv/1399?api_key=k&amp;language=en</url></episodeguide></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tmdb: "1399"},
		},
		{
			name: "episodeguide TVDB v3 login URL",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><episodeguide><url post="yes" cache="auth.json">https://api.thetvdb.com/login?{&quot;apikey&quot;:&quot;k&quot;,&quot;id&quot;:121361}|Content-Type=application/json</url></episodeguide></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tvdb: "121361"},
		},
		{
			name: "episodeguide keys that spell one provider two ways resolve the same every time",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><episodeguide>{"tmdb": "1", "themoviedb": "2"}</episodeguide></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tmdb: "2"},
		},
		{
			name: "uniqueid wins over episodeguide",
			data: func(*testing.T) string {
				return `<tvshow><title>Show</title><uniqueid type="tvdb">1</uniqueid><episodeguide>{"tvdb": "2"}</episodeguide></tvshow>`
			},
			want: want{title: "Show", typ: typeSeries, tvdb: "1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := parseNFOData([]byte(tc.data(t)))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseNFOData() = %#v, want error", p)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNFOData() error = %v", err)
			}
			got := want{
				title: p.Title, overview: p.Overview, typ: p.Type, year: p.Year,
				tmdb: p.TmdbID, imdb: p.ImdbID, tvdb: p.TvdbID, linkOnly: p.LinkOnly,
			}
			if got != tc.want {
				t.Errorf("parsed = %+v\nwant     %+v", got, tc.want)
			}
		})
	}
}

func TestParseNFOData_NumericElementsAreLenient(t *testing.T) {
	t.Parallel()

	movie, err := parseNFOData([]byte(`<movie><title>X</title>` +
		`<actor><name>A</name><order>first</order></actor>` +
		`<actor><name>B</name><order> 7 </order></actor></movie>`))
	if err != nil {
		t.Fatalf("parseNFOData(movie) error = %v", err)
	}
	if len(movie.People) != 2 || movie.People[0].SortOrder != 0 || movie.People[1].SortOrder != 7 {
		t.Errorf("People = %#v, want file order 0 for the bad <order> and 7 for the padded one", movie.People)
	}

	episode, err := parseNFOData([]byte(`<episodedetails><title>Pilot</title><season>1.0</season><episode>one</episode></episodedetails>`))
	if err != nil {
		t.Fatalf("parseNFOData(episode) error = %v", err)
	}
	if !episode.SeasonSet || episode.Season != 1 || episode.EpisodeSet || episode.Title != "Pilot" {
		t.Errorf("episode = %+v, want season 1 set, episode unset, title kept", episode)
	}

	season, err := parseNFOData([]byte(`<season><title>S</title><seasonnumber>n/a</seasonnumber></season>`))
	if err != nil {
		t.Fatalf("parseNFOData(season) error = %v", err)
	}
	if season.SeasonSet || season.Title != "S" {
		t.Errorf("season = %+v, want no season number and the title kept", season)
	}
}

func TestParseNFOInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"2019", 2019, true},
		{" 3 ", 3, true},
		{"2.0", 2, true},
		{"-1", -1, true},
		{"2019-05-01", 2019, true},
		{"3 of 10", 3, true},
		{"", 0, false},
		{"TBD", 0, false},
		{"S01", 0, false},
		{"2.5", 0, false},
		{"2.50", 0, false},
		{"2019.0", 2019, true},
		{"2e3", 0, false},
		{"1e10", 0, false},
		{"0x10", 0, false},
	}
	for _, tc := range tests {
		got, ok := parseNFOInt(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("parseNFOInt(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestParseNFOData_MultiEpisodeStillDetectedWithLenientDecoder(t *testing.T) {
	t.Parallel()

	doc := strings.Join([]string{
		`<episodedetails><title>One</title><episode>1</episode></episodedetails>`,
		`<episodedetails><title>Two</title><episode>2</episode></episodedetails>`,
	}, "\n")
	p, err := parseNFOData([]byte(doc))
	if err != nil {
		t.Fatalf("parseNFOData() error = %v", err)
	}
	if !p.MultiEpisode || p.Title != "One" {
		t.Errorf("parsed = %+v, want the first block with MultiEpisode set", p)
	}
}
