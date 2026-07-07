package collectionutil

import (
	"errors"
	"strings"
)

var ErrOrderedIDsMismatch = errors.New("ordered_ids does not match the current set")

const (
	collectionSourceFetchMultiplier = 4
	collectionSourceFetchMin        = 100
	collectionSourceFetchMax        = 500
)

// MaxExplicitItemLimit is the largest explicit per-collection item limit the
// import APIs accept. Sync never scans more than collectionSourceFetchMax
// source entries, so a larger explicit limit could never be satisfied anyway.
// Mirrored by COLLECTION_MAX_ITEMS in web/src/lib/collectionTemplates.ts.
const MaxExplicitItemLimit = collectionSourceFetchMax

func SourceFetchLimit(itemLimit *int) int {
	if itemLimit == nil || *itemLimit <= 0 {
		return 0
	}
	limit := *itemLimit * collectionSourceFetchMultiplier
	if limit < collectionSourceFetchMin {
		limit = collectionSourceFetchMin
	}
	if limit > collectionSourceFetchMax {
		limit = collectionSourceFetchMax
	}
	return limit
}

func ItemLimitReached(itemCount int, itemLimit *int) bool {
	return itemLimit != nil && *itemLimit > 0 && itemCount >= *itemLimit
}

func HasDuplicateOrderedIDs(ids []string) bool {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

func SlugifyGroupSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '-' || r == '_':
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "group"
	}
	return out
}
