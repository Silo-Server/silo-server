// Package artworkkey owns the object-key naming contract for cached artwork:
// the variant ladder, the key grammars, and the portable content-addressed
// storage format (see portable.go and manifest.go).
//
// Two grammars coexist, and every read path in this package serves both:
//
//   - portable (all new writes):
//     artwork/v1/objects/{image-type}/{revision[0:2]}/{revision}/{variant}.{ext}
//   - legacy provider-oriented:
//     {provider}/{type}/{id}/.../{image-type}/{variant}.{revision}.{ext},
//     plus pre-revision names such as {…}/{image-type}/original.webp
//
// Legacy keys are never rewritten in place. They stay readable and
// garbage-collectable for as long as a catalog row points at one; art converts
// to the portable layout the next time it is materialized.
package artworkkey

import (
	"path"
	"strconv"
	"strings"
)

const (
	OriginalVariant = "original"
	VariantW300     = "w300"
	VariantW500     = "w500"
	VariantW780     = "w780"
	VariantW1280    = "w1280"
	VariantW1920    = "w1920"
)

// LadderVersion identifies the generated variant set. It changes only when
// VariantWidths changes, so readers can distinguish revisions produced before
// a ladder extension without treating their intentionally smaller manifests as
// damaged. No request path uses it as a reason to enqueue repair work.
const LadderVersion = 2

// defaultVariantExt is the extension assumed when a caller builds a key
// without naming one. Every encode the pipeline performs today outputs WebP.
const defaultVariantExt = ".webp"

