package handlers

import (
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
)

// writeInvalidImageSize answers a request whose image_size is not one this
// server serves. A typo is a client bug; quietly serving the default size would
// hide it behind artwork that is merely the wrong resolution.
func writeInvalidImageSize(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_image_size",
		"image_size must be one of small, medium, large, original")
}

// rejectInvalidImageSize validates the request's image_size and reports whether
// the handler may continue. It is for entrypoints that do not go through
// accessFilterOrError, which validates the parameter for every catalog read.
func rejectInvalidImageSize(w http.ResponseWriter, r *http.Request) bool {
	if _, err := imagesize.FromRequest(r); err != nil {
		writeInvalidImageSize(w)
		return false
	}
	return true
}

// requestImageSize reads the artwork size the client asked for, treating an
// unrecognized value as no preference.
//
// The authoritative parse — the one that answers 400 — happens once per request,
// in accessFilterOrError or rejectInvalidImageSize. This is for the places that
// turn a request into the per-request context everything else reads: the access
// filter, and the single parse at the top of a section response. Response
// builders take the resolved size as an argument rather than calling this, so
// the value cannot diverge from the one that was validated.
func requestImageSize(r *http.Request) imagesize.Size {
	size, err := imagesize.FromRequest(r)
	if err != nil {
		return imagesize.Unset
	}
	return size
}

// requestVariantHint returns the semantic variant hint to send to a plugin
// resolver alongside a path resolved at the request's size. Without an explicit
// size the caller's per-context hint stands.
func requestVariantHint(defaultHint string, size imagesize.Size) string {
	if size == imagesize.Unset {
		return defaultHint
	}
	return imagesize.PluginVariant(size)
}

// sizedImagePath rewrites a cached image path to the variant the client asked
// for. Without an explicit size it returns fallback — the per-context default
// the server has always used, which differs between card and hero surfaces.
// An explicit size wins uniformly instead, so every image in one response is
// the same size. Full URLs and plugin-prefixed paths pass through: their
// variant is chosen at resolution time.
func sizedImagePath(path, imageType string, size imagesize.Size, fallback string) string {
	if size == imagesize.Unset {
		return fallback
	}
	if strings.Contains(path, "://") {
		return path
	}
	return strings.Replace(path, "/original.", "/"+imagesize.Variant(imageType, size)+".", 1)
}

// sizedCardPath is cardThumbnailPath with the request's size applied.
func sizedCardPath(path, imageType string, size imagesize.Size) string {
	return sizedImagePath(path, imageType, size, cardThumbnailPath(path))
}

// sizedCardBackdropPath resolves an image sitting in a card's backdrop slot.
//
// That slot does not always hold a real backdrop: an episode row puts its still
// there, and a still rides the still ladder, which has no w1920. Asking for the
// backdrop ladder would name a key that was never generated, so the type is read
// back off the key instead of assumed.
func sizedCardBackdropPath(path string, size imagesize.Size) string {
	return sizedCardPath(path, imageTypeForBackdropPath(path), size)
}

// sizedPosterPath is featuredPosterPath with the request's size applied.
func sizedPosterPath(path string, size imagesize.Size) string {
	return sizedImagePath(path, "poster", size, featuredPosterPath(path))
}

// sizedBackdropPath is featuredBackdropPath with the request's size applied.
func sizedBackdropPath(path string, size imagesize.Size) string {
	return sizedImagePath(path, imageTypeForBackdropPath(path), size, featuredBackdropPath(path))
}

// imageTypeForBackdropPath reports which ladder governs a path used in a
// backdrop slot. Episode "backdrops" are frequently the episode still, which
// rides the still ladder — asking for a backdrop width there would build a key
// that was never generated.
func imageTypeForBackdropPath(path string) string {
	if imageType := catalog.ImageTypeFromCachedPath(path); imageType != "" {
		return imageType
	}
	return artworkkey.ImageBackdrop
}
