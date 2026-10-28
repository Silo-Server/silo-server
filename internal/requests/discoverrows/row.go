// Package discoverrows defines the rows of the Requests Discover page: the
// row types, how a row is checked and stored, and where each row's titles
// come from. The request service (internal/requests) stores the layout and
// lays each viewer's availability and request state over the titles; this
// package never imports it. See docs/architecture/request-discover-rows.md.
package discoverrows

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Type is what a row shows.
type Type string

const (
	// TypeTMDBChart is one of TMDB's charts (trending, popular, upcoming...).
	TypeTMDBChart Type = "tmdb_chart"
	// TypeTMDBDiscover is a TMDB /discover query built from filters.
	TypeTMDBDiscover Type = "tmdb_discover"
	// TypeTMDBList is a public TMDB list, read from a stored copy.
	TypeTMDBList Type = "tmdb_list"
	// TypeMDBList is a public MDBList list, read from a stored copy.
	TypeMDBList Type = "mdblist"
	// TypeStudios, TypeNetworks and TypeGenres are the fixed brand
	// carousels; their cards open a browse page instead of a title.
	TypeStudios  Type = "studios"
	TypeNetworks Type = "networks"
	TypeGenres   Type = "genres"
)

// IsBrand reports whether the row shows brand cards rather than titles.
func (t Type) IsBrand() bool {
	return t == TypeStudios || t == TypeNetworks || t == TypeGenres
}

// Row is one row of the Discover layout, as stored and as an admin edits it.
// Exactly the spec member that matches Type is set: Chart for tmdb_chart,
// Discover for tmdb_discover, List for tmdb_list and mdblist, none for a
// brand row.
type Row struct {
	ID      string `json:"id"`
	Type    Type   `json:"type"`
	Title   string `json:"title"`
	Enabled bool   `json:"enabled"`
	// Limit is how many titles the row shows on the Discover page; its see
	// all page keeps paging. Zero on a brand row.
	Limit int `json:"limit,omitempty"`
	// HideAvailable leaves out titles the library already has.
	HideAvailable bool          `json:"hide_available,omitempty"`
	Chart         *ChartSpec    `json:"chart,omitempty"`
	Discover      *DiscoverSpec `json:"discover,omitempty"`
	List          *ListSpec     `json:"list,omitempty"`
}

// ChartSpec names a TMDB chart. MediaType is movie, series, or all (trending
// only); TimeWindow (day or week) applies to trending only.
type ChartSpec struct {
	Preset     string `json:"preset"`
	MediaType  string `json:"media_type"`
	TimeWindow string `json:"time_window,omitempty"`
}

// DiscoverSpec is a TMDB /discover query. Genres must all match; the other
// lists match any of their values. Years bound the release (or first-air)
// year, inclusive.
type DiscoverSpec struct {
	MediaType        string   `json:"media_type"`
	SortBy           string   `json:"sort_by"`
	GenreIDs         []int    `json:"genre_ids,omitempty"`
	ExcludeGenreIDs  []int    `json:"exclude_genre_ids,omitempty"`
	KeywordIDs       []int    `json:"keyword_ids,omitempty"`
	CompanyIDs       []int    `json:"company_ids,omitempty"`
	NetworkIDs       []int    `json:"network_ids,omitempty"`
	OriginalLanguage string   `json:"original_language,omitempty"`
	OriginCountries  []string `json:"origin_countries,omitempty"`
	YearFrom         int      `json:"year_from,omitempty"`
	YearTo           int      `json:"year_to,omitempty"`
	VoteAverageGte   float64  `json:"vote_average_gte,omitempty"`
	VoteCountGte     int      `json:"vote_count_gte,omitempty"`
	RuntimeGte       int      `json:"runtime_gte,omitempty"`
	RuntimeLte       int      `json:"runtime_lte,omitempty"`
	CertificationLte string   `json:"certification_lte,omitempty"`
	WatchProviderIDs []int    `json:"watch_provider_ids,omitempty"`
	WatchRegion      string   `json:"watch_region,omitempty"`
}

// ListSpec is a public list by URL: a themoviedb.org list page for tmdb_list,
// an mdblist.com list page for mdblist. Normalize stores it canonical.
type ListSpec struct {
	URL string `json:"url"`
	// Order is how an mdblist row orders the list: "" (or ListOrderList) for
	// the list's own rank order, or one of the newest-first orders. A
	// tmdb_list row takes no order. Normalize stores list order as "".
	Order string `json:"order,omitempty"`
}

// The orders an mdblist row may take. MDBList's public feed returns a list in
// rank order unless asked for another, whatever sort its owner saved for the
// list page, and the feed does not report that saved sort.
const (
	ListOrderList           = "list"
	ListOrderNewestAdded    = "newest_added"
	ListOrderNewestReleased = "newest_released"
)

// FieldErrors maps a field path such as "rows[2].discover.sort_by" to what
// is wrong with it.
type FieldErrors map[string]string

func (f FieldErrors) add(prefix string, other FieldErrors) {
	for field, detail := range other {
		f[prefix+field] = detail
	}
}

const (
	// MaxRows bounds a layout. Every title row costs TMDB and database work
	// on each Discover load, so the page stays a page.
	MaxRows = 30
	// MaxTitleLength bounds a row title, in characters.
	MaxTitleLength = 80
	// DefaultLimit and MaxLimit bound how many titles a row shows.
	DefaultLimit = 20
	MaxLimit     = 100
	// PageSize is how many titles one page of a row holds: TMDB's own page
	// size, which list rows follow too.
	PageSize = 20
)

var rowIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// reservedIDs may not name a custom row: the built-in rows' IDs, which shipped
// clients merge on, and words the API's paths use.
var reservedIDs = map[string]bool{"rows": true, "browse": true}

// NormalizeLayout checks a whole layout and returns it in canonical form:
// specs canonical, limits defaulted, and an ID on every new row. A built-in
// row (one of the six original charts) keeps its ID and chart; its title,
// limit, switch and hide_available can change. A brand row's ID is its type,
// so each appears at most once.
func NormalizeLayout(rows []Row) ([]Row, FieldErrors) {
	errs := FieldErrors{}
	if len(rows) > MaxRows {
		errs["rows"] = fmt.Sprintf("a layout has at most %d rows", MaxRows)
		return nil, errs
	}
	out := make([]Row, 0, len(rows))
	seen := map[string]int{}
	for i, row := range rows {
		prefix := fmt.Sprintf("rows[%d].", i)
		normalized, rowErrs := NormalizeRow(row)
		errs.add(prefix, rowErrs)
		out = append(out, normalized)
		if _, unknown := rowErrs["type"]; unknown {
			continue
		}
		if detail := checkRowID(&out[i]); detail != "" {
			errs[prefix+"id"] = detail
		} else if first, dup := seen[out[i].ID]; dup {
			errs[prefix+"id"] = fmt.Sprintf("rows[%d] already uses this id", first)
		} else {
			seen[out[i].ID] = i
		}
		if legacy, ok := LegacyRow(out[i].ID); ok && rowErrs["chart"] == "" && !sameChart(out[i].Chart, legacy.Chart) {
			errs[prefix+"chart"] = "a built-in row keeps its chart"
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

// NormalizeRow checks one row on its own, without the layout's ID rules, and
// returns it in canonical form. Field paths are relative to the row.
func NormalizeRow(row Row) (Row, FieldErrors) {
	errs := FieldErrors{}
	row.ID = strings.TrimSpace(row.ID)
	row.Title = strings.TrimSpace(row.Title)
	switch n := utf8.RuneCountInString(row.Title); {
	case n == 0:
		errs["title"] = "a row needs a title"
	case n > MaxTitleLength:
		errs["title"] = fmt.Sprintf("at most %d characters", MaxTitleLength)
	}
	kind, ok := KindOf(row.Type)
	if !ok {
		errs["type"] = fmt.Sprintf("unknown row type %q", row.Type)
		return row, errs
	}
	if row.Type.IsBrand() {
		row.Limit, row.HideAvailable = 0, false
	} else if row.Limit == 0 {
		row.Limit = DefaultLimit
	} else if row.Limit < 1 || row.Limit > MaxLimit {
		errs["limit"] = fmt.Sprintf("between 1 and %d titles", MaxLimit)
	}
	errs.add("", kind.Normalize(&row))
	return row, errs
}

// checkRowID assigns a new row its ID and checks a given one. It returns
// what is wrong, or "".
func checkRowID(row *Row) string {
	if row.Type.IsBrand() {
		if row.ID == "" {
			row.ID = string(row.Type)
		}
		if row.ID != string(row.Type) {
			return fmt.Sprintf("a %s row's id is %q", row.Type, row.Type)
		}
		return ""
	}
	if row.ID == "" {
		row.ID = newRowID()
		return ""
	}
	if !rowIDPattern.MatchString(row.ID) {
		return "lowercase letters, digits and underscores, at most 40"
	}
	if reservedIDs[row.ID] || Type(row.ID).IsBrand() {
		return fmt.Sprintf("%q is reserved", row.ID)
	}
	if IsLegacyID(row.ID) && row.Type != TypeTMDBChart {
		return "a built-in row stays a chart row"
	}
	return ""
}

// newRowID returns an opaque ID for a custom row. The prefix keeps it clear
// of the built-in IDs that shipped clients merge on.
func newRowID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	buf := make([]byte, 10)
	_, _ = rand.Read(buf) // crypto/rand.Read never fails
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return "r_" + string(buf)
}

func sameChart(a, b *ChartSpec) bool {
	return a != nil && b != nil && *a == *b
}

// Media types a row names, as the request API does.
const (
	MediaMovie  = "movie"
	MediaSeries = "series"
	// mediaAll mixes movies and series on a trending chart.
	mediaAll = "all"
)

// tmdbMediaType maps a row's media type onto TMDB's: series is tv.
func tmdbMediaType(mediaType string) string {
	if mediaType == MediaSeries {
		return "tv"
	}
	return mediaType
}

// usCertifications are the US ratings TMDB's certification.lte takes, lowest
// first, per media type.
var usCertifications = map[string][]string{
	MediaMovie:  {"G", "PG", "PG-13", "R", "NC-17"},
	MediaSeries: {"TV-Y", "TV-Y7", "TV-G", "TV-PG", "TV-14", "TV-MA"},
}

// CertificationLadder returns the US ratings TMDB's certification.lte takes
// for a media type (movie, series, or TMDB's tv), lowest first.
func CertificationLadder(mediaType string) []string {
	if mediaType == "tv" {
		mediaType = MediaSeries
	}
	return usCertifications[mediaType]
}
