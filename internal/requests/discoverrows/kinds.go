package discoverrows

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
)

// Kind is the behavior of one row type. Adding a row type means adding a
// Kind to kinds below (and, for a list type, its fetch to Refresher);
// nothing outside this package switches on Type except for brand rows, which
// the clients draw themselves.
type Kind interface {
	// Normalize checks the row's spec, refuses a spec member of another
	// type, and rewrites the spec to canonical form. Field paths are
	// relative to the row.
	Normalize(row *Row) FieldErrors
	// Source returns where the row's titles come from; nil for a brand row.
	Source(env Env, row Row) Source
	// SourceKey names the stored list the row reads; "" for a live source.
	SourceKey(row Row) string
}

var kinds = map[Type]Kind{
	TypeTMDBChart:    chartKind{},
	TypeTMDBDiscover: discoverKind{},
	TypeTMDBList:     tmdbListKind{},
	TypeMDBList:      mdbListKind{},
	TypeStudios:      brandKind{},
	TypeNetworks:     brandKind{},
	TypeGenres:       brandKind{},
}

// KindOf returns the behavior of a row type. A stored row whose type this
// server does not know (written by a newer one) has none and is skipped.
func KindOf(t Type) (Kind, bool) {
	k, ok := kinds[t]
	return k, ok
}

// SourceKey returns the stored list a row reads, or "" for a row that reads
// a live source or has an unknown type.
func SourceKey(row Row) string {
	if k, ok := KindOf(row.Type); ok {
		return k.SourceKey(row)
	}
	return ""
}

// onlySpec refuses the spec members a row type does not use.
func onlySpec(row *Row, keep string) FieldErrors {
	errs := FieldErrors{}
	set := map[string]bool{"chart": row.Chart != nil, "discover": row.Discover != nil, "list": row.List != nil}
	for name, isSet := range set {
		if isSet && name != keep {
			errs[name] = fmt.Sprintf("not used by a %s row", row.Type)
		}
	}
	if keep != "" && !set[keep] {
		errs[keep] = fmt.Sprintf("a %s row needs one", row.Type)
	}
	return errs
}

type chartKind struct{}

func (chartKind) Normalize(row *Row) FieldErrors {
	if errs := onlySpec(row, "chart"); len(errs) > 0 {
		return errs
	}
	c := new(*row.Chart)
	row.Chart = c
	c.Preset, c.MediaType, c.TimeWindow = strings.TrimSpace(c.Preset), strings.TrimSpace(c.MediaType), strings.TrimSpace(c.TimeWindow)
	if c.Preset != presetTrending {
		c.TimeWindow = ""
	}
	if c.MediaType != MediaMovie && c.MediaType != MediaSeries && c.MediaType != mediaAll {
		return FieldErrors{"chart.media_type": "movie, series or all"}
	}
	err := templates.ValidateTMDBSpec(templates.TMDBSpec{Preset: c.Preset, MediaType: tmdbMediaType(c.MediaType), TimeWindow: c.TimeWindow})
	if err != nil {
		return FieldErrors{"chart": err.Error()}
	}
	return nil
}

func (chartKind) Source(env Env, row Row) Source { return chartSource{env: env, spec: *row.Chart} }

func (chartKind) SourceKey(Row) string { return "" }

type discoverKind struct{}

var (
	countryPattern  = regexp.MustCompile(`^[A-Z]{2}$`)
	languagePattern = regexp.MustCompile(`^[a-z]{2}$`)
)

// maxFilterIDs bounds each ID list of a discover row.
const maxFilterIDs = 20

func (discoverKind) Normalize(row *Row) FieldErrors {
	if errs := onlySpec(row, "discover"); len(errs) > 0 {
		return errs
	}
	d := new(*row.Discover)
	row.Discover = d
	d.MediaType, d.SortBy = strings.TrimSpace(d.MediaType), strings.TrimSpace(d.SortBy)
	if d.MediaType != MediaMovie && d.MediaType != MediaSeries {
		return FieldErrors{"discover.media_type": "movie or series"}
	}
	errs := FieldErrors{}
	checkDiscoverLists(d, errs)
	checkDiscoverCodes(d, errs)
	checkDiscoverRanges(d, errs)
	if err := templates.ValidateTMDBDiscoverSpec(templateDiscoverSpec(*d)); err != nil && len(errs) == 0 {
		errs["discover"] = err.Error()
	}
	return errs
}

