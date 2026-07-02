package jellycompat

import "strings"

// Search-path guard policy for every jellycompat search EXCEPT the
// Meilisearch-backed /Items media search.
//
// Short, recursive type-ahead terms (e.g. a single "a") against the PostgreSQL
// people index and the in-memory collection/box-set filter produced multi-second
// scans that pegged the server when a client fired one search per keystroke.
// Anything that is not offloaded to Meilisearch is therefore gated: a provided
// term must be longer than auxSearchMinTermLen to run at all, and a guarded path
// never returns more than auxSearchMaxResults rows.
//
// The minimum allowed term length is 3 runes (auxSearchMinTermLen=2 is the
// longest length still rejected, so 1-2 runes are rejected and 3+ are allowed).
// 3 runes is the point where a pg_trgm trigram index becomes usable: a 2-char
// pattern yields no complete trigram, so it cannot be served from the people
// name trigram index and degrades to a ~400ms full scan, whereas any 3+ char
// term is trigram-eligible and stays fast. Aligning the gate with the trigram
// floor lets legitimate short titles/names ("300", "Saw", 3-letter actors) and
// 3-char type-ahead hints through.
const (
	// auxSearchMinTermLen is the maximum SearchTerm length (counted in runes,
	// after trimming) that is rejected. A non-empty term of this length or
	// shorter returns no results WITHOUT querying any backend; only terms
	// strictly longer than this are allowed to run.
	auxSearchMinTermLen = 2
	// auxSearchMaxResults caps how many results a guarded search path may return,
	// regardless of the client-requested Limit.
	auxSearchMaxResults = 20
)

// auxSearchTermTooShort reports whether a non-empty SearchTerm is too short to
// run on a guarded (non-/Items) search path.
//
// An empty term is NOT a search — it is a browse/list request — so it is never
// reported as too short; callers keep their existing empty-term behavior (the
// result limit is still clamped separately via clampAuxSearchLimit).
func auxSearchTermTooShort(term string) bool {
	trimmed := strings.TrimSpace(term)
	if trimmed == "" {
		return false
	}
	return len([]rune(trimmed)) <= auxSearchMinTermLen
}

// clampAuxSearchLimit caps a client-requested limit to auxSearchMaxResults for
// guarded search paths. A non-positive request collapses to the cap.
func clampAuxSearchLimit(requested int) int {
	if requested <= 0 || requested > auxSearchMaxResults {
		return auxSearchMaxResults
	}
	return requested
}