// Build returns an object key for a variant under basePath.
func Build(basePath, variant, revision, ext string) string {
	basePath = strings.TrimRight(strings.TrimSpace(basePath), "/")
	variant = strings.TrimSpace(variant)
	revision = strings.TrimSpace(revision)
	if basePath == "" || variant == "" {
		return ""
	}
	if ext == "" {
		ext = defaultVariantExt
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
//
// It serves both grammars: a portable key's filename is "original.{ext}" and a
// legacy key's is "original.{revision}.{ext}", so replacing the leading
// "original" segment is correct for either.
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

// Directory returns the prefix containing every object that belongs with an
// artwork key, including a trailing slash.
//
// For a portable key that is the revision directory, which holds exactly one
// revision. For a legacy key it is the image-type prefix, which holds every
// revision ever written for that one image.
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

// Revision extracts the content revision from a revisioned key. Portable keys
// carry it as their directory; legacy revisioned keys carry it in the filename.
// Pre-revision legacy keys return an empty string.
func Revision(objectPath string) string {
	if info, ok := ParsePortableKey(objectPath); ok {
		return info.Revision
	}
	name := path.Base(strings.TrimSpace(objectPath))
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	firstDot := strings.IndexByte(stem, '.')
	if firstDot < 0 || firstDot == len(stem)-1 {
		return ""
	}
	return stem[firstDot+1:]
}

// Artwork types the pipeline produces a variant ladder for. They are the
// normalized values that appear in a portable key.
const (
	ImageTypePoster   = "poster"
	ImageTypeBackdrop = "backdrop"
	ImageTypeLogo     = "logo"
	ImageTypeStill    = "still"
	ImageTypeProfile  = "profile"
)

// ImageTypes returns every catalog artwork type in a stable order. It exists so
// a caller describing the whole ladder — the artwork capability, for one —
// cannot drift from the ladder this package actually generates. Upload image
// types are reported separately by UploadImageTypes.
func ImageTypes() []string {
	return []string{ImageTypePoster, ImageTypeBackdrop, ImageTypeLogo, ImageTypeStill, ImageTypeProfile}
}

// IsStoredArtworkKey distinguishes artwork objects from unrelated objects in
// the shared public bucket. It recognizes the portable tree, adoption hints,
// the legacy upload namespaces, and provider/local legacy ladders whose parent
// directory is one of the fixed image types.
func IsStoredArtworkKey(key string) bool {
	key = strings.TrimSpace(key)
	if _, ok := ParsePortableKey(key); ok || IsAdoptionIndexKey(key) || IsBrandingKey(key) {
		return true
	}
	if IsLegacyUploadKey(key) {
		return true
	}
	dir, filename := path.Dir(key), path.Base(key)
	if dir == "." || filename == "." || filename == "" {
		return false
	}
	imageType := path.Base(dir)
	if !isKnownImageType(imageType) {
		return false
	}
	stem := strings.TrimSuffix(filename, path.Ext(filename))
	variant := strings.SplitN(stem, ".", 2)[0]
	if variant == OriginalVariant {
		return true
	}
	if !strings.HasPrefix(variant, "w") {
		return false
	}
	_, err := strconv.Atoi(strings.TrimPrefix(variant, "w"))
	return err == nil
}

func isKnownImageType(imageType string) bool {
	for _, candidate := range append(ImageTypes(), UploadImageTypes()...) {
		if imageType == candidate {
			return true
		}
	}
	return false
}

// VariantWidths returns the resize widths generated for an artwork type. This
// is the single source of truth for the variant ladder: image generation,
// object-key expansion, and garbage collection all derive from it.
//
// Getting an upload type wrong here is not cosmetic: garbage collection expands
// a trigger-queued revision through VariantNames, so a ladder narrower than what
// was actually written would leave orphans behind.
func VariantWidths(imageType string) []int {
	switch strings.ToLower(strings.TrimSpace(imageType)) {
	case ImageTypeBackdrop:
		return []int{1920, 1280, 300}
	case ImageTypeLogo:
		return []int{1280, 500}
	case ImageTypePoster, ImageTypeStill:
		return []int{780, 500, 300}
	case ImageTypeProfile:
		return []int{500, 300}
	case ImageTypeCollectionBackdrop:
		// Narrower than a catalog backdrop: collection backdrops are shown in
		// headers and cards, never as a full-viewport hero.
		return []int{1280, 300}
	case ImageTypeAvatar:
		// Square, and only ever rendered at avatar size.
		return []int{256}
	default: // library-poster, collection-poster, and defensive unknowns
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

// IsLadderExtensionVariant reports whether variant is the newest wide rung for
// an artwork type whose ladder grew at LadderVersion. Older portable manifests
// may legitimately omit only these rungs; their absence is a compatibility
// fallback condition, not evidence of storage loss.
func IsLadderExtensionVariant(imageType, variant string) bool {
	switch strings.ToLower(strings.TrimSpace(imageType)) {
	case ImageTypePoster, ImageTypeStill, ImageTypeLogo:
		widths := VariantWidths(imageType)
		return len(widths) > 0 && variant == "w"+strconv.Itoa(widths[0])
	default:
		return false
	}
}

// ObjectKeys expands an original key to every expected key for its image type.
//
// Garbage collection uses this for candidates queued by the displacement
// triggers, which carry no stored manifest. A portable key expands to its
// ladder plus manifest.json, so removing a revision leaves no orphan
// completeness marker behind, and the type recorded in the key wins over the
// caller's: it was fixed when the revision was addressed, while the caller's
// comes from whichever column displaced the row.
func ObjectKeys(originalPath, imageType string) []string {
	if originalPath == "" || strings.Contains(originalPath, "://") {
		return nil
	}
	if info, ok := ParsePortableKey(originalPath); ok {
		if info.IsManifest {
			// The manifest is never a publishable target, and its .json
			// extension would expand to variant keys that do not exist.
			return nil
		}
		names := VariantNames(info.ImageType)
		keys := make([]string, 0, len(names)+1)
		keys = append(keys, info.Directory+"/"+ManifestName)
		for _, name := range names {
			keys = append(keys, info.Directory+"/"+name+info.Ext)
		}
		return keys
	}
	names := VariantNames(imageType)
	keys := make([]string, 0, len(names))
	for _, name := range names {
		keys = append(keys, Variant(originalPath, name))
	}
	return keys
}
