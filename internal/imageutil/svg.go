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

typedef struct {
	int width;
	int height;
} silo_size;

// Reports the size libvips draws an SVG at with scale, from its header alone,
// or 0x0 on failure with the reason in libvips' error buffer.
static silo_size silo_vips_svg_size(void *buf, size_t len, double scale) {
	silo_size size = {0, 0};
	VipsImage *image = NULL;
	if (vips_svgload_buffer(buf, len, &image, "scale", scale, NULL)) {
		return size;
	}
	size.width = vips_image_get_width(image);
	size.height = vips_image_get_height(image);
	g_object_unref(image);
	return size;
}

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

// rasterizeSVG renders an SVG with its longest edge at maxDim. A vector has
// no resolution of its own, and its declared canvas says nothing about the
// size it is drawn at: rendered at that canvas, a TMDB logo declaring 54x24
// became a 54px original, and every rung, which never upscales, was that same
// 54px image.
//
// Sizes come from libvips' own header at each scale rather than from the
// declared canvas, which bimg rounds to whole pixels: a fractional canvas
// scales to a raster of a different size than the rounded one, and the
// budget has to be charged for the raster libvips actually draws.
func rasterizeSVG(data []byte, maxDim int) ([]byte, error) {
	source := C.CBytes(data)
	defer C.free(source)
	length := C.size_t(len(data))
	sizeAt := func(scale float64) (bimg.ImageSize, error) {
		size := C.silo_vips_svg_size(source, length, C.double(scale))
		if size.width <= 0 || size.height <= 0 {
			return bimg.ImageSize{}, fmt.Errorf("%w: SVG: %s", ErrInvalidImage, takeVipsError())
		}
		return bimg.ImageSize{Width: int(size.width), Height: int(size.height)}, nil
	}

	// Scale the declared canvas to roughly maxDim, then correct the scale
	// with the size libvips reports for it.
	scale := 1.0
	for range 2 {
		size, err := sizeAt(scale)
		if err != nil {
			return nil, err
		}
		scale *= float64(maxDim) / float64(max(size.Width, size.Height))
	}
	rendered, err := sizeAt(scale)
	if err != nil {
		return nil, err
	}
	// A square or tall SVG at the full size can be more than a node with
	// little memory accepts at all, so it is drawn as large as the node
	// allows instead; the margin covers libvips rounding up.
	budget := decodes()
	limit := min(int64(maxSourcePixels), budget.pixels)
	if pixels := int64(rendered.Width) * int64(rendered.Height); pixels > limit {
		scale *= math.Sqrt(float64(limit)/float64(pixels)) * 0.99
		if rendered, err = sizeAt(scale); err != nil {
			return nil, err
		}
	}
	release, err := budget.reserve(rendered)
	if err != nil {
		return nil, err
	}
	defer release()

	out := C.silo_vips_svg_to_png(source, length, C.double(scale))
	if out.data == nil {
		return nil, fmt.Errorf("imageutil: rasterize SVG: %s", takeVipsError())
	}
	defer C.g_free(C.gpointer(out.data))
	return C.GoBytes(out.data, C.int(out.len)), nil
}

// takeVipsError returns and clears libvips' error buffer.
func takeVipsError() string {
	message := C.silo_vips_take_error()
	defer C.g_free(C.gpointer(message))
	return C.GoString(message)
}
