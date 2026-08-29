// Package artworkkey owns the object-key naming contract for cached artwork.
// Legacy names such as original.webp and revisioned names such as
// original.<revision>.webp are both supported.
package artworkkey

import (
	"path"
	"strconv"
	"strings"
)

const OriginalVariant = "original"

// Image types the variant ladder is keyed by. These name the directory segment
// in a cached artwork key (".../{imageType}/{variant}.{ext}") and the argument
// every ladder lookup takes, so they are the shared vocabulary for the packages
// that resolve artwork rather than five copies of the same string literals.
const (
	ImagePoster   = "poster"
	ImageBackdrop = "backdrop"
	ImageStill    = "still"
	ImageLogo     = "logo"
	ImageProfile  = "profile"
)

// LadderVersion identifies the current shape of the variant ladder returned by
// VariantWidths. It MUST be bumped whenever VariantWidths changes so the
// one-shot ladder backfill re-enqueues already-cached artwork and generates the
// newly-added rungs. Existing deployments compare their recorded
// backfilled_version against this value; leaving it stale means clients asking
// for a new rung keep falling back to the next lower one forever.
const LadderVersion = 2

// Build returns an object key for a variant under basePath.
func Build(basePath, variant, revision, ext string) string {
	basePath = strings.TrimRight(strings.TrimSpace(basePath), "/")
	variant = strings.TrimSpace(variant)
	revision = strings.TrimSpace(revision)
	if basePath == "" || variant == "" {
		return ""
	}
	if ext == "" {
		ext = ".webp"
	} else if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if revision == "" {
		return basePath + "/" + variant + ext
	}
	return basePath + "/" + variant + "." + revision + ext
}

// Original returns the original-variant key under basePath.
func Original(basePath, revision, ext string) string {
	return Build(basePath, OriginalVariant, revision, ext)
}

// Variant rewrites an original key to another variant while retaining any
// revision and extension. Unrecognized paths pass through unchanged.
func Variant(originalPath, variant string) string {
	if originalPath == "" || variant == "" || variant == OriginalVariant {
		return originalPath
	}
	dir := path.Dir(originalPath)
	base := path.Base(originalPath)
	if dir == "." || !strings.HasPrefix(base, OriginalVariant+".") {
		return originalPath
	}
	return strings.TrimRight(dir, "/") + "/" + variant + strings.TrimPrefix(base, OriginalVariant)
}

// Directory returns the image-type prefix containing every revision and
// variant for an artwork key, including a trailing slash.
func Directory(objectPath string) string {
	objectPath = strings.TrimSpace(objectPath)
	if objectPath == "" || strings.Contains(objectPath, "://") {
		return ""
	}
	dir := path.Dir(objectPath)
	if dir == "." || dir == "/" {
		return ""
	}
	return strings.TrimRight(dir, "/") + "/"
}

// Revision extracts the content revision from a revisioned key. Legacy keys
// return an empty string.
func Revision(objectPath string) string {
	name := path.Base(strings.TrimSpace(objectPath))
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	firstDot := strings.IndexByte(stem, '.')
	if firstDot < 0 || firstDot == len(stem)-1 {
		return ""
	}
	return stem[firstDot+1:]
}

// VariantWidths returns the resize widths generated for an artwork type,
// ordered widest first. This is the single source of truth for the variant
// ladder: image generation, object-key expansion, garbage collection, and the
// client-selectable image sizes in internal/imagesize all derive from it.
//
// Bump LadderVersion whenever this function changes.
func VariantWidths(imageType string) []int {
	switch strings.ToLower(strings.TrimSpace(imageType)) {
	case ImageBackdrop:
		return []int{1920, 1280, 300}
	case ImageLogo:
		return []int{1280, 500}
	case ImageProfile:
		// Cast/crew headshots are only ever rendered at card size; they do not
		// get the wide rung posters and stills carry.
		return []int{500, 300}
	default: // poster, still
		return []int{780, 500, 300}
	}
}

// VariantNames returns the cached variants generated for an artwork type.
func VariantNames(imageType string) []string {
	widths := VariantWidths(imageType)
	names := make([]string, 0, len(widths)+1)
	names = append(names, OriginalVariant)
	for _, width := range widths {
		names = append(names, "w"+strconv.Itoa(width))
	}
	return names
}

// ObjectKeys expands an original key to every expected key for its image type.
func ObjectKeys(originalPath, imageType string) []string {
	if originalPath == "" || strings.Contains(originalPath, "://") {
		return nil
	}
	names := VariantNames(imageType)
	keys := make([]string, 0, len(names))
	for _, name := range names {
		keys = append(keys, Variant(originalPath, name))
	}
	return keys
}
