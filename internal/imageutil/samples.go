package imageutil

/*
#cgo pkg-config: vips
#include <vips/vips.h>

enum {
	SILO_LOAD_JPEG,
	SILO_LOAD_PNG,
	SILO_LOAD_WEBP,
	SILO_LOAD_TIFF,
	SILO_LOAD_GIF,
	SILO_LOAD_SVG,
	SILO_LOAD_HEIF
};

typedef struct {
	int width;
	int height;
	int bands;
	int sample_bytes;
	int multiscan;
	char error[256];
} SiloHeader;

// silo_vips_header opens buf with one loader and reports what the image that
// loader decodes will be: its dimensions, band count, the bytes one sample
// takes, and whether it is a JPEG stored in several scans. Opening reads the
// header only.
static int silo_vips_header(const unsigned char *data, size_t len, int loader, SiloHeader *out) {
	void *buf = (void *) data;
	VipsImage *image = NULL;
	int failed = -1;

	switch (loader) {
	case SILO_LOAD_JPEG:
		failed = vips_jpegload_buffer(buf, len, &image, NULL);
		break;
	case SILO_LOAD_PNG:
		failed = vips_pngload_buffer(buf, len, &image, NULL);
		break;
	case SILO_LOAD_WEBP:
		failed = vips_webpload_buffer(buf, len, &image, NULL);
		break;
	case SILO_LOAD_TIFF:
		failed = vips_tiffload_buffer(buf, len, &image, NULL);
		break;
	case SILO_LOAD_GIF:
		failed = vips_gifload_buffer(buf, len, &image, NULL);
		break;
	case SILO_LOAD_SVG:
		failed = vips_svgload_buffer(buf, len, &image, NULL);
		break;
	case SILO_LOAD_HEIF:
		failed = vips_heifload_buffer(buf, len, &image, NULL);
		break;
	}
	if (failed) {
		g_strlcpy(out->error, vips_error_buffer(), sizeof(out->error));
		vips_error_clear();
	} else {
		out->width = vips_image_get_width(image);
		out->height = vips_image_get_height(image);
		out->bands = vips_image_get_bands(image);
		out->sample_bytes = (int) vips_format_sizeof(vips_image_get_format(image));
		out->multiscan = 0;
		if (vips_image_get_typeof(image, "jpeg-multiscan") &&
			vips_image_get_int(image, "jpeg-multiscan", &out->multiscan)) {
			out->multiscan = 0;
			vips_error_clear();
		}
		g_object_unref(image);
	}
	vips_thread_shutdown();
	return failed;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"strings"

	"github.com/h2non/bimg"
)

// sourceHeader is what an image's header declares about its decoded form.
type sourceHeader struct {
	width, height int
	bands         int
	// sampleBytes is the size of one sample of a band once decoded: 1 for
	// the usual 8-bit image, 2 for a 16-bit one, 4 or more for floating
	// point.
	sampleBytes int
	// progressive marks a JPEG stored in several scans.
	progressive bool
}

// readSourceHeader reads data's header with the loader bimg uses for kind
// (see decodableType), and so describes the image bimg decodes. It is the
// package's one read of a source's header: bimg reports the dimensions and
// band count but not the sample size or the scan layout.
func readSourceHeader(data []byte, kind bimg.ImageType) (sourceHeader, error) {
	var loader C.int
	switch kind {
	case bimg.JPEG:
		loader = C.SILO_LOAD_JPEG
	case bimg.PNG:
		loader = C.SILO_LOAD_PNG
	case bimg.WEBP:
		loader = C.SILO_LOAD_WEBP
	case bimg.TIFF:
		loader = C.SILO_LOAD_TIFF
	case bimg.GIF:
		loader = C.SILO_LOAD_GIF
	case bimg.SVG:
		loader = C.SILO_LOAD_SVG
	case bimg.HEIF, bimg.AVIF:
		loader = C.SILO_LOAD_HEIF
	default:
		return sourceHeader{}, errors.New("unsupported image format")
	}
	var header C.SiloHeader
	if C.silo_vips_header((*C.uchar)(&data[0]), C.size_t(len(data)), loader, &header) != 0 {
		return sourceHeader{}, fmt.Errorf("unreadable image header: %s", strings.TrimSpace(C.GoString(&header.error[0])))
	}
	return sourceHeader{
		width:       int(header.width),
		height:      int(header.height),
		bands:       int(header.bands),
		sampleBytes: int(header.sample_bytes),
		progressive: header.multiscan != 0,
	}, nil
}
