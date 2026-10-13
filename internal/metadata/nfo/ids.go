package nfo

import (
	"encoding/json"
	"encoding/xml"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/providerid"
)

// nfoLegacyID is the pre-<uniqueid> <id> element. Kodi writes the default
// <uniqueid> value there, whatever its type; Jellyfin also writes
// TMDB/TVDB/IMDB attributes on it.
type nfoLegacyID struct {
	Value string     `xml:",chardata"`
	Attrs []xml.Attr `xml:",any,attr"`
}

// nfoEpisodeGuide is a tvshow <episodeguide>: Kodi 20+ writes a JSON object
// of ids as its text, older scrapers a <url> to the TVDB or TMDB API.
type nfoEpisodeGuide struct {
	Text string   `xml:",chardata"`
	URLs []string `xml:"url"`
}

// nfoIDElements holds the alternate id forms shared by <movie> and <tvshow>.
type nfoIDElements struct {
	LegacyIDs []nfoLegacyID `xml:"id"`
	IMDBID    []string      `xml:"imdbid"`
	IMDBIDAlt []string      `xml:"imdb_id"`
	TMDBID    []string      `xml:"tmdbid"`
	TVDBID    []string      `xml:"tvdbid"`
}

const (
	idKeyIMDB = "imdb"
	idKeyTMDB = "tmdb"
	idKeyTVDB = "tvdb"
)

var (
	imdbIDRe       = regexp.MustCompile(`(?i)\btt\d{7,}\b`)
	providerLinkRe = regexp.MustCompile(`(?i)\b(?:https?://)?(?:[a-z0-9-]+\.)*(?:imdb\.com|themoviedb\.org|tmdb\.org|thetvdb\.com)/[^\s"'<>]*`)
	// Kodi's TVDB v3 scraper wrote its episodeguide as the API login URL with
	// the series id in a JSON query: api.thetvdb.com/login?{"apikey":"…","id":N}.
	tvdbLoginSeriesRe = regexp.MustCompile(`(?i)thetvdb\.com/login\?\S*?"id"\s*:\s*"?(\d+)`)
)

// uniqueIDKey maps a <uniqueid type> (or episodeguide JSON key) to the id key
// it names. Writers disagree on spelling (TMDB, themoviedb, tmdb_id, TheTVDB),
// so the type is compared with case and punctuation removed. An absent or
// "unknown" type returns "" so the caller can sniff the value instead; any
// other type returns "other", which names no slot.
func uniqueIDKey(rawType string) string {
	normalized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return -1
	}, rawType)
	switch normalized {
	case "imdb", "imdbid":
		return idKeyIMDB
	case "tmdb", "tmdbid", "themoviedb", "themoviedbid", "moviedb":
		return idKeyTMDB
	case "tvdb", "tvdbid", "thetvdb", "thetvdbid":
		return idKeyTVDB
	case "", "unknown":
		return ""
	}
	return "other"
}

// idSlots fills each provider id once; the first form to supply a valid
// value for a key wins, so callers offer forms strongest first. A malformed
// value (N/A, a TMDB id with letters, a 0 placeholder) claims nothing, so a
// weaker valid form still fills the slot.
type idSlots struct{ p *parsedNFO }

func (s idSlots) set(key, value string) {
	value = strings.TrimSpace(value)
	switch {
	case key == idKeyIMDB && !providerid.IsIMDbID(value):
		return
	case (key == idKeyTMDB || key == idKeyTVDB) && !providerid.IsPositiveDecimal(value):
		return
	}
	var slot *string
	switch key {
	case idKeyIMDB:
		slot = &s.p.ImdbID
	case idKeyTMDB:
		slot = &s.p.TmdbID
	case idKeyTVDB:
		slot = &s.p.TvdbID
	default:
		return
	}
	if key == idKeyIMDB {
		value = strings.ToLower(value)
	}
	if *slot == "" {
		*slot = value
	}
}

// applyUniqueIDs reads typed <uniqueid> entries, the entry marked
// default="true" first for each type, then sniffs an IMDb id out of untyped or
// "unknown" ones.
func applyUniqueIDs(p *parsedNFO, ids []nfoUniqueID) {
	slots := idSlots{p}
	for _, wantDefault := range []bool{true, false} {
		for _, uid := range ids {
			if isTrueAttr(uid.Default) != wantDefault {
				continue
			}
			slots.set(uniqueIDKey(uid.Type), uid.Value)
		}
	}
	for _, uid := range ids {
		if uniqueIDKey(uid.Type) == "" && providerid.IsIMDbID(uid.Value) {
			slots.set(idKeyIMDB, uid.Value)
		}
	}
}

