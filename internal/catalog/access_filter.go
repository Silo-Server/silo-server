package catalog

import (
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

// AccessFilter captures effective viewer access constraints for catalog reads.
type AccessFilter struct {
	AllowedLibraryIDs     []int
	AllowedContentIDs     []string
	DisabledLibraryIDs    []int // user-disabled libraries (only set when AllowedLibraryIDs is nil)
	PresentationLibraryID *int
	PresentationLanguage  string
	// ProfilePreferredLanguage is the viewer profile's preferred metadata
	// language. Presentation language resolves: explicit PresentationLanguage
	// → ProfilePreferredLanguage → the library's metadata_language.
	ProfilePreferredLanguage string
	MaxContentRating         string
	MaxPlaybackQuality       string
	SelectedFileID           int
	UserID                   int
	ProfileID                string
	// NamePrefix, when non-empty, restricts results to items whose
	// LOWER(COALESCE(NULLIF(BTRIM(sort_title),''), title)) starts with the
	// given (case-insensitive) prefix. Pushed into the SQL WHERE clause so
	// the predicate can use idx_media_items_sort_key.
	NamePrefix string
	// ExcludedMediaTypes lists media_items.type values the viewer's surface
	// never exposes (e.g. the Jellyfin compat layer excludes "audiobook" and
	// "podcast" — they're served by the ABS-compat API instead). Applied by
	// every query builder that consumes an AccessFilter.
	ExcludedMediaTypes []string
}

func applyAccessFilter(alias string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	if filter.MaxContentRating != "" {
		allowedRatings := access.AllowedRatingsUpTo(filter.MaxContentRating)
		if len(allowedRatings) == 0 {
			*conditions = append(*conditions, "1 = 0")
		} else {
			*conditions = append(*conditions, fmt.Sprintf("%s.content_rating = ANY($%d)", alias, *argIdx))
			*args = append(*args, allowedRatings)
			*argIdx = *argIdx + 1
		}
	}
	if len(filter.ExcludedMediaTypes) > 0 {
		*conditions = append(*conditions, fmt.Sprintf("NOT (%s.type = ANY($%d))", alias, *argIdx))
		*args = append(*args, filter.ExcludedMediaTypes)
		*argIdx = *argIdx + 1
	}
}

// libraryAccessConditions returns the per-item library allow/deny predicates
// gating keyColumn's membership rows in media_item_libraries (e.g.
// "mi.content_id", or "e.series_id" for episode access resolved through the
// parent series). allowedIdx and disabledIdx are 1-based SQL placeholder
// positions for the allowed/disabled folder-ID arrays; 0 means that restriction
// is not active.
//
// The predicates are independent EXISTS / NOT EXISTS subqueries — never
// allow/deny checks against one joined membership row. The single-join form
// leaks: an item linked to BOTH a passing library and a disabled one satisfies
// the predicates via the passing row (audit 2026-05-01 §3.3; review finding C3).
//
// When only a disabled list is active, positive membership is still required:
// orphan items (no media_item_libraries link — mid-scan, stale rows from a
// removed library, or metadata-refresh inserts not yet linked) must not become
// visible to a restricted viewer through a vacuous NOT EXISTS. When an allowed
// list is active it already implies membership, so no extra EXISTS is added.
func libraryAccessConditions(keyColumn string, allowedIdx, disabledIdx int) []string {
	var conditions []string
	if allowedIdx > 0 {
		conditions = append(conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s AND mil.media_folder_id = ANY($%d))",
			keyColumn, allowedIdx))
	}
	if disabledIdx > 0 {
		if allowedIdx == 0 {
			conditions = append(conditions, fmt.Sprintf(
				"EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s)",
				keyColumn))
		}
		conditions = append(conditions, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s AND mil.media_folder_id = ANY($%d))",
			keyColumn, disabledIdx))
	}
	return conditions
}

// appendLibraryAccessConditions binds the filter's library restrictions as args
// and appends the matching libraryAccessConditions predicates for keyColumn.
func appendLibraryAccessConditions(keyColumn string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	var allowedIdx, disabledIdx int
	if filter.AllowedLibraryIDs != nil {
		*args = append(*args, filter.AllowedLibraryIDs)
		allowedIdx = *argIdx
		*argIdx = *argIdx + 1
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		*args = append(*args, filter.DisabledLibraryIDs)
		disabledIdx = *argIdx
		*argIdx = *argIdx + 1
	}
	*conditions = append(*conditions, libraryAccessConditions(keyColumn, allowedIdx, disabledIdx)...)
}

// ApplySectionAccessFilter applies non-library access constraints to section queries.
func ApplySectionAccessFilter(alias string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	applyAccessFilter(alias, filter, conditions, args, argIdx)
}

// FileAllowedByAccess reports whether a media file fits within the viewer's
// effective access policy.
func FileAllowedByAccess(file *models.MediaFile, filter AccessFilter) bool {
	if file == nil {
		return false
	}
	if filter.AllowedLibraryIDs != nil && !intInSlice(file.MediaFolderID, filter.AllowedLibraryIDs) {
		return false
	}
	if len(filter.DisabledLibraryIDs) > 0 && intInSlice(file.MediaFolderID, filter.DisabledLibraryIDs) {
		return false
	}
	return access.QualityAllowed(file.Resolution, filter.MaxPlaybackQuality)
}

func intInSlice(value int, values []int) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// FilterMediaFilesByAccess drops file versions that exceed the viewer's
// effective quality ceiling.
func FilterMediaFilesByAccess(files []*models.MediaFile, filter AccessFilter) []*models.MediaFile {
	if len(files) == 0 || strings.TrimSpace(filter.MaxPlaybackQuality) == "" {
		return files
	}

	filtered := make([]*models.MediaFile, 0, len(files))
	for _, file := range files {
		if FileAllowedByAccess(file, filter) {
			filtered = append(filtered, file)
		}
	}
	return filtered
}
