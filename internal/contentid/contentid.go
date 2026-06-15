// Package contentid derives deterministic, cross-server stable content IDs for
// logical media items (movies, series, seasons, episodes).
//
// Historically every logical item was minted a Sonyflake ID — a locally
// generated, time-ordered number that differs per server for the same title.
// content_id is the anchor that artwork, metadata, watch history, progress,
// favorites, ratings, collections and credits hang off, so a per-server ID
// means two servers holding the same movie cannot share any of that state.
//
// This package replaces that with a structured natural key derived from the
// provider IDs already embedded in the library:
//
//	movie-<provider>-<id>                      e.g. movie-tmdb-228064
//	series-<provider>-<id>                     e.g. series-tvdb-296762
//	season-<provider>-<seriesId>-<seasonNo>    e.g. season-tvdb-296762-1
//	episode-<provider>-<seriesId>-<s>-<e>      e.g. episode-tvdb-296762-1-5
//	local-<hex112>                             unmatched / local fallback
//
// Provider IDs are unique by construction, so the value is collision-free with
// no hashing. The leading entity-type token domain-separates the namespaces so
// a movie and an episode can never alias on a coincidental number.
//
// The component separator is "-" (an RFC 3986 unreserved character) rather than
// ":". Every component is [a-z0-9]+ (or "tt"+digits for IMDb), so "-" is an
// unambiguous delimiter, and because it needs no percent-encoding the id is its
// own tidy URL path segment — /item/series-tvdb-296762, identical to what is
// stored in the database (no encode/decode, greppable as-is). See sep.
//
// Load-bearing format invariant: an episode/season ID embeds its series anchor,
// so the series content_id is always a pure string transform of the
// episode/season ID (see SeriesIDFromContentID). The watch-history query relies
// on this to resolve a show without an episodes table lookup. Never break it.
//
// See docs/architecture/deterministic-content-id.md for the full rationale.
package contentid

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// SchemeVersion freezes the provider precedence and the exact string format.
// Changing either re-IDs items, so it is a deliberate, rare event that forces a
// full remap (see the migration machinery). It is intentionally a code constant,
// not a per-row column: content_id has no lasting mixed-version population
// because any scheme change normalizes every row at once.
const SchemeVersion = 1

// sep joins the components of a content_id. It is an RFC 3986 unreserved
// character, so a content_id is URL-safe verbatim (encodeURIComponent is a
// no-op) and survives as a clean path segment with no percent-encoding. It is
// part of the frozen format (SchemeVersion) and mirrored by the literal
// delimiters in 20260612130000_deterministic_content_id.sql and the split_part
// transform in internal/catalog/history_source.go — changing it here without
// changing those re-IDs items inconsistently. No component ever contains it
// (all are [a-z0-9]+ / "tt"+digits), so it is an unambiguous delimiter.
const sep = "-"

// Entity-type tokens. These domain-separate the provider-number namespaces.
const (
	kindMovie   = "movie"
	kindSeries  = "series"
	kindSeason  = "season"
	kindEpisode = "episode"
	kindLocal   = "local"
)

// Canonical provider tokens.
const (
	ProviderTMDB = "tmdb"
	ProviderIMDB = "imdb"
	ProviderTVDB = "tvdb"
)

// movieProviderPrecedence and seriesProviderPrecedence freeze, per the scheme
// version, which provider anchors a logical item when more than one tag is
// present. Two servers that both see the tags therefore pick the same anchor.
// They are unexported and never returned by reference: the precedence is part of
// SchemeVersion and is mirrored by the hard-coded order in
// 20260612130000_deterministic_content_id.sql, so a stray runtime mutation would
// silently mint ids that no longer match the migration.
//
//   - Movies prefer TMDB (the richest movie source).
//   - Series/Season/Episode prefer TVDB (the traditional episode-canonical
//     source).
var (
	movieProviderPrecedence  = []string{ProviderTMDB, ProviderIMDB, ProviderTVDB}
	seriesProviderPrecedence = []string{ProviderTVDB, ProviderTMDB, ProviderIMDB}
)

// ProviderIDs carries the raw provider identifiers for an item as found on the
// denormalized columns or parsed from folder/file tags. Empty fields are
// ignored.
type ProviderIDs struct {
	Tmdb string
	Imdb string
	Tvdb string
}