func isTrueAttr(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

// applyIDElements reads the alternate id forms after <uniqueid>, which wins
// any key they share: <imdbid>/<imdb_id>/<tmdbid>/<tvdbid>, then the legacy
// <id> element the way Jellyfin reads it: its TMDB/TVDB/IMDB attributes, and
// its text only as an IMDb id. A bare number in <id> names no provider (Kodi
// writes the default uniqueid there, which may be a TMDB or a TVDB id).
func applyIDElements(p *parsedNFO, e *nfoIDElements) {
	slots := idSlots{p}
	for _, values := range []struct {
		key    string
		values []string
	}{
		{idKeyIMDB, e.IMDBID},
		{idKeyIMDB, e.IMDBIDAlt},
		{idKeyTMDB, e.TMDBID},
		{idKeyTVDB, e.TVDBID},
	} {
		for _, value := range values.values {
			slots.set(values.key, value)
		}
	}
	for _, id := range e.LegacyIDs {
		for _, attr := range id.Attrs {
			slots.set(uniqueIDKey(attr.Name.Local), attr.Value)
		}
		if providerid.IsIMDbID(id.Value) {
			slots.set(idKeyIMDB, id.Value)
		}
	}
}

// applyEpisodeGuide reads the ids a tvshow <episodeguide> carries.
func applyEpisodeGuide(p *parsedNFO, guide nfoEpisodeGuide) {
	slots := idSlots{p}
	text := strings.TrimSpace(guide.Text)
	// Numbers keep their literal text, so a fractional or exponent value fails
	// the same id check as any other malformed form.
	var ids map[string]any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if strings.HasPrefix(text, "{") && decoder.Decode(&ids) == nil {
		// Sorted keys keep the result stable when two spellings name one slot.
		for _, rawKey := range slices.Sorted(maps.Keys(ids)) {
			switch v := ids[rawKey].(type) {
			case string:
				slots.set(uniqueIDKey(rawKey), v)
			case json.Number:
				slots.set(uniqueIDKey(rawKey), v.String())
			}
		}
	}
	for _, link := range append([]string{text}, guide.URLs...) {
		applyProviderLinks(p, scanProviderLinks(link), typeSeries)
	}
}

// providerLinks holds the ids found in provider URLs. TMDB ids are kept per
// kind because a movie id and a TV id with the same number are different
// titles.
type providerLinks struct {
	IMDB      string
	TMDBMovie string
	TMDBTV    string
	TVDB      string
}

func (l providerLinks) empty() bool {
	return l == providerLinks{}
}

// scanProviderLinks finds IMDb, TMDB, and TVDB ids in free text: a URL-only
// NFO, the text after a hybrid NFO's XML, or an episodeguide URL. A bare IMDb
// id (tt1234567) counts too.
func scanProviderLinks(text string) providerLinks {
	var links providerLinks
	// A placeholder link (movie/0, tt0000000) claims nothing, so a later
	// valid link still supplies the id.
	setID := func(slot *string, value string) {
		if *slot == "" && providerid.IsPositiveDecimal(value) {
			*slot = value
		}
	}
	setIMDb := func(value string) {
		if links.IMDB == "" && providerid.IsIMDbID(value) {
			links.IMDB = value
		}
	}
	for _, raw := range providerLinkRe.FindAllString(text, -1) {
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		segments := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
		switch {
		case hostIs(host, "imdb.com"):
			for _, segment := range segments {
				if providerid.IsIMDbID(segment) {
					setIMDb(segment)
					break
				}
			}
		case hostIs(host, "themoviedb.org"), hostIs(host, "tmdb.org"):
			// www.themoviedb.org/movie/78-blade-runner, api.tmdb.org/3/tv/1399
			for i := 0; i+1 < len(segments); i++ {
				kind, id := strings.ToLower(segments[i]), leadingDigits(segments[i+1])
				if !providerid.IsPositiveDecimal(id) {
					continue
				}
				if kind == typeMovie {
					setID(&links.TMDBMovie, id)
					break
				}
				if kind == "tv" {
					setID(&links.TMDBTV, id)
					break
				}
			}
		case hostIs(host, "thetvdb.com"):
			setID(&links.TVDB, tvdbSeriesIDFromURL(u, segments))
		}
	}
	if match := tvdbLoginSeriesRe.FindStringSubmatch(text); match != nil {
		setID(&links.TVDB, match[1])
	}
	for _, id := range imdbIDRe.FindAllString(text, -1) {
		setIMDb(id)
	}
	return links
}

// tvdbSeriesIDFromURL reads a series id from the TVDB URL shapes NFO files
// carry: ?tab=series&id=N, any URL with seriesid=N (old episode links), and
// /series/N paths (including the v1 API's /api/<key>/series/N/all/en.zip).
// Slug-only URLs such as /series/breaking-bad carry no id.
func tvdbSeriesIDFromURL(u *url.URL, segments []string) string {
	query := u.Query()
	if id := query.Get("seriesid"); providerid.IsPositiveDecimal(id) {
		return id
	}
	if strings.EqualFold(query.Get("tab"), "series") && providerid.IsPositiveDecimal(query.Get("id")) {
		return query.Get("id")
	}
	for i := 0; i+1 < len(segments); i++ {
		if strings.EqualFold(segments[i], "series") && providerid.IsPositiveDecimal(segments[i+1]) {
			return segments[i+1]
		}
	}
	return ""
}

// applyProviderLinks fills ids from links that fit the document's type: TMDB
// movie links for movies, TMDB TV and TVDB series links for shows.
func applyProviderLinks(p *parsedNFO, links providerLinks, contentType string) {
	slots := idSlots{p}
	slots.set(idKeyIMDB, links.IMDB)
	switch contentType {
	case typeMovie:
		slots.set(idKeyTMDB, links.TMDBMovie)
	case typeSeries:
		slots.set(idKeyTMDB, links.TMDBTV)
		slots.set(idKeyTVDB, links.TVDB)
	}
}

// linkOnlyNFOType infers what a URL-only NFO describes from its links, so the
// provider's root-type guard still applies: a TMDB movie link means a movie,
// a TMDB TV or TVDB link a show. An IMDb id alone, or links of both kinds,
// leave the type open and only the IMDb id is kept.
func linkOnlyNFOType(links providerLinks) string {
	movie := links.TMDBMovie != ""
	series := links.TMDBTV != "" || links.TVDB != ""
	switch {
	case movie && !series:
		return typeMovie
	case series && !movie:
		return typeSeries
	}
	return ""
}

func hostIs(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func leadingDigits(value string) string {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	return value[:end]
}
