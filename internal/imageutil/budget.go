package imageutil

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/h2non/bimg"
	"golang.org/x/sync/semaphore"

	"github.com/Silo-Server/silo-server/internal/nodemetrics"
)

// ErrBusy reports that a source waited maxDecodeWait for room in the decode
// budget and found none. The image may be fine; the caller can try again.
var ErrBusy = errors.New("imageutil: image processing is busy")

// maxSourcePixels is the largest source any node accepts. A file's size says
// nothing about its raster: a PNG of a few hundred kilobytes can declare
// billions of pixels, and bimg opens every source with random access, so
// libvips decodes all of them. 100 megapixels is twice a 48-megapixel phone
// photo. A node with little memory accepts less; see decodeBudget.
const maxSourcePixels = 100_000_000

// decodeBytesPerPixel is what one declared source pixel is charged while
// libvips works on it. The most expensive accepted sources measured through
// GenerateVariants were a CMYK progressive JPEG at 27 bytes per pixel and a
// lossless WebP with alpha at 25; a 16-bit RGBA PNG was 19. The charge sits
// above all of them.
const decodeBytesPerPixel = 32

// decodeMemoryShare is the fraction of the process's memory limit, as a
// divisor, that sources being decoded may fill. The rest is the server's.
const decodeMemoryShare = 2

// unboundedDecodePixels is the budget where no memory limit is detectable.
const unboundedDecodePixels = 8 * maxSourcePixels

// maxDecodeWait bounds how long a source waits for room in the budget. The
// functions here take no context, so without it a caller that has gone away
// would stay queued, holding its source bytes. A variable so a test can
// shorten it.
var maxDecodeWait = 30 * time.Second

// decodeBudget bounds the declared pixels libvips is decoding at once across
// the whole process, which bounds the memory those decodes hold however many
// uploads and artwork jobs arrive together. A per-source limit alone does
// not: a hundred sources just under it are a hundred rasters.
type decodeBudget struct {
	pixels int64
	slots  *semaphore.Weighted
}

// newDecodeBudget sizes the budget to memoryBytes, the process's memory
// limit, or to unboundedDecodePixels when that is zero.
func newDecodeBudget(memoryBytes int64) *decodeBudget {
	pixels := int64(unboundedDecodePixels)
	if memoryBytes > 0 {
		pixels = memoryBytes / decodeMemoryShare / decodeBytesPerPixel
	}
	return &decodeBudget{pixels: pixels, slots: semaphore.NewWeighted(pixels)}
}

// decodes is the process's budget, sized on first use.
var decodes = sync.OnceValue(func() *decodeBudget {
	return newDecodeBudget(nodemetrics.ProcessMemoryLimitBytes())
})

// accepts reports whether a source of this size may be decoded at all: it
// must fit both maxSourcePixels and the whole budget, or it could never be
// reserved.
func (b *decodeBudget) accepts(size bimg.ImageSize) error {
	pixels := int64(size.Width) * int64(size.Height)
	if size.Width <= 0 || size.Height <= 0 || pixels > min(maxSourcePixels, b.pixels) {
		return fmt.Errorf("%w: %dx%d is not a size this server accepts", ErrInvalidImage, size.Width, size.Height)
	}
	return nil
}

// reserve holds size's pixels in the budget until release is called, waiting
// up to maxDecodeWait for room.
func (b *decodeBudget) reserve(size bimg.ImageSize) (release func(), err error) {
	if err := b.accepts(size); err != nil {
		return nil, err
	}
	pixels := int64(size.Width) * int64(size.Height)
	ctx, cancel := context.WithTimeout(context.Background(), maxDecodeWait)
	defer cancel()
	if err := b.slots.Acquire(ctx, pixels); err != nil {
		return nil, ErrBusy
	}
	return func() { b.slots.Release(pixels) }, nil
}

// NeedsRasterizing reports whether data is in a format libvips reads but the
// functions here refuse: SVG, HEIF or AVIF. Rasterize turns such a source
// into one they accept.
func NeedsRasterizing(data []byte) bool {
	switch bimg.DetermineImageType(data) {
	case bimg.SVG, bimg.HEIF, bimg.AVIF:
		return true
	default:
		return false
	}
}

// checkFormat refuses a source in a format whose declared width and height
// do not bound what decoding it costs, wrapping ErrInvalidImage. The budget
// charges decodeBytesPerPixel for each declared pixel, which holds for JPEG,
// PNG, WebP and GIF. It does not hold for the rest libvips reads here:
//
//   - TIFF allows any number of samples per pixel, and floating-point ones,
//     so a pixel can cost hundreds of bytes.
//   - HEIF and AVIF can declare a small canvas over a much larger coded
//     image (a grid or a crop), and a 12-bit 4:4:4 one with alpha measured
//     about 98 bytes per pixel.
//   - SVG declares a canvas, and an image embedded in it is decoded at that
//     image's own size, so a 1x1 SVG can hold a raster of gigabytes.
//
// Rasterize is the one way in for the last three, for sources the operator
// chose.
func checkFormat(data []byte) error {
	switch format := bimg.DetermineImageType(data); format {
	case bimg.JPEG, bimg.PNG, bimg.WEBP, bimg.GIF:
		return nil
	case bimg.UNKNOWN:
		return fmt.Errorf("%w: not a recognized image", ErrInvalidImage)
	default:
		return fmt.Errorf("%w: %s is not accepted here", ErrInvalidImage, bimg.ImageTypeName(format))
	}
}

// reserveSource reads data's header and reserves its pixels in the process's
// decode budget. Every function that hands source bytes to libvips for
// processing calls it first and releases when its last Process call returns.
// None of them calls another while holding a reservation. Bytes libvips
// cannot read, a format checkFormat refuses, and a size the node does not
// accept all wrap ErrInvalidImage.
func reserveSource(data []byte) (size bimg.ImageSize, release func(), err error) {
	if err := checkFormat(data); err != nil {
		return size, nil, err
	}
	return reserveHeader(data)
}

// reserveHeader is reserveSource without the format check.
func reserveHeader(data []byte) (size bimg.ImageSize, release func(), err error) {
	size, err = bimg.NewImage(data).Size()
	if err != nil {
		return size, nil, fmt.Errorf("%w: %w", ErrInvalidImage, err)
	}
	release, err = decodes().reserve(size)
	return size, release, err
}
