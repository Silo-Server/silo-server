package catalog

import "strings"

const collectionTypeSmart = "smart"

// The collection types a sync fills from an external list.
const (
	collectionTypeMDBList = "mdblist"
	collectionTypeTMDB    = "tmdb"
)

// IsKnownType reports whether collectionType is a value the
// library_collections collection_type CHECK constraint accepts. Values are
// stored lowercase, so the comparison is exact.
func IsKnownType(collectionType string) bool {
	switch collectionType {
	case libraryCollectionTypeManual, collectionTypeSmart, collectionTypeMDBList, collectionTypeTMDB:
		return true
	default:
		return false
	}
}

// IsLiveQueryType returns true for collection types that derive membership
// from a query at read time rather than stored items.
func IsLiveQueryType(collectionType string) bool {
	return strings.EqualFold(strings.TrimSpace(collectionType), collectionTypeSmart)
}

// IsSyncableType returns true for collection types that support external sync.
// Mirrored by isListBackedCollectionType in web/src/lib/collections/types.ts.
func IsSyncableType(collectionType string) bool {
	switch strings.TrimSpace(strings.ToLower(collectionType)) {
	case collectionTypeMDBList, collectionTypeTMDB:
		return true
	default:
		return false
	}
}
