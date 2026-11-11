package imageutil

/*
#cgo pkg-config: vips
#include <vips/vips.h>

static void silo_vips_block_untrusted(void) {
#if VIPS_MAJOR_VERSION > 8 || (VIPS_MAJOR_VERSION == 8 && VIPS_MINOR_VERSION >= 13)
	vips_block_untrusted_set(TRUE);
	vips_operation_block_set("VipsForeignLoadSvg", FALSE);
#endif
}
*/
import "C"

// Silo decodes artwork from metadata providers, media folders and uploads,
// and the format is sniffed from the bytes, not the file name. libvips marks
// the loaders that are not fuzzed for hostile input as untrusted (PDF,
// ImageMagick, OpenEXR, FITS, OpenSlide, JPEG 2000, JPEG XL, among others),
// and they are blocked for the whole process. Of these, bimg reaches PDF
// (Poppler) for any `%PDF` input, and, where libvips links ImageMagick 6,
// ImageMagick for anything only it recognizes (BMP, PSD, ICO and hundreds
// more); bimg does not recognize the ImageMagick 7 loader's name. Silo uses
// neither format for artwork. SVG is untrusted too but stays enabled:
// providers such as TMDB serve logos as SVG, and librsvg is written in Rust.
// Only Rasterize reads one; see checkFormat.
// JPEG, PNG, WebP, GIF, TIFF, HEIF and AVIF are trusted and unaffected by
// the block; checkFormat separately refuses TIFF, HEIF and AVIF as sources.
// bimg's package init has already started libvips by the time this runs.
// libvips older than 8.13 has no untrusted flag and keeps every loader.
func init() {
	C.silo_vips_block_untrusted()

	// libvips keeps finished operations so that a repeated one can be answered
	// from its cache, and bimg leaves that cache on. Silo decodes every source
	// from its own buffer, which never matches an earlier one, so the cache
	// only holds memory after the call that used it has returned. Keep
	// nothing.
	C.vips_cache_set_max(0)
}

// operationCacheSize is the number of finished operations libvips is holding.
// It is here for the tests, which cannot use cgo themselves.
func operationCacheSize() int {
	return int(C.vips_cache_get_size())
}