func (p ProviderIDs) get(provider string) string {
	switch provider {
	case ProviderTMDB:
		return p.Tmdb
	case ProviderIMDB:
		return p.Imdb
	case ProviderTVDB:
		return p.Tvdb
	}
	return ""
}

// numericIDPattern validates a tmdb/tvdb id: a non-empty run of digits.
var numericIDPattern = regexp.MustCompile(`^[0-9]+$`)

// imdbIDPattern validates an imdb id: the literal "tt" followed by digits.
var imdbIDPattern = regexp.MustCompile(`^tt[0-9]+$`)

// normalizeProviderID normalizes and validates a provider id for use in a
// content_id. Returns the normalized id and whether it is usable. IMDb ids are
// lowercased and must retain their "tt" prefix; tmdb/tvdb ids must be a bare
// run of digits with no leading-zero rewriting (the provider's canonical form).
func normalizeProviderID(provider, raw string) (string, bool) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", false
	}
	switch provider {
	case ProviderIMDB:
		id = strings.ToLower(id)
		if !imdbIDPattern.MatchString(id) {
			return "", false
		}
		return id, true
	case ProviderTMDB, ProviderTVDB:
		if !numericIDPattern.MatchString(id) {
			return "", false
		}
		return id, true
	default:
		return "", false
	}
}

// anchor picks the canonical (provider, id) for an item under the given
// precedence, returning ok=false when no usable provider id is present.
func anchor(ids ProviderIDs, precedence []string) (provider, id string, ok bool) {
	for _, p := range precedence {
		if norm, valid := normalizeProviderID(p, ids.get(p)); valid {
			return p, norm, true
		}
	}
	return "", "", false
}

// ForMovie returns the deterministic content_id for a movie, or ("", false) if
// no provider anchor is present (the caller should fall back to ForLocal).
func ForMovie(ids ProviderIDs) (string, bool) {
	provider, id, ok := anchor(ids, movieProviderPrecedence)
	if !ok {
		return "", false
	}
	return kindMovie + sep + provider + sep + id, true
}

// ForSeries returns the deterministic content_id for a series, or ("", false)
// if no provider anchor is present.
func ForSeries(ids ProviderIDs) (string, bool) {
	provider, id, ok := anchor(ids, seriesProviderPrecedence)
	if !ok {
		return "", false
	}
	return kindSeries + sep + provider + sep + id, true
}

// seriesBody returns the "<provider>-<id>" portion of a provider-anchored series
// content_id, e.g. "tvdb-296762" for "series-tvdb-296762". Returns ok=false for
// any id that is not a provider-anchored series (local series, legacy Sonyflake
// ids, etc.) so seasons/episodes correctly fall back instead of composing a
// malformed key.
func seriesBody(seriesContentID string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(seriesContentID), kindSeries+sep)
	if !ok || rest == "" {
		return "", false
	}
	// Guard the format invariant: exactly "<provider>-<id>" with a known,
	// validated provider and id. Anything else cannot anchor children.
	parts := strings.Split(rest, sep)
	if len(parts) != 2 {
		return "", false
	}
	if _, valid := normalizeProviderID(parts[0], parts[1]); !valid {
		return "", false
	}
	return rest, true
}

// ForSeason composes a season content_id from its parent series content_id and
// the season number. It relies on the format invariant: the season key embeds
// the series anchor. Returns ("", false) when the series is not provider-
// anchored, so the caller falls back to a local/legacy id.
func ForSeason(seriesContentID string, seasonNumber int) (string, bool) {
	body, ok := seriesBody(seriesContentID)
	if !ok {
		return "", false
	}
	return kindSeason + sep + body + sep + strconv.Itoa(seasonNumber), true
}

// ForEpisode composes an episode content_id from its parent series content_id
// and the season/episode numbers. Episodes compose from the series anchor plus
// numbers (both universally present in filenames), not their own provider
// episode IDs which are often missing. Returns ("", false) when the series is
// not provider-anchored.
func ForEpisode(seriesContentID string, seasonNumber, episodeNumber int) (string, bool) {
	body, ok := seriesBody(seriesContentID)
	if !ok {
		return "", false
	}
	return kindEpisode + sep + body + sep +
		strconv.Itoa(seasonNumber) + sep + strconv.Itoa(episodeNumber), true
}

