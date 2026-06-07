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

// ParseFilter pulls apart an ABS `filter=` query value. Real ABS encodes the
// value as base64-then-URL-encoded — chi/http already URL-decodes the query,
// so the input we see is `<kind>.<base64-value>`. Two non-encoded special
// cases: the literal `no-series` sentinel and the `progress.*` family
// (in-progress / finished / not-finished). When the value isn't valid
// base64, we treat it as a sentinel and pass it through unchanged.
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

	// progress.* and the no-series sentinel are never base64-encoded by
	// real ABS clients.
	if kind == FilterProgress || rest == SentinelNoSeries {
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
	// Fall through — accept the raw string as a sentinel. Future ABS
	// versions may add more sentinels (mirroring how no-series escaped the
	// base64 encoding); this keeps us forward-compatible.
	out.Value = rest
	return out, true
}

// Matches reports whether the given LibraryItem satisfies this filter. genres
// is best-effort — backend summaries don't carry genres, so a genres filter
// matches nothing unless the caller pre-populated the field via detail
// lookups (we currently don't). tags / languages behave the same way and
// are accepted for forward-compat but always return false.
//
// Progress filters require the caller to supply an optional `inProgress`
// and `finished` flag for the book; without them the progress branch
// returns false. The caller is responsible for joining progress state from
// the store.
func (f Filter) Matches(item LibraryItem, inProgress, finished bool, hasProgress bool) bool {
	switch f.Kind {
	case FilterAuthors:
		for _, a := range item.Media.Metadata.Authors {
			if a.ID == f.Value || a.Name == f.Value {
				return true
			}
		}
		return false
	case FilterSeries:
		if f.Value == SentinelNoSeries {
			return len(item.Media.Metadata.Series) == 0
		}
		for _, s := range item.Media.Metadata.Series {
			if s.ID == f.Value || s.Name == f.Value {
				return true
			}
		}
		return false
	case FilterNarrators:
		for _, n := range item.Media.Metadata.Narrators {
			if n == f.Value {
				return true
			}
		}
		return false
	case FilterProgress:
		switch f.Value {
		case "in-progress":
			return hasProgress && inProgress && !finished
		case "finished":
			return hasProgress && finished
		case "not-finished":
			return !finished
		case "not-started":
			return !hasProgress
		}
		return false
	default:
		// genres / tags / languages — not derivable from a summary today.
		return false
	}
}
