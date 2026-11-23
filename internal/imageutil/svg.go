//go:build !silo_node

package imageutil //nolint:gocritic // see import "C" below

/*
#cgo pkg-config: vips
#include <stdlib.h>
#include <vips/vips.h>

typedef struct {
	void *data;
	size_t len;
} silo_buffer;

// Renders an SVG at scale times its declared size and encodes it as a PNG.
// bimg loads an SVG only at its declared size, which is not a pixel size
// for a vector: TMDB logos declare canvases a few dozen units wide. Returns
// no data on failure, with the reason in libvips' error buffer.
static silo_buffer silo_vips_svg_to_png(void *buf, size_t len, double scale) {
	silo_buffer out = {NULL, 0};
	VipsImage *image = NULL;
	if (vips_svgload_buffer(buf, len, &image, "scale", scale, "access", VIPS_ACCESS_SEQUENTIAL, NULL)) {
		return out;
	}
	if (vips_pngsave_buffer(image, &out.data, &out.len, NULL)) {
		out.data = NULL;
		out.len = 0;
	}
	g_object_unref(image);
	return out;
}

static char *silo_vips_take_error(void) {
	char *message = g_strdup(vips_error_buffer());
	vips_error_clear();
	return message;
}
*/
import "C" //nolint:gocritic // cgo imports unsafe under its own alias for CBytes and GoBytes

import (
	"fmt"
	"math"

	"github.com/h2non/bimg"
)

// rasterizeSVG renders an SVG so its longest edge is
// MaxCachedOriginalDimension, the widest original the cache keeps. A vector
// has no resolution of its own, and its declared canvas says nothing about
// the size it is drawn at: rendered at that canvas, a TMDB logo declaring
// 54x24 became a 54px original, and every rung, which never upscales, was
// that same 54px image.
func rasterizeSVG(data []byte) ([]byte, error) {
	declared, err := bimg.NewImage(data).Size()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidImage, err)
	}
	longest := max(declared.Width, declared.Height)
	if longest <= 0 {
		return nil, fmt.Errorf("%w: SVG declares no size", ErrInvalidImage)
	}
	scale := float64(MaxCachedOriginalDimension) / float64(longest)
	renderedSize := func() bimg.ImageSize {
		return bimg.ImageSize{
			Width:  max(1, int(math.Ceil(float64(declared.Width)*scale))),
			Height: max(1, int(math.Ceil(float64(declared.Height)*scale))),
		}
	}
	// Charge the budget for the raster this renders, not the declared canvas.
	// A square or tall SVG at the full width can be more than a node with
	// little memory accepts at all, so it is drawn as large as the node
	// allows instead; the margin covers rounding up.
	rendered := renderedSize()
	budget := decodes()
	limit := min(int64(maxSourcePixels), budget.pixels)
	if pixels := int64(rendered.Width) * int64(rendered.Height); pixels > limit {
		scale *= math.Sqrt(float64(limit)/float64(pixels)) * 0.99
		rendered = renderedSize()
	}
	release, err := budget.reserve(rendered)
	if err != nil {
		return nil, err
	}
	defer release()

	source := C.CBytes(data)
	defer C.free(source)
	out := C.silo_vips_svg_to_png(source, C.size_t(len(data)), C.double(scale))
	if out.data == nil {
		message := C.silo_vips_take_error()
		defer C.g_free(C.gpointer(message))
		return nil, fmt.Errorf("imageutil: rasterize SVG: %s", C.GoString(message))
	}
	defer C.g_free(C.gpointer(out.data))
	return C.GoBytes(out.data, C.int(out.len)), nil
}