// ForLocal returns a content_id in the disjoint "local-" namespace for an item
// with no provider anchor, derived from a normalized path so the same library
// on the same server stays stable across rescans. It can never collide with a
// provider-derived id. An item's local id changes if it is later matched to a
// provider — rare, and such items seldom carry watch state.
//
// The path is NFC-normalized and trimmed before hashing; the hash is the first
// localHashLen bytes (112 bits) of SHA-256, hex-encoded. That width is chosen so
// the id packs losslessly into a Jellyfin-compat UUID (see Pack); 112 bits is
// far beyond any single server's local-item count. Cross-server stability for
// local items is best-effort only (paths differ between servers).
func ForLocal(path string) string {
	normalized := norm.NFC.String(strings.TrimSpace(path))
	sum := sha256.Sum256([]byte(normalized))
	return kindLocal + sep + hex.EncodeToString(sum[:localHashLen])
}

// SeriesIDFromContentID derives the owning series content_id from an episode or
// season content_id by pure string transform — no catalog lookup — per the
// format invariant. For a movie or series id it returns the id unchanged (a
// movie is its own display item; a series id is already a series id). For a
// local episode/season (no embedded series anchor) or any unrecognized id it
// returns ok=false, signaling the caller to fall back to a resolved lookup.
//
//	episode-tvdb-296762-1-5 -> series-tvdb-296762
//	season-tvdb-296762-1    -> series-tvdb-296762
//	movie-tmdb-228064       -> movie-tmdb-228064  (unchanged)
//	series-tvdb-296762      -> series-tvdb-296762  (unchanged)
func SeriesIDFromContentID(contentID string) (string, bool) {
	id := strings.TrimSpace(contentID)
	if strings.HasPrefix(id, kindMovie+sep) || strings.HasPrefix(id, kindSeries+sep) {
		// A movie is its own display item; a series id is already a series id.
		// Still require the anchor to be well-formed so a truncated "series-"
		// cannot pass through unchanged.
		if !IsProviderAnchored(id) {
			return "", false
		}
		return id, true
	}
	// episode/season carry an embedded "<provider>-<seriesId>" anchor; recover it
	// by pure string transform. parseAnchored enforces the full per-kind arity, so
	// a truncated "episode-tvdb-296762" (missing season/episode) fails closed
	// rather than aliasing onto a series.
	if provider, seriesID, ok := parseAnchored(id); ok {
		return kindSeries + sep + provider + sep + seriesID, true
	}
	// local ids and legacy Sonyflake ids have no embedded anchor.
	return "", false
}

// IsProviderAnchored reports whether the content_id is a fully-formed
// provider-derived key (movie/series/season/episode), as opposed to a local id,
// a legacy Sonyflake id, or a truncated/malformed anchor. Used by migration and
// diagnostics to tell remappable items apart.
func IsProviderAnchored(contentID string) bool {
	_, _, ok := parseAnchored(strings.TrimSpace(contentID))
	return ok
}

// parseAnchored validates a provider-anchored content_id against the exact
// per-kind arity and component shape, returning the canonical (provider,
// seriesId-or-id) anchor when it is well-formed:
//
//	movie-<p>-<id>                 -> (p, id)
//	series-<p>-<id>                -> (p, id)
//	season-<p>-<sid>-<n>           -> (p, sid)   n must be a base-10 integer
//	episode-<p>-<sid>-<s>-<e>      -> (p, sid)   s,e must be base-10 integers
//
// It fails closed for any other shape (wrong part count, non-numeric
// season/episode, unknown/invalid provider id, local or legacy ids).
func parseAnchored(id string) (provider, seriesID string, ok bool) {
	kind, rest, found := strings.Cut(id, sep)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, sep)
	var wantParts int
	switch kind {
	case kindMovie, kindSeries:
		wantParts = 2 // <provider>-<id>
	case kindSeason:
		wantParts = 3 // <provider>-<seriesId>-<seasonNo>
	case kindEpisode:
		wantParts = 4 // <provider>-<seriesId>-<seasonNo>-<episodeNo>
	default:
		return "", "", false
	}
	if len(parts) != wantParts {
		return "", "", false
	}
	if _, valid := normalizeProviderID(parts[0], parts[1]); !valid {
		return "", "", false
	}
	// The trailing season/episode numbers must be base-10 integers.
	for _, n := range parts[2:] {
		if !numericIDPattern.MatchString(n) {
			return "", "", false
		}
	}
	return parts[0], parts[1], true
}

// IsLocal reports whether the content_id is in the local fallback namespace.
func IsLocal(contentID string) bool {
	return strings.HasPrefix(strings.TrimSpace(contentID), kindLocal+sep)
}
