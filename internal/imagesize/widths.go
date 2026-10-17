package imagesize

import (
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
)

// Slot is a family of response artwork fields a client can size on its own,
// independently of the size the rest of the response uses. A slot is named
// for the field, not the image in it: an episode's backdrop_url often carries
// its still, and the backdrop width still governs it.
type Slot uint8

const (
	// SlotPoster is every poster_url.
	SlotPoster Slot = iota
	// SlotBackdrop is every backdrop_url and backdrop_urls entry.
	SlotBackdrop
	// SlotLogo is every logo_url.
	SlotLogo
	// SlotStill is an episode row's still_url.
	SlotStill
	// SlotPhoto is a cast or crew member's photo_url.
	SlotPhoto
)

// Slots lists every slot, in the order the API documents them.
var Slots = []Slot{SlotPoster, SlotBackdrop, SlotLogo, SlotStill, SlotPhoto}

// Name is the slot's name in the API, the prefix of its width parameter
// (poster_width, ...).
func (s Slot) Name() string {
	switch s {
	case SlotPoster:
		return artworkkey.ImagePoster
	case SlotBackdrop:
		return artworkkey.ImageBackdrop
	case SlotLogo:
		return artworkkey.ImageLogo
	case SlotStill:
		return artworkkey.ImageStill
	case SlotPhoto:
		return "photo"
	default:
		return ""
	}
}

// MaxTargetWidth bounds a requested slot width. Wider than any display Silo
// serves; anything past the widest rung resolves to the original anyway.
const MaxTargetWidth = 7680

// Widths are per-slot target widths in device pixels: how wide the client
// draws the image. Zero means no preference for that slot.
type Widths struct {
	Poster   int
	Backdrop int
	Logo     int
	Still    int
	Photo    int
}

// IsZero reports whether no slot has a target width.
func (w Widths) IsZero() bool {
	return w == Widths{}
}

func (w Widths) of(slot Slot) int {
	switch slot {
	case SlotPoster:
		return w.Poster
	case SlotBackdrop:
		return w.Backdrop
	case SlotLogo:
		return w.Logo
	case SlotStill:
		return w.Still
	case SlotPhoto:
		return w.Photo
	default:
		return 0
	}
}

// Encode is a canonical, order-stable form of the widths for binding a
// pagination cursor to the artwork the first page was resolved with. It is
// empty when no slot has a width.
func (w Widths) Encode() string {
	var parts []string
	for _, slot := range Slots {
		if px := w.of(slot); px > 0 {
			parts = append(parts, slot.Name()+"="+strconv.Itoa(px))
		}
	}
	return strings.Join(parts, ",")
}

// Request is a client's artwork preference for one response: a size for every
// image, and per-slot target widths that override it for their slot.
type Request struct {
	Size   Size
	Widths Widths
}

// For returns the size to resolve a slot at: a target width when the client
// gave the slot one, else the response-wide size (possibly Unset, which leaves
// the caller's per-context default in place).
func (r Request) For(slot Slot) Size {
	if px := r.Widths.of(slot); px > 0 {
		return Width(px)
	}
	return r.Size
}

// widthPrefix marks a Size that is a target width rather than a named size.
const widthPrefix = "width:"

// Width is a size that names a target width in device pixels instead of a
// ladder position. It resolves to the narrowest rung of the image's own
// ladder at least that wide, or the original when every rung is narrower. It
// is server-internal: Parse never produces one, because clients send widths
// through the per-slot width parameters, not image_size.
func Width(px int) Size {
	return Size(widthPrefix + strconv.Itoa(px))
}

// TargetWidth reports the width a Width size names.
func (s Size) TargetWidth() (int, bool) {
	raw, ok := strings.CutPrefix(string(s), widthPrefix)
	if !ok {
		return 0, false
	}
	px, err := strconv.Atoi(raw)
	if err != nil || px <= 0 {
		return 0, false
	}
	return px, true
}

// variantForWidth is the narrowest rung of imageType's ladder at least px
// wide, or the original when every rung is narrower.
func variantForWidth(imageType string, px int) string {
	widths := artworkkey.VariantWidths(imageType)
	for i := len(widths) - 1; i >= 0; i-- {
		if widths[i] >= px {
			return widthVariant(widths[i])
		}
	}
	return artworkkey.OriginalVariant
}

// pluginVariantForWidth maps a target width onto the semantic hint plugin
// resolvers understand. Plugins host images at their own sizes, so this is an
// approximation by width class rather than a ladder lookup.
func pluginVariantForWidth(px int) string {
	switch {
	case px <= 300:
		return PluginVariantCard
	case px <= 500:
		return PluginVariantFeatured
	case px <= 1280:
		return PluginVariantLarge
	default:
		return PluginVariantOriginal
	}
}

// PhotoPluginVariant is the plugin hint for a cast or crew headshot at size.
// Without a size, and for any target width short of the original, it is the
// large hint cast lists have always used: a plugin's smaller headshots (TMDB
// serves 185px for both card and featured) are softer than a cast card on a
// high-density screen. A named size maps as it does for any image.
func PhotoPluginVariant(size Size) string {
	if size == Unset {
		return PluginVariantLarge
	}
	if px, ok := size.TargetWidth(); ok && pluginVariantForWidth(px) != PluginVariantOriginal {
		return PluginVariantLarge
	}
	return PluginVariant(size)
}
