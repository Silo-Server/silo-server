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

// VariantWidths returns the resize widths generated for an artwork type. This
// is the single source of truth for the variant ladder: image generation,
// object-key expansion, and garbage collection all derive from it.
func VariantWidths(imageType string) []int {
	switch strings.ToLower(strings.TrimSpace(imageType)) {
	case "backdrop":
		return []int{1920, 1280, 300}
	case "logo":
		return []int{500}
	default: // poster, still, profile
		return []int{500, 300}
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
