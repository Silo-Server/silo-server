package jellycompat

import (
	"context"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

const compatCardImageSize = "small"

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
		if strings.EqualFold(imageType, "Backdrop") {
			return "medium"
		}
		return compatCardImageSize
	}

	maxWidth := parsePositiveInt(firstNonEmpty(r.URL.Query().Get("MaxWidth"), r.URL.Query().Get("maxWidth")), 0)
	maxHeight := parsePositiveInt(firstNonEmpty(r.URL.Query().Get("MaxHeight"), r.URL.Query().Get("maxHeight")), 0)
	fillWidth := parsePositiveInt(firstNonEmpty(r.URL.Query().Get("FillWidth"), r.URL.Query().Get("fillWidth")), 0)
	fillHeight := parsePositiveInt(firstNonEmpty(r.URL.Query().Get("FillHeight"), r.URL.Query().Get("fillHeight")), 0)
	maxDim := max(max(maxWidth, maxHeight), max(fillWidth, fillHeight))

	switch {
	case maxDim <= 0:
		if strings.EqualFold(imageType, "Backdrop") {
			return "medium"
		}
		return compatCardImageSize
	case maxDim <= 320:
		return compatCardImageSize
	case maxDim >= 1200:
		return "original"
	case maxDim >= 780:
		// The ladder now carries a rung between the pre-existing default and
		// the original (w780 posters and stills, w1280 logos), so a Jellyfin
		// client asking for a large-but-not-full image gets one instead of
		// being rounded down to the default.
		return "large"
	default:
		return "medium"
	}
}
