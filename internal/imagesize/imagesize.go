// Package imagesize owns the client-selectable image size contract: the query
// parameter clients send, the sizes they may ask for, and the mapping from a
// size to a concrete cached artwork variant.
//
// It is a leaf package — it depends only on internal/artworkkey and the
// standard library — so handlers, the catalog detail service, jellycompat, and
// the metadata image resolver can all reach the same decisions without one of
// them importing another.
package imagesize

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
)

// QueryParam is the request parameter a client uses to pick an image size. It
// applies to the whole request: every image in the response is resolved at the
// requested size.
const QueryParam = "image_size"

// Size is a client-requested artwork size.
type Size string

const (
	// Unset means the client did not ask for a size, and the server keeps its
	// historical per-context defaults (card views get small artwork, hero
	// contexts get large artwork) rather than applying one size uniformly.
	Unset Size = ""
	// Small is the narrowest cached rung for the image type.
	Small Size = "small"
	// Medium is the size the server picked before image_size existed.
	Medium Size = "medium"
	// Large is the widest cached rung short of the original.
	Large Size = "large"
	// Original is the cached original, capped at
	// imageutil.MaxCachedOriginalDimension on ingest.
	Original Size = "original"
)

// All lists every size a client may request, narrowest first.
var All = []Size{Small, Medium, Large, Original}

// Parse converts a raw parameter value to a Size. An empty value is Unset and
// is not an error; any other unrecognized value is rejected so a client typo
// fails loudly instead of silently serving the default size.
func Parse(raw string) (Size, error) {
	switch Size(strings.ToLower(strings.TrimSpace(raw))) {
	case Unset:
		return Unset, nil
	case Small:
		return Small, nil
	case Medium:
		return Medium, nil
	case Large:
		return Large, nil
	case Original:
		return Original, nil
	default:
		return Unset, fmt.Errorf("imagesize: unknown %s %q (want small, medium, large, or original)", QueryParam, raw)
	}
}

// FromRequest reads the image size from a request's query string.
func FromRequest(r *http.Request) (Size, error) {
	if r == nil || r.URL == nil {
		return Unset, nil
	}
	return Parse(r.URL.Query().Get(QueryParam))
}

// Variant returns the cached artwork variant for an explicitly requested size.
//
// Small and Large are derived from artworkkey.VariantWidths so adding or
// removing a rung moves them automatically. Medium is a deliberate per-type
// literal: it is the variant the server chose before image_size existed, and it
// must keep resolving to the same key even if the ladder grows, so that an
// absent parameter and an explicit medium agree.
//
// size must not be Unset — the caller decides what "no preference" means. Unset
// is treated as Medium here so a missed check degrades to today's behavior
// rather than to an empty key.
func Variant(imageType string, size Size) string {
	widths := artworkkey.VariantWidths(imageType)
	switch size {
	case Original:
		return artworkkey.OriginalVariant
	case Small:
		if len(widths) == 0 {
			return artworkkey.OriginalVariant
		}
		return widthVariant(widths[len(widths)-1])
	case Large:
		if len(widths) == 0 {
			return artworkkey.OriginalVariant
		}
		return widthVariant(widths[0])
	default: // Medium, and Unset defensively.
		return mediumVariant(imageType)
	}
}

// Widths the pre-image_size server picked per image type. They are literals
// rather than ladder positions on purpose: an absent parameter and an explicit
// medium have to keep resolving to the same key even after the ladder grows.
// Changing one changes what every client that sends no parameter receives.
const (
	mediumBackdropWidth = 1920
	mediumDefaultWidth  = 500
)

// mediumVariant is the pre-image_size default for an image type.
func mediumVariant(imageType string) string {
	// poster, still, logo, profile, and anything unrecognized share the default.
	if strings.EqualFold(strings.TrimSpace(imageType), artworkkey.ImageBackdrop) {
		return widthVariant(mediumBackdropWidth)
	}
	return widthVariant(mediumDefaultWidth)
}

// Semantic variant hints understood by plugin image resolvers. This vocabulary
// is the plugin's, not the client's: it is deliberately separate from the sizes
// above even where a word coincides, because a plugin picks the closest image it
// happens to host rather than a pixel width.
//
// PluginVariantFull is part of the vocabulary but no client size maps onto it;
// it is named here so the set is readable in one place.
const (
	PluginVariantCard     = "card"
	PluginVariantFeatured = "featured"
	PluginVariantLarge    = "large"
	PluginVariantFull     = "full"
	PluginVariantOriginal = artworkkey.OriginalVariant
)

// PluginVariant maps a size to the semantic variant hint understood by plugin
// image resolvers. It only produces the subset a client size maps onto.
//
// "large" is sent unconditionally. The SDK's variant field is an open string
// and every first-party plugin falls back gracefully on a name it does not
// recognize (tmdb and metadb to the original, tvdb to full art), so a plugin
// built before this tier existed still returns a usable image rather than
// failing — which is what lets the vocabulary grow additively instead of being
// gated on a capability handshake.
func PluginVariant(size Size) string {
	switch size {
	case Small:
		return PluginVariantCard
	case Large:
		return PluginVariantLarge
	case Original:
		return PluginVariantOriginal
	default: // Medium and Unset defensively.
		return PluginVariantFeatured
	}
}

// NextLower returns the next narrower cached rung below variant for an image
// type, reporting false when variant is already the narrowest rung (or is not a
// width rung at all, such as the original).
//
// It exists so a resolver that finds a newly-added rung missing from S3 can walk
// down to one that was generated by an older ladder version.
func NextLower(imageType, variant string) (string, bool) {
	width, ok := VariantWidthPx(variant)
	if !ok {
		return "", false
	}
	widths := artworkkey.VariantWidths(imageType)
	for _, candidate := range widths {
		if candidate < width {
			return widthVariant(candidate), true
		}
	}
	return "", false
}

func widthVariant(width int) string {
	return "w" + strconv.Itoa(width)
}

// VariantWidthPx returns the pixel width a width rung names, e.g. "w780" -> 780.
// It reports false for anything that is not a width rung — the original, or a
// malformed name — because those have no fixed width.
func VariantWidthPx(variant string) (int, bool) {
	variant = strings.TrimSpace(variant)
	if !strings.HasPrefix(variant, "w") {
		return 0, false
	}
	width, err := strconv.Atoi(variant[1:])
	if err != nil || width <= 0 {
		return 0, false
	}
	return width, true
}
