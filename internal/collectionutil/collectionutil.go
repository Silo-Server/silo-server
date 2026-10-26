package collectionutil

import (
	"errors"
	"math"
	"strings"
)

var ErrOrderedIDsMismatch = errors.New("ordered_ids does not match the current set")

const (
	collectionSourceFetchMultiplier = 4
	collectionSourceFetchMin        = 100
)

// SourceFetchLimit is how many source entries a sync reads for a collection
// with an explicit item limit: a few times the limit, because some entries
// don't match the catalog or the owner's access. It returns 0, meaning the
// source's whole list, when the collection has no item limit or the scan
// depth would overflow.
func SourceFetchLimit(itemLimit *int) int {
	if itemLimit == nil || *itemLimit <= 0 || *itemLimit > math.MaxInt/collectionSourceFetchMultiplier {
		return 0
	}
	return max(*itemLimit*collectionSourceFetchMultiplier, collectionSourceFetchMin)
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
