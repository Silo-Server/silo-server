package jellycompat

import (
	"context"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
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
	ladderType := artworkkey.ImageTypeOf(path, imageType)
	return detailSvc.PresignImageURLWithExpiry(ctx, path, imageType, compatSizeFor(size, ladderType))
}

// compatRungTolerance is how much narrower than a request a rung may be and
// still serve it. Scaling a rung up by a quarter costs little sharpness, while
// the next rung is often twice as wide (w500 to w1000 posters), so a modest
// shortfall beats doubling the bytes.
const compatRungTolerance = 0.8

// compatRequestImageSize maps Jellyfin's MaxWidth/MaxHeight/FillWidth/FillHeight
// onto the native image sizes: the narrowest rung of the image type's ladder
// that covers the requested box, within compatRungTolerance, or the original
// when even the widest rung falls short. The ladder is keyed by width, so a
// height becomes one through the type's aspect ratio (see compatTargetWidth).
//
// Which image serves a request is known only once the item resolves: Primary
// is a poster, an episode still or a headshot, and an item without the
// requested type can fall back to another (a series backdrop for a missing
// episode still). Ladders and aspect ratios differ, so the value carries the
// requested type's size and, after it, each ladder whose size differs, e.g.
// "large;profile=original"; compatSizeFor picks the resolved image's own when
// it is presigned. The value doubles as the route cache's size key, so two
// requests share a cached URL only when they agree on every ladder.
func compatRequestImageSize(r *http.Request, imageType string) string {
	defaultSize := compatCardImageSize
	if strings.EqualFold(imageType, compatBackdropImageType) {
		defaultSize = compatMediumImageSize
	}
	if r == nil {
		return defaultSize
	}

	query := r.URL.Query()
	param := func(name, alias string) int {
		return parsePositiveInt(firstNonEmpty(query.Get(name), query.Get(alias)), 0)
	}
	box := compatImageBox{
		maxWidth: param("MaxWidth", "maxWidth"), maxHeight: param("MaxHeight", "maxHeight"),
		fillWidth: param("FillWidth", "fillWidth"), fillHeight: param("FillHeight", "fillHeight"),
	}
	if box == (compatImageBox{}) {
		return defaultSize
	}
	own := compatLadderType(imageType)
	ownSize := compatSizeOnLadder(own, box)
	parts := []string{ownSize}
	for _, ladder := range compatLadders {
		// The requested type's own ladder never differs, so it is skipped here.
		if size := compatSizeOnLadder(ladder, box); size != ownSize {
			parts = append(parts, ladder+"="+size)
		}
	}
	return strings.Join(parts, compatSizeSeparator)
}

// compatLadders are the artwork ladders a Jellyfin image request can resolve
// on, in a fixed order so equal requests produce equal cache keys.
var compatLadders = []string{
	artworkkey.ImagePoster, artworkkey.ImageStill, artworkkey.ImageProfile,
	artworkkey.ImageBackdrop, artworkkey.ImageLogo, artworkkey.ImageThumb,
	artworkkey.ImageBanner, artworkkey.ImageClearArt, artworkkey.ImageDisc,
}

// compatSizeSeparator separates a request's own size from the per-ladder
// sizes that differ from it.
const compatSizeSeparator = ";"

// compatImageBox is a Jellyfin image request's size parameters; zero is absent.
type compatImageBox struct {
	maxWidth, maxHeight, fillWidth, fillHeight int
}

// compatTargetWidth is the width an image of the given aspect ratio is drawn at
// for box. The Max parameters bound the image, so with both it fits within
// each and the smaller implied width wins. The Fill parameters are a box the
// image covers, so the larger wins. Either kind alone is that one constraint.
func compatTargetWidth(box compatImageBox, aspect float64) int {
	implied := func(height int) int { return int(math.Ceil(float64(height) * aspect)) }
	fit := box.maxWidth
	if box.maxHeight > 0 && (fit == 0 || implied(box.maxHeight) < fit) {
		fit = implied(box.maxHeight)
	}
	// slices.Max rather than max: this package shadows the builtin with a
	// first-positive helper.
	return slices.Max([]int{fit, box.fillWidth, implied(box.fillHeight)})
}

// compatSizeOnLadder is the narrowest of small, medium and large on ladderType
// that is at least compatRungTolerance of the width box asks for, else the
// original.
func compatSizeOnLadder(ladderType string, box compatImageBox) string {
	width := compatTargetWidth(box, compatImageAspect(ladderType))
	for _, size := range []imagesize.Size{imagesize.Small, imagesize.Medium, imagesize.Large} {
		rung, ok := imagesize.VariantWidthPx(imagesize.Variant(ladderType, size))
		if ok && float64(rung) >= compatRungTolerance*float64(width) {
			return string(size)
		}
	}
	return compatOriginalImageSize
}

// compatSizeFor is the size to presign an image on ladderType at, from a value
// compatRequestImageSize produced: that ladder's size when it differs from the
// requested type's, else the requested type's.
func compatSizeFor(size, ladderType string) string {
	own, others, ok := strings.Cut(size, compatSizeSeparator)
	if !ok {
		return size
	}
	for part := range strings.SplitSeq(others, compatSizeSeparator) {
		if ladder, ladderSize, _ := strings.Cut(part, "="); ladder == ladderType {
			return ladderSize
		}
	}
	return own
}

// compatLadderType is the artwork ladder a Jellyfin image type is cached on.
// Primary maps to the poster ladder; compatRequestImageSize also sizes every
// request on the other ladders.
func compatLadderType(imageType string) string {
	switch lower := strings.ToLower(imageType); lower {
	case artworkkey.ImageBackdrop, artworkkey.ImageLogo, artworkkey.ImageThumb,
		artworkkey.ImageBanner, artworkkey.ImageDisc:
		// Jellyfin names these the same as the cache does.
		return lower
	case "art":
		return artworkkey.ImageClearArt
	default:
		return artworkkey.ImagePoster
	}
}

// compatImageAspect is a ladder type's typical width over height.
func compatImageAspect(ladderType string) float64 {
	switch ladderType {
	case artworkkey.ImagePoster, artworkkey.ImageProfile:
		return 2.0 / 3.0
	case artworkkey.ImageBanner:
		return 1000.0 / 185.0
	case artworkkey.ImageDisc:
		return 1
	default: // backdrop, still, thumb, clear art, and logos, which are wider still
		return 16.0 / 9.0
	}
}
