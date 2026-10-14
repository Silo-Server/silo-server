package naming

import (
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// extraSuffixKinds maps the Jellyfin/Plex filename-suffix convention onto the
// shared extra-kind vocabulary (models.ExtraKind values). The suffix is the
// final "-token" (or ".token") of the stem: "Movie (2020)-trailer.mkv".
var extraSuffixKinds = map[string]string{
	"trailer":         "trailer",
	"teaser":          "teaser",
	"featurette":      "featurette",
	"clip":            "clip",
	"behindthescenes": "behind_the_scenes",
	"bloopers":        "bloopers",
	"deleted":         "deleted_scene",
	"deletedscene":    "deleted_scene",
	"interview":       "other",
	"scene":           "other",
	"short":           "other",
	"extra":           "other",
	"other":           "other",
}

// extrasDirKinds classifies supplemental directory names (normalized via
// NormalizeDirLabel) into the shared extra-kind vocabulary. The set mirrors
// the Jellyfin/Plex extras folder convention ("other" included: both
// conventions document it).
//
// Deliberately absent: the plural "others", which is in neither convention.
// The same words also name content-scope folders ("movies/other/<Movie>/",
// "tv/Kids/Shorts/"), so a name match alone never makes a folder an extras
// folder. extrasClassifier in internal/scanner also requires a title folder
// that holds media; deriveSeriesRoot only checks the folder's position and the
// file name, so a category folder below the library folder can still be read
// as an extras folder there.
var extrasDirKinds = map[string]models.ExtraKind{
	"extra":             models.ExtraKindOther,
	"extras":            models.ExtraKindOther,
	"other":             models.ExtraKindOther,
	"featurette":        models.ExtraKindFeaturette,
	"featurettes":       models.ExtraKindFeaturette,
	"behind the scenes": models.ExtraKindBehindTheScenes,
	"deleted scene":     models.ExtraKindDeletedScene,
	"deleted scenes":    models.ExtraKindDeletedScene,
	"trailer":           models.ExtraKindTrailer,
	"trailers":          models.ExtraKindTrailer,
	"teaser":            models.ExtraKindTeaser,
	"teasers":           models.ExtraKindTeaser,
	"clip":              models.ExtraKindClip,
	"clips":             models.ExtraKindClip,
	"bloopers":          models.ExtraKindBloopers,
	"interviews":        models.ExtraKindOther,
	"scenes":            models.ExtraKindOther,
	"shorts":            models.ExtraKindOther,
}

var dirLabelSeparators = strings.NewReplacer(".", " ", "_", " ", "-", " ")

// NormalizeDirLabel folds a directory name for convention lookups: lower
// case, with dots, underscores, and dashes read as spaces.
func NormalizeDirLabel(name string) string {
	return collapseWhitespace(dirLabelSeparators.Replace(strings.ToLower(name)))
}

// ExtrasDirKind reports whether name is an extras folder name ("Trailers",
// "Behind The Scenes", ...) and the extra kind it implies.
func ExtrasDirKind(name string) (models.ExtraKind, bool) {
	kind, ok := extrasDirKinds[NormalizeDirLabel(name)]
	return kind, ok
}

// IsExtrasDir reports whether name is an extras folder name.
func IsExtrasDir(name string) bool {
	_, ok := ExtrasDirKind(name)
	return ok
}

// ParseExtraSuffix reports whether the file name (or stem) carries a
// Jellyfin/Plex-style extras suffix, returning the mapped extra kind. Only
// the exact final token after the last '-' or '.' separator counts, so titles
// merely containing words like "scene" do not misclassify.
func ParseExtraSuffix(fileName string) (kind string, ok bool) {
	stem := strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	idx := strings.LastIndexAny(stem, "-.")
	if idx <= 0 || idx == len(stem)-1 {
		return "", false
	}
	token := strings.ToLower(strings.TrimSpace(stem[idx+1:]))
	mapped, found := extraSuffixKinds[token]
	if !found {
		return "", false
	}
	// A bare suffix with no title ("trailer.mkv" handled by ExtraTitleFromFile
	// callers via directory classification) still counts: idx>0 ensures some
	// title text precedes the separator.
	return mapped, true
}

// ExtraTitleFromFile derives a human-readable extra title from a file path,
// stripping a recognized extras suffix when present.
func ExtraTitleFromFile(filePath string) string {
	stem := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	if idx := strings.LastIndexAny(stem, "-."); idx > 0 && idx < len(stem)-1 {
		token := strings.ToLower(strings.TrimSpace(stem[idx+1:]))
		if _, found := extraSuffixKinds[token]; found {
			stem = stem[:idx]
		}
	}
	stem = strings.NewReplacer(".", " ", "_", " ").Replace(stem)
	return strings.Join(strings.Fields(stem), " ")
}
