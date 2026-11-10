package catalog

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// NormalizeSortPreferenceRef is the identity a saved sort preference is
// stored under: its collection kind, and the collection's id, which is
// userstore.PersonalSortPreferenceCollectionID for the personal lists
// (watchlist, favorites), whatever id the caller sent. False for an unknown
// kind or a collection kind without an id. The sort editor and the backup
// importer both store preferences under this identity.
func NormalizeSortPreferenceRef(rawKind, rawID string) (kind, collectionID string, ok bool) {
	kind = strings.ToLower(strings.TrimSpace(rawKind))
	if IsPersonalSortPreferenceKind(kind) {
		return kind, userstore.PersonalSortPreferenceCollectionID, true
	}
	if kind != userstore.CollectionKindLibrary && kind != userstore.CollectionKindUser {
		return "", "", false
	}
	collectionID = strings.TrimSpace(rawID)
	if collectionID == "" {
		return "", "", false
	}
	return kind, collectionID, true
}

// IsPersonalSortPreferenceKind reports whether kind is a personal list's.
func IsPersonalSortPreferenceKind(kind string) bool {
	return kind == userstore.CollectionKindWatchlist || kind == userstore.CollectionKindFavorites
}

// NormalizeSortPreference validates a saved sort for a collection of kind
// (as NormalizeSortPreferenceRef returns it) and applies the field's
// default order. An empty field is valid and clears the preference.
func NormalizeSortPreference(kind, rawField, rawOrder string) (field, order string, ok bool) {
	field = strings.ToLower(strings.TrimSpace(rawField))
	if field == "" {
		return "", "", true
	}
	if IsPersonalSortPreferenceKind(kind) {
		qs, ok := NormalizePersonalSourceSort(field, rawOrder)
		return qs.Field, qs.Order, ok
	}
	qs, ok := NormalizeCollectionSort(field, rawOrder, true)
	return qs.Field, qs.Order, ok
}

// NormalizeCollectionSortPreference is NormalizeSortPreferenceRef and
// NormalizeSortPreference together: the identity, field, and order the
// sort editor stores a preference under, or false when it refuses one.
func NormalizeCollectionSortPreference(rawKind, rawID, rawField, rawOrder string) (kind, collectionID, field, order string, ok bool) {
	kind, collectionID, ok = NormalizeSortPreferenceRef(rawKind, rawID)
	if !ok {
		return "", "", "", "", false
	}
	field, order, ok = NormalizeSortPreference(kind, rawField, rawOrder)
	return kind, collectionID, field, order, ok
}