// checkDiscoverLists dedupes and bounds the ID and country lists.
func checkDiscoverLists(d *DiscoverSpec, errs FieldErrors) {
	for field, ids := range map[string]*[]int{
		"genre_ids": &d.GenreIDs, "exclude_genre_ids": &d.ExcludeGenreIDs, "keyword_ids": &d.KeywordIDs,
		"company_ids": &d.CompanyIDs, "network_ids": &d.NetworkIDs, "watch_provider_ids": &d.WatchProviderIDs,
	} {
		*ids = dedupe(*ids)
		if len(*ids) > maxFilterIDs {
			errs["discover."+field] = fmt.Sprintf("at most %d", maxFilterIDs)
		} else if slices.ContainsFunc(*ids, func(id int) bool { return id <= 0 }) {
			errs["discover."+field] = "TMDB ids are positive"
		}
	}
	if len(d.NetworkIDs) > 0 && d.MediaType != MediaSeries {
		errs["discover.network_ids"] = "networks filter series only"
	}
	countries := make([]string, 0, len(d.OriginCountries))
	for _, c := range d.OriginCountries {
		if c = strings.ToUpper(strings.TrimSpace(c)); c != "" {
			countries = append(countries, c)
		}
	}
	d.OriginCountries = dedupe(countries)
	if len(d.OriginCountries) > maxFilterIDs {
		errs["discover.origin_countries"] = fmt.Sprintf("at most %d", maxFilterIDs)
	} else if slices.ContainsFunc(d.OriginCountries, func(c string) bool { return !countryPattern.MatchString(c) }) {
		errs["discover.origin_countries"] = "two-letter ISO 3166-1 country codes"
	}
}

// checkDiscoverCodes normalizes and checks the language, region, rating and
// sort.
func checkDiscoverCodes(d *DiscoverSpec, errs FieldErrors) {
	d.OriginalLanguage = strings.ToLower(strings.TrimSpace(d.OriginalLanguage))
	d.WatchRegion = strings.ToUpper(strings.TrimSpace(d.WatchRegion))
	d.CertificationLte = strings.ToUpper(strings.TrimSpace(d.CertificationLte))
	if d.OriginalLanguage != "" && !languagePattern.MatchString(d.OriginalLanguage) {
		errs["discover.original_language"] = "a two-letter ISO 639-1 language code"
	}
	switch {
	case len(d.WatchProviderIDs) > 0 && d.WatchRegion == "":
		errs["discover.watch_region"] = "the region the watch providers stream in"
	case d.WatchRegion != "" && !countryPattern.MatchString(d.WatchRegion):
		errs["discover.watch_region"] = "a two-letter ISO 3166-1 country code"
	case len(d.WatchProviderIDs) == 0:
		d.WatchRegion = ""
	}
	if ladder := CertificationLadder(d.MediaType); d.CertificationLte != "" && !slices.Contains(ladder, d.CertificationLte) {
		errs["discover.certification_lte"] = "one of " + strings.Join(ladder, ", ")
	}
	sortField, _, _ := strings.Cut(d.SortBy, ".")
	switch {
	case d.MediaType == MediaSeries && (sortField == "primary_release_date" || sortField == "revenue"):
		errs["discover.sort_by"] = "series sort by first_air_date, not " + sortField
	case d.MediaType == MediaMovie && sortField == "first_air_date":
		errs["discover.sort_by"] = "movies sort by primary_release_date, not first_air_date"
	}
}

// checkDiscoverRanges checks the year and score bounds.
func checkDiscoverRanges(d *DiscoverSpec, errs FieldErrors) {
	if d.YearFrom != 0 && (d.YearFrom < 1870 || d.YearFrom > 2100) {
		errs["discover.year_from"] = "a year between 1870 and 2100"
	}
	if d.YearTo != 0 && (d.YearTo < 1870 || d.YearTo > 2100) {
		errs["discover.year_to"] = "a year between 1870 and 2100"
	}
	if d.YearFrom != 0 && d.YearTo != 0 && d.YearFrom > d.YearTo {
		errs["discover.year_to"] = "not before year_from"
	}
	if d.VoteAverageGte > 10 {
		errs["discover.vote_average_gte"] = "TMDB scores run from 0 to 10"
	}
}

