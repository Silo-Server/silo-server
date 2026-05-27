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
	MaxContentRating      string
	MaxPlaybackQuality    string
	SelectedFileID        int
	UserID                int
	ProfileID             string
	// NamePrefix, when non-empty, restricts results to items whose
	// LOWER(COALESCE(NULLIF(BTRIM(sort_title),''), title)) starts with the
	// given (case-insensitive) prefix. Pushed into the SQL WHERE clause so
	// the predicate can use idx_media_items_sort_key.
	NamePrefix string
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
