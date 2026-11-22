// Package imageutil provides image resizing and thumbhash generation
// for collection poster and backdrop uploads.
package imageutil

import "errors"

// The declarations both builds share. The full build (imageutil.go and
// budget.go) and the transcode node build (imageutil_node.go) each define
// the functions.

// ErrBusy reports that a source waited maxDecodeWait for room in the decode
// budget and found none. The image may be fine; the caller can try again.
var ErrBusy = errors.New("imageutil: image processing is busy")

// ErrInvalidImage reports source bytes that libvips cannot read as an image.
// Failures after the header was read, such as encoding a variant, do not wrap
// it; see PixelDataUndecodable for damaged pixel data.
var ErrInvalidImage = errors.New("imageutil: invalid image")

// MaxCachedOriginalDimension caps the longest edge of a cached "original"
// variant at 4K. Provider artwork is rarely larger (TMDB backdrops top out at
// 3840 wide, posters at 2000x3000), so the original keeps the provider's full
// resolution; the cap bounds an outlier upload rather than everyday art.
const MaxCachedOriginalDimension = 3840

// Variant holds a named image variant (e.g. "original", "w500").
type Variant struct {
	Key  string
	Data []byte
}

// VariantResult contains generated variants and their output format.
type VariantResult struct {
	Variants []Variant
	Ext      string // file extension including dot: ".webp"
}