// templateDiscoverSpec is the part of a discover row the collection templates
// already check.
func templateDiscoverSpec(d DiscoverSpec) templates.TMDBDiscoverSpec {
	spec := templates.TMDBDiscoverSpec{
		MediaType: tmdbMediaType(d.MediaType), WithGenres: d.GenreIDs, WithoutGenres: d.ExcludeGenreIDs, SortBy: d.SortBy,
		VoteCountGte: d.VoteCountGte, VoteAverageGte: d.VoteAverageGte, CertificationLte: d.CertificationLte,
		WithRuntimeGte: d.RuntimeGte, WithRuntimeLte: d.RuntimeLte, OriginalLanguage: d.OriginalLanguage,
	}
	spec.ReleaseDateGte, spec.ReleaseDateLte = releaseDateRange(d)
	return spec
}

// releaseDateRange turns the row's year bounds into TMDB's inclusive release
// (or first-air) date bounds; "" leaves a side open.
func releaseDateRange(d DiscoverSpec) (gte, lte string) {
	if d.YearFrom != 0 {
		gte = fmt.Sprintf("%04d-01-01", d.YearFrom)
	}
	if d.YearTo != 0 {
		lte = fmt.Sprintf("%04d-12-31", d.YearTo)
	}
	return gte, lte
}

func (discoverKind) Source(env Env, row Row) Source {
	return discoverSource{env: env, spec: *row.Discover}
}

func (discoverKind) SourceKey(Row) string { return "" }

// tmdbListKind and mdbListKind are public lists read from a stored copy,
// which the refresher keeps current (see Refresher). Stored copies are keyed
// by source, so two rows showing one list share a copy and editing a row's
// title keeps it.
type tmdbListKind struct{}

func (tmdbListKind) Normalize(row *Row) FieldErrors {
	// The stored URL is the list page without its slug.
	return normalizeList(row, collectionutil.CanonicalTMDBListURL)
}

func (k tmdbListKind) Source(env Env, row Row) Source {
	return listSource{env: env, key: k.SourceKey(row)}
}

func (tmdbListKind) SourceKey(row Row) string {
	if row.List == nil {
		return ""
	}
	id, err := collectionutil.ParseTMDBListURL(row.List.URL)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("tmdb_list:%d", id)
}

type mdbListKind struct{}

func (mdbListKind) Normalize(row *Row) FieldErrors {
	// The stored URL is the list page, without the /json suffix its feed adds.
	return normalizeList(row, func(raw string) (string, error) {
		feed, err := collectionutil.CanonicalMDBListURL(raw)
		return strings.TrimSuffix(feed, "/json"), err
	})
}

func (k mdbListKind) Source(env Env, row Row) Source {
	return listSource{env: env, key: k.SourceKey(row)}
}

func (mdbListKind) SourceKey(row Row) string {
	if row.List == nil {
		return ""
	}
	feed, err := collectionutil.CanonicalMDBListURL(row.List.URL)
	if err != nil {
		return ""
	}
	return "mdblist:" + feed
}

// normalizeList stores a list row's URL in the canonical form the allowlist
// returns; a URL the allowlist refuses is never stored.
func normalizeList(row *Row, canonical func(string) (string, error)) FieldErrors {
	if errs := onlySpec(row, "list"); len(errs) > 0 {
		return errs
	}
	url, err := canonical(row.List.URL)
	if err != nil {
		return FieldErrors{"list.url": err.Error()}
	}
	row.List = &ListSpec{URL: url}
	return nil
}

// brandKind is a carousel of fixed brand cards the clients draw themselves.
type brandKind struct{}

func (brandKind) Normalize(row *Row) FieldErrors { return onlySpec(row, "") }
func (brandKind) Source(Env, Row) Source         { return nil }
func (brandKind) SourceKey(Row) string           { return "" }

// dedupe returns values without repeats, in first-seen order, in a new slice
// (nil when empty), leaving the caller's slice alone.
func dedupe[T comparable](values []T) []T {
	var out []T
	for _, v := range values {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
