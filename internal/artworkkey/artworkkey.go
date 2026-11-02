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

// LocalPrefix starts the key of every cached copy of artwork Silo read from
// the library itself (sidecar images, embedded covers) rather than from a
// metadata provider.
const LocalPrefix = "local/"

// NFOPrefix starts the key of a cached copy of an image URL a library's NFO
// file names. The NFO provider's copies are keyed like a provider's, under its
// slug, but the library chose them, so they count as library artwork.
const NFOPrefix = "nfo/"

// IsLocal reports whether objectPath is a cached copy of library artwork: a
// file Silo read from the library or an image URL a library NFO names.
func IsLocal(objectPath string) bool {
	return strings.HasPrefix(objectPath, LocalPrefix) || strings.HasPrefix(objectPath, NFOPrefix)
}

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
	ImageBanner   = "banner"
	ImageThumb    = "thumb"
	ImageClearArt = "clearart"
	ImageDisc     = "disc"
)

// ImageTypeOf returns the image type a cached key was generated under: its
// image-type directory segment (".../{imageType}/{variant}.{ext}") when that
// names one of the types above, else fallback. The segment is authoritative
// because the cacher generates the ladder for the same type it writes there,
// so a slot that carries another type's art (an episode still in a backdrop or
// poster slot) still resolves to rungs that exist.
func ImageTypeOf(objectPath, fallback string) string {
	dir := Directory(objectPath)
	if dir == "" {
		return fallback
	}
	switch segment := path.Base(dir); segment {
	case ImagePoster, ImageBackdrop, ImageStill, ImageLogo, ImageProfile,
		ImageBanner, ImageThumb, ImageClearArt, ImageDisc:
		return segment
	default:
		return fallback
	}
}

// LadderVersion identifies the current shape of the variant ladder returned by
// VariantWidths. It MUST be bumped whenever an existing type's widths change so
// the one-shot ladder backfill re-enqueues already-cached artwork and generates
// the newly-added rungs. Adding a type does not need a bump. Existing deployments compare their recorded
// backfilled_version against this value; leaving it stale means clients asking
// for a new rung keep falling back to the next lower one forever.
const LadderVersion = 3

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

// OriginalOf returns the original object of the revision a variant key
// belongs to, for example ".../poster/original.r1.webp" for
// ".../poster/w500.r1.webp". Repair regenerates variants from that original.
// A key without a directory or a revision is returned unchanged.
func OriginalOf(objectPath string) string {
	dir := Directory(objectPath)
	if dir == "" {
		return objectPath
	}
	base := path.Base(objectPath)
	ext := path.Ext(base)
	stem := base[:len(base)-len(ext)]
	dot := strings.IndexByte(stem, '.')
	if dot < 0 {
		return objectPath
	}
	return dir + OriginalVariant + stem[dot:] + ext
}

// VariantWidths returns the resize widths generated for an artwork type,
// ordered widest first. This is the single source of truth for the variant
// ladder: image generation, object-key expansion, garbage collection, and the
// client-selectable image sizes in internal/imagesize all derive from it.
//
// Bump LadderVersion whenever an existing type's widths change. A new type
// needs no bump: nothing cached under it predates its ladder.
func VariantWidths(imageType string) []int {
	switch strings.ToLower(strings.TrimSpace(imageType)) {
	case ImageBackdrop:
		// w2560 serves 1440p and high-density laptop and tablet heroes without
		// sending the 4K original.
		return []int{2560, 1920, 1280, 300}
	case ImageStill:
		// Episode stills are 16:9 like backdrops; w1280 is a TV-sized hero.
		return []int{1280, 500, 300}
	case ImageThumb:
		// Landscape thumbs are 16:9 cards, shown smaller than backdrops.
		return []int{1280, 500, 300}
	case ImageLogo:
		return []int{1280, 500}
	case ImageBanner, ImageClearArt:
		// Kodi banners (1000x185) and clear art (1000x562) top out at 1000.
		return []int{1000, 500}
	case ImageDisc:
		return []int{1000, 500, 300}
	case ImageProfile:
		// Cast/crew headshots are only ever rendered at card size; they do not
		// get the wide rung posters and stills carry.
		return []int{500, 300}
	default: // poster
		return []int{1000, 500, 300}
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
