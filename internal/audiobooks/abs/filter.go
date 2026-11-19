package abs

import (
	"encoding/base64"
	"strings"
)

// FilterKind is the leading segment of an ABS `filter=` query value.
type FilterKind string

const (
	FilterAuthors   FilterKind = "authors"
	FilterSeries    FilterKind = "series"
	FilterNarrators FilterKind = "narrators"
	FilterGenres    FilterKind = "genres"
	FilterProgress  FilterKind = "progress"
	FilterTags      FilterKind = "tags"
	FilterLanguages FilterKind = "languages"
)

// SentinelNoSeries is the literal value real ABS clients send for "books
// without a series" — it is NOT base64-encoded, in contrast to ordinary
// series IDs which are.
const SentinelNoSeries = "no-series"

// Filter describes a parsed ABS `filter=<kind>.<value>` query parameter.
// Value is the post-decode value (base64-decoded for most kinds; sentinel
// values such as "no-series" are passed through). Raw preserves the
// original `<kind>.<value>` for echoing back in pagination envelopes.
type Filter struct {
	Kind  FilterKind
	Value string
	Raw   string
}

// ParseFilter pulls apart an ABS `filter=` query value. Real ABS clients
// base64-encode every value, progress states and the no-series sentinel
// included (the app's filter sheet sends `progress.` + base64 of
// "in-progress"), and the ABS server decodes every value. chi/http already
// URL-decodes the query, so the input we see is `<kind>.<base64-value>`.
// Older or hand-written clients send progress states and no-series in the
// clear, so a known plain value is taken as is. Any other value that isn't
// valid base64 is passed through unchanged.
//
// Returns (Filter{}, false) when raw is empty or has no kind prefix.
func ParseFilter(raw string) (Filter, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Filter{}, false
	}
	dot := strings.IndexByte(raw, '.')
	if dot <= 0 || dot >= len(raw)-1 {
		return Filter{}, false
	}
	kind := FilterKind(raw[:dot])
	rest := raw[dot+1:]
	out := Filter{Kind: kind, Raw: raw}

	if rest == SentinelNoSeries || (kind == FilterProgress && isProgressValue(rest)) {
		out.Value = rest
		return out, true
	}

	if b, err := base64.RawURLEncoding.DecodeString(rest); err == nil && len(b) > 0 {
		out.Value = string(b)
		return out, true
	}
	if b, err := base64.RawStdEncoding.DecodeString(rest); err == nil && len(b) > 0 {
		out.Value = string(b)
		return out, true
	}
	if b, err := base64.StdEncoding.DecodeString(rest); err == nil && len(b) > 0 {
		out.Value = string(b)
		return out, true
	}
	out.Value = rest
	return out, true
}

func isProgressValue(v string) bool {
	switch v {
	case ProgressInProgress, ProgressAudioInProgress, ProgressFinished, ProgressNotStarted, ProgressNotFinished,
		"ebook-in-progress", "ebook-finished":
		return true
	}
	return false
}

// ItemsQuery carries the ABS /libraries/{id}/items options the store applies
// in SQL: the parsed filter and the requested sort. UserID and ProfileID name
// the viewer whose listening progress the progress filter and the "progress"
// sort read; a progress filter matches only that profile's rows.
type ItemsQuery struct {
	Filter    Filter
	Sort      string
	Desc      bool
	UserID    string
	ProfileID string
}

// Progress filter values real ABS clients send (libraryItemsBookFilters.js).
// audio-in-progress is in-progress restricted to books with audio, which every
// Silo audiobook is. The ebook-* values match books without audio files, so
// they match nothing here.
const (
	ProgressInProgress      = "in-progress"
	ProgressAudioInProgress = "audio-in-progress"
	ProgressFinished        = "finished"
	ProgressNotStarted      = "not-started"
	ProgressNotFinished     = "not-finished"
)
