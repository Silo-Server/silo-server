package jellycompat

import (
	"context"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
)

// The Jellyfin surface expresses a requested size with the same vocabulary the
// native API uses, because both end up in DetailService.PresignImageURL.
const (
	compatCardImageSize     = string(imagesize.Small)
	compatMediumImageSize   = string(imagesize.Medium)
	compatLargeImageSize    = string(imagesize.Large)
	compatOriginalImageSize = string(imagesize.Original)
)

// compatBackdropImageType is Jellyfin's name for a backdrop, which is the one
// image type this layer defaults to a hero size rather than a card size.
const compatBackdropImageType = "Backdrop"

func compatPresignImage(detailSvc *catalog.DetailService, ctx context.Context, path, imageType, size string) string {
	return compatPresignImageWithExpiry(detailSvc, ctx, path, imageType, size).URL
}

func compatPresignImageWithExpiry(detailSvc *catalog.DetailService, ctx context.Context, path, imageType, size string) catalog.ResolvedImageURL {
	if detailSvc == nil {
		return catalog.ResolvedImageURL{URL: path}
	}
	return detailSvc.PresignImageURLWithExpiry(ctx, path, imageType, size)
}

func compatRequestImageSize(r *http.Request, imageType string) string {
	if r == nil {
		if strings.EqualFold(imageType, compatBackdropImageType) {
			return compatMediumImageSize
		}
		return compatCardImageSize
	}

	maxWidth := parsePositiveInt(firstNonEmpty(r.URL.Query().Get("MaxWidth"), r.URL.Query().Get("maxWidth")), 0)
	maxHeight := parsePositiveInt(firstNonEmpty(r.URL.Query().Get("MaxHeight"), r.URL.Query().Get("maxHeight")), 0)
	fillWidth := parsePositiveInt(firstNonEmpty(r.URL.Query().Get("FillWidth"), r.URL.Query().Get("fillWidth")), 0)
	fillHeight := parsePositiveInt(firstNonEmpty(r.URL.Query().Get("FillHeight"), r.URL.Query().Get("fillHeight")), 0)
	maxDim := max(max(maxWidth, maxHeight), max(fillWidth, fillHeight))
	// The large bucket names a width rung (w780 posters and stills), so only a
	// width-ish constraint may promote into it: a portrait poster asked for at
	// MaxHeight=900 would come back far taller than 900. The original bucket
	// above deliberately keeps reading maxDim — it serves the stored original
	// rather than a rung, so a height-only request there is merely generous.
	maxWidthDim := max(maxWidth, fillWidth)

	switch {
	case maxDim <= 0:
		if strings.EqualFold(imageType, compatBackdropImageType) {
			return compatMediumImageSize
		}
		return compatCardImageSize
	case maxDim <= 320:
		return compatCardImageSize
	case maxDim >= 1200:
		return compatOriginalImageSize
	case maxWidthDim >= 780:
		// The ladder now carries a rung between the pre-existing default and
		// the original (w780 posters and stills, w1280 logos), so a Jellyfin
		// client asking for a large-but-not-full image gets one instead of
		// being rounded down to the default.
		return compatLargeImageSize
	default:
		return compatMediumImageSize
	}
}
