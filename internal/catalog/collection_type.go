package catalog

import "strings"

// IsLiveQueryType returns true for collection types that derive membership
// from a query at read time rather than stored items.
func IsLiveQueryType(collectionType string) bool {
	return strings.EqualFold(strings.TrimSpace(collectionType), "smart")
}

// IsSyncableType returns true for collection types that support external sync.
func IsSyncableType(collectionType string) bool {
	switch strings.TrimSpace(strings.ToLower(collectionType)) {
	case "mdblist", "tmdb", "trakt":
		return true
	default:
		return false
	}
}

// IntersectCollectionLibraryIDs narrows a smart collection's query library scope
// (existing) to the libraries the collection is bound to (required), returning a
// copy of the other set when one side is empty. Shared by the web API and the
// Jellyfin-compat collection-item resolvers so their scoping stays identical.
func IntersectCollectionLibraryIDs(existing, required []int) []int {
	if len(required) == 0 {
		return append([]int(nil), existing...)
	}
	if len(existing) == 0 {
		return append([]int(nil), required...)
	}

	allowed := make(map[int]struct{}, len(required))
	for _, id := range required {
		allowed[id] = struct{}{}
	}

	result := make([]int, 0, len(existing))
	seen := make(map[int]struct{}, len(existing))
	for _, id := range existing {
		if _, ok := allowed[id]; !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}
