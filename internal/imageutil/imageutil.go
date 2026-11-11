// Package imageutil provides image resizing and thumbhash generation
// for collection poster and backdrop uploads.
package imageutil

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"slices"
	"sort"

	"github.com/h2non/bimg"
	"go.n16f.net/thumbhash"
)

const (
	webpQuality              = 90
	thumbhashSourceDimension = 100
)

// libvips threading: bimg's package init pins libvips to a single thread per
// operation (vips_concurrency_set(1)) unless VIPS_CONCURRENCY is set in the
// environment. Every caller in Silo runs encodes from its own worker pool, so
// that pool is the only source of parallelism and the pools are sized per
// CPU core; see tasks.imageCacheWorkerCount. Do not raise VIPS_CONCURRENCY
// in deployments without lowering those pools, or the host oversubscribes.

// ErrInvalidImage reports source bytes this package will not process: ones
// libvips cannot read as an image, and ones that would decode to more than
// the source limits allow (ErrSourceTooLarge). Failures after the header was
// read, such as encoding a variant, do not wrap it; see PixelDataUndecodable
// for damaged pixel data.
var ErrInvalidImage = errors.New("imageutil: invalid image")

// ErrSourceTooLarge reports a source whose header declares more than
// MaxSourcePixels, MaxSourceBands or MaxSourceBytes. It wraps
// ErrInvalidImage, so a caller that refuses unreadable images refuses these
// too. Unlike an unreadable download, the same bytes fail the same way every
// time.
var ErrSourceTooLarge = fmt.Errorf("%w: too large to decode", ErrInvalidImage)

// maxGoDecodePixels bounds the raster this package lets the standard library
// allocate, in PixelDataUndecodable and the thumbhash fallback. A
// few-kilobyte file can declare large dimensions, and image.Decode allocates
// the whole raster before it reads the pixel data. At 4 megapixels that is
// 32 MB for a 16-bit PNG, and about twice that for an interlaced one, whose
// passes are decoded into separate images.
const maxGoDecodePixels = 4_000_000

// The source limits bound what every entry point of this package hands to
// libvips. Uploads and downloads are capped in bytes, but decoded size, not
// file size, is what an image costs to process, and libvips decodes the
// whole raster, into memory when it is small and into a temporary file
// otherwise. sourceSize therefore checks what the header declares before
// anything is decoded.
const (
	// MaxSourceBytes bounds the raster one decode holds: width × height ×
	// bands × bytes per sample, and for a progressive JPEG its coefficients
	// as well, two bytes a sample, which libjpeg keeps until the last scan is
	// read. 160 MiB admits an 8-bit color photo of 55 megapixels, which
	// covers a 50-megapixel phone frame and a 6000×9000 poster, an 8-bit
	// RGBA image of 41, and a progressive color JPEG of 18. It was set by
	// measurement: caching one source at the limit with the widest variant
	// ladder peaked near the memory the image cache budgets for a worker
	// (tasks.imageCacheWorkerMemoryBudget).
	//
	// An SVG is the exception. Its header gives the size it renders at, but
	// rendering costs whatever its drawing asks for, which no header bounds.
	// The limits apply to its raster and nothing more, so SVG is refused
	// from a profile's own uploads (CheckProfileImage) and still decoded for
	// provider artwork, library sidecar files and administrator uploads.
	MaxSourceBytes = 160 << 20

	// MaxSourcePixels binds only sources of one or two 8-bit bands, and
	// 16-bit grey, which the byte limit alone would admit up to 167
	// megapixels.
	MaxSourcePixels = 65_000_000

	// MaxSourceBands admits grey, grey with alpha, RGB, RGBA and CMYK.
	MaxSourceBands = 4
)

// decodableSignature reports whether data begins as one of the formats this
// package decodes: JPEG, GIF, PNG, TIFF, WebP, SVG, HEIF or AVIF. It is plain
// Go and tests the formats in bimg's order, so what passes is opened by the
// loader for that format and no other.
func decodableSignature(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	switch {
	case data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF: // JPEG
	case bytes.HasPrefix(data, []byte("GIF")):
	case bytes.HasPrefix(data, []byte("\x89PNG")):
	case bytes.HasPrefix(data, []byte("II*\x00")), bytes.HasPrefix(data, []byte("MM\x00*")): // TIFF
	case bytes.HasPrefix(data, []byte("%PDF")):
		return false
	case string(data[8:12]) == "WEBP":
	case bimg.IsSVGImage(data):
	case string(data[4:8]) == "ftyp" && slices.Contains(heifBrands, string(data[8:12])):
	default:
		return false
	}
	return true
}

// heifBrands are the HEIF and AVIF file types bimg recognizes.
var heifBrands = []string{"heic", "mif1", "msf1", "heis", "hevc", "avif"}

// decodableType names the loader bimg uses for data, or refuses data in any
// other format. PDF and the formats only ImageMagick reads are not artwork,
// and libvips blocks its loaders for them (untrusted.go); their bytes are
// turned away here, in Go, so that libvips is not called on them at all.
func decodableType(data []byte) (bimg.ImageType, error) {
	if decodableSignature(data) {
		switch kind := bimg.DetermineImageType(data); kind {
		case bimg.JPEG, bimg.GIF, bimg.PNG, bimg.TIFF, bimg.WEBP, bimg.SVG, bimg.HEIF, bimg.AVIF:
			return kind, nil
		}
	}
	return bimg.UNKNOWN, fmt.Errorf("%w: not in a format Silo decodes", ErrInvalidImage)
}

// CheckProfileImage reports whether a signed-in profile may supply data as
// its own artwork: a personal collection poster or an avatar. An SVG may not,
// for the reason given at MaxSourceBytes; the image cache still decodes the
// SVG logos providers serve. It reads data's bytes and calls nothing in
// libvips. A refusal wraps ErrInvalidImage.
func CheckProfileImage(data []byte) error {
	if bimg.IsSVGImage(data) {
		return fmt.Errorf("%w: SVG is not accepted from a profile", ErrInvalidImage)
	}
	return nil
}

// sourceSize reads the dimensions data declares, without decoding it, and
// refuses a source in a format this package does not decode, one libvips
// cannot read, and one over the source limits.
func sourceSize(data []byte) (bimg.ImageSize, error) {
	kind, err := decodableType(data)
	if err != nil {
		return bimg.ImageSize{}, err
	}
	header, err := readSourceHeader(data, kind)
	if err != nil {
		return bimg.ImageSize{}, fmt.Errorf("%w: %w", ErrInvalidImage, err)
	}
	if header.width <= 0 || header.height <= 0 {
		return bimg.ImageSize{}, fmt.Errorf("%w: size %dx%d", ErrInvalidImage, header.width, header.height)
	}
	bytesPerSample := int64(header.sampleBytes)
	if header.progressive {
		bytesPerSample += 2
	}
	// Checked in this order, the last product cannot overflow.
	pixels := int64(header.width) * int64(header.height)
	if pixels > MaxSourcePixels || header.bands > MaxSourceBands || pixels*int64(header.bands)*bytesPerSample > MaxSourceBytes {
		return bimg.ImageSize{}, fmt.Errorf("%w: %dx%d with %d bands of %d-byte samples (progressive: %t)",
			ErrSourceTooLarge, header.width, header.height, header.bands, header.sampleBytes, header.progressive)
	}
	return bimg.ImageSize{Width: header.width, Height: header.height}, nil
}

// PixelDataUndecodable reports whether data is a JPEG or PNG whose header
// reads but whose pixel data does not decode. libvips reads only the header in
// Size, so such a file first fails inside Process, where the error looks like
// an encoder failure. Callers use it after GenerateVariants fails to tell
// damaged input from a server fault. Other formats, including those other
// packages register with image, sources above maxGoDecodePixels, and
// features the standard library does not support report false.
func PixelDataUndecodable(data []byte) bool {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") ||
		int64(cfg.Width)*int64(cfg.Height) > maxGoDecodePixels {
		return false
	}
	_, _, err = image.Decode(bytes.NewReader(data))
	var jpegUnsupported jpeg.UnsupportedError
	var pngUnsupported png.UnsupportedError
	return err != nil && !errors.As(err, &jpegUnsupported) && !errors.As(err, &pngUnsupported)
}

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

// GenerateVariants produces WebP variants of the source image at the requested
// widths, plus an "original" re-encoded as WebP. WebP provides better quality
// per byte than JPEG and supports transparency (unlike JPEG). Images narrower
// than a target width are re-encoded without upscaling. All resizes operate on
// the original bytes to avoid compounding quality loss.
func GenerateVariants(data []byte, widths []int) (*VariantResult, error) {
	size, err := sourceSize(data)
	if err != nil {
		return nil, err
	}

	variants := make([]Variant, 0, len(widths)+1)

	// Original — re-encode as WebP, strip metadata, and cap very large provider
	// artwork so cached originals do not preserve oversized source dimensions.
	originalOptions := bimg.Options{
		Type:          bimg.WEBP,
		Quality:       webpQuality,
		StripMetadata: true,
	}
	fitWithin(&originalOptions, size, MaxCachedOriginalDimension)
	original, err := bimg.NewImage(data).Process(originalOptions)
	if err != nil {
		return nil, fmt.Errorf("imageutil: encode original: %w", err)
	}
	variants = append(variants, Variant{Key: "original", Data: original})

	// Sort widths descending (largest first).
	sorted := slices.Clone(widths)
	slices.Sort(sorted)
	slices.Reverse(sorted)

	previousWidth, previousHeight := originalOptions.Width, originalOptions.Height
	previousOutput := original
	for _, w := range sorted {
		opts := bimg.Options{
			Type:          bimg.WEBP,
			Quality:       webpQuality,
			StripMetadata: true,
		}
		if size.Width > w {
			opts.Width = w
		}
		var out []byte
		if opts.Width == previousWidth && opts.Height == previousHeight {
			// Equivalent encodes share the work, while each variant retains its
			// own buffer. Compare both dimensions: tall originals may be capped.
			out = bytes.Clone(previousOutput)
		} else {
			out, err = bimg.NewImage(data).Process(opts)
			if err != nil {
				return nil, fmt.Errorf("imageutil: resize to w%d: %w", w, err)
			}
		}
		previousWidth, previousHeight, previousOutput = opts.Width, opts.Height, out
		variants = append(variants, Variant{Key: fmt.Sprintf("w%d", w), Data: out})
	}

	return &VariantResult{Variants: variants, Ext: ".webp"}, nil
}

// CheckImage reports whether data is an image an upload would accept as
// a source: a header libvips reads, within the source limits, and, for
// a JPEG or PNG, pixel data that decodes (PixelDataUndecodable). A file
// failing it wraps ErrInvalidImage.
func CheckImage(data []byte) error {
	if _, err := sourceSize(data); err != nil {
		return err
	}
	if PixelDataUndecodable(data) {
		return fmt.Errorf("%w: its pixel data does not decode", ErrInvalidImage)
	}
	return nil
}

// EncodeWebPWidth re-encodes the source image as a single WebP no wider than
// width, with metadata stripped. It is the "w<width>" rung of GenerateVariants
// for callers that store that one variant and no original. Narrower sources
// keep their width.
func EncodeWebPWidth(data []byte, width int) ([]byte, error) {
	size, err := sourceSize(data)
	if err != nil {
		return nil, err
	}
	opts := bimg.Options{
		Type:          bimg.WEBP,
		Quality:       webpQuality,
		StripMetadata: true,
	}
	if size.Width > width {
		opts.Width = width
	}
	out, err := bimg.NewImage(data).Process(opts)
	if err != nil {
		return nil, fmt.Errorf("imageutil: resize to w%d: %w", width, err)
	}
	return out, nil
}

// EncodePNGWithin re-encodes the source image as a PNG that fits within
// maxWidth×maxHeight, keeping its aspect ratio and never upscaling, and
// returns the encoded dimensions. Email uses it: unlike WebP, PNG renders in
// every mail client.
func EncodePNGWithin(data []byte, maxWidth, maxHeight int) (out []byte, width, height int, err error) {
	return encodeWithin(data, bimg.Options{Type: bimg.PNG, StripMetadata: true}, "png", maxWidth, maxHeight)
}

// EncodeWebPWithin is EncodePNGWithin for WebP, with transparency kept. Card
// overlay images use it: one small image per badge, with no variants beside
// it.
func EncodeWebPWithin(data []byte, maxWidth, maxHeight int) (out []byte, width, height int, err error) {
	return encodeWithin(data, bimg.Options{Type: bimg.WEBP, Quality: webpQuality, StripMetadata: true}, "webp", maxWidth, maxHeight)
}

// encodeWithin re-encodes data with opts so it fits within
// maxWidth×maxHeight, never upscaling, and returns the encoded dimensions. A
// source sourceSize refuses wraps ErrInvalidImage.
func encodeWithin(data []byte, opts bimg.Options, format string, maxWidth, maxHeight int) (out []byte, width, height int, err error) {
	size, err := sourceSize(data)
	if err != nil {
		return nil, 0, 0, err
	}
	// Constrain only the binding edge so libvips keeps the aspect ratio.
	if size.Width*maxHeight >= size.Height*maxWidth {
		if size.Width > maxWidth {
			opts.Width = maxWidth
		}
	} else if size.Height > maxHeight {
		opts.Height = maxHeight
	}
	out, err = bimg.NewImage(data).Process(opts)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("imageutil: encode %s: %w", format, err)
	}
	encoded, err := readSourceHeader(out, opts.Type)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("imageutil: read %s size: %w", format, err)
	}
	return out, encoded.width, encoded.height, nil
}

// GenerateSquareVariants center-crops the source image to a square and returns
// a square original plus resized square variants, all encoded as WebP. The
// original is capped at MaxCachedOriginalDimension, because libwebp holds the
// whole picture while it encodes.
func GenerateSquareVariants(data []byte, sizes []int) (*VariantResult, error) {
	size, err := sourceSize(data)
	if err != nil {
		return nil, err
	}
	squareSize := min(size.Width, size.Height)

	variants := make([]Variant, 0, len(sizes)+1)
	original, err := encodeSquare(data, min(squareSize, MaxCachedOriginalDimension), squareSize)
	if err != nil {
		return nil, fmt.Errorf("imageutil: encode square original: %w", err)
	}
	variants = append(variants, Variant{Key: "original", Data: original})

	sorted := make([]int, len(sizes))
	copy(sorted, sizes)
	sort.Sort(sort.Reverse(sort.IntSlice(sorted)))

	for _, square := range sorted {
		if square <= 0 {
			continue
		}
		out, err := encodeSquare(data, square, squareSize)
		if err != nil {
			return nil, fmt.Errorf("imageutil: resize square to %d: %w", square, err)
		}
		variants = append(variants, Variant{Key: fmt.Sprintf("w%d", square), Data: out})
	}

	return &VariantResult{Variants: variants, Ext: ".webp"}, nil
}

// encodeSquare encodes the centered side×side WebP square of data, whose
// shorter edge is squareSize. libvips scales the source so that edge fills
// the square, then crops, in one pass: no full-resolution intermediate, and
// the crop is taken after EXIF auto-rotation.
func encodeSquare(data []byte, side, squareSize int) ([]byte, error) {
	return bimg.NewImage(data).Process(bimg.Options{
		Type:          bimg.WEBP,
		Quality:       webpQuality,
		StripMetadata: true,
		Width:         side,
		Height:        side,
		Crop:          true,
		Enlarge:       squareSize < side,
	})
}

// Thumbhash computes a base64-encoded thumbhash from raw image bytes.
// The image is scaled to max 100x100 before hashing.
//
// The downscale happens in libvips before the Go-side decode, not after:
// decoding a full provider original in Go materializes the whole raster on
// the heap — well over a hundred MiB for a large poster — per concurrent
// caller, while vips shrinks it to thumbhashSourceDimension with
// shrink-on-load and hands Go a raster of a few KiB. The pure-Go decode of
// the raw bytes remains as the fallback for anything vips cannot parse, for
// sources up to maxGoDecodePixels.
//
// Changing this pipeline changes the emitted hash bytes for a given image.
// Stored thumbhashes remain valid placeholders, and the one site that
// compares hashes for equality (ebook scan cover change detection) stores
// the freshly computed hash whenever it re-caches, so a pipeline change
// costs one re-cache per scan-covered ebook and then converges.
func Thumbhash(data []byte) (string, error) {
	img, err := decodeThumbhashSource(data)
	if err != nil {
		return "", err
	}
	scaled := scaleImage(img, thumbhashSourceDimension)
	hashBytes := thumbhash.EncodeImage(scaled)
	return base64.StdEncoding.EncodeToString(hashBytes), nil
}

func decodeThumbhashSource(data []byte) (image.Image, error) {
	normalized, normalizeErr := normalizeThumbhashSource(data)
	if normalizeErr == nil {
		if img, _, err := image.Decode(bytes.NewReader(normalized)); err == nil {
			return img, nil
		}
	}
	// A source over the limits is not decoded by the standard library
	// either.
	if errors.Is(normalizeErr, ErrSourceTooLarge) {
		return nil, normalizeErr
	}
	img, err := decodeWithinBudget(data)
	if err != nil {
		// Bytes neither libvips nor the standard library can read keep
		// ErrInvalidImage, as GenerateVariants reports them.
		if errors.Is(normalizeErr, ErrInvalidImage) {
			return nil, fmt.Errorf("%w: decode for thumbhash: %w", ErrInvalidImage, err)
		}
		return nil, fmt.Errorf("imageutil: decode for thumbhash: %w", err)
	}
	return img, nil
}

// decodeWithinBudget decodes data with the standard library unless its header
// declares more than maxGoDecodePixels.
func decodeWithinBudget(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxGoDecodePixels {
		return nil, fmt.Errorf("%dx%d image exceeds %d pixels", cfg.Width, cfg.Height, maxGoDecodePixels)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func normalizeThumbhashSource(data []byte) ([]byte, error) {
	size, err := sourceSize(data)
	if err != nil {
		return nil, err
	}
	opts := bimg.Options{
		Type:          bimg.PNG,
		StripMetadata: true,
	}
	fitWithin(&opts, size, thumbhashSourceDimension)
	out, err := bimg.NewImage(data).Process(opts)
	if err != nil {
		return nil, fmt.Errorf("imageutil: normalize thumbhash source: %w", err)
	}
	return out, nil
}

func fitWithin(opts *bimg.Options, size bimg.ImageSize, maxDim int) {
	if opts == nil || maxDim <= 0 {
		return
	}
	if size.Width <= maxDim && size.Height <= maxDim {
		return
	}
	if size.Width >= size.Height {
		opts.Width = maxDim
		return
	}
	opts.Height = maxDim
}

// scaleImage scales src so its longest dimension does not exceed maxDim,
// preserving aspect ratio. Uses nearest-neighbour interpolation.
func scaleImage(src image.Image, maxDim int) *image.NRGBA {
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	scale := 1.0
	if srcW > maxDim || srcH > maxDim {
		scaleW := float64(maxDim) / float64(srcW)
		scaleH := float64(maxDim) / float64(srcH)
		if scaleW < scaleH {
			scale = scaleW
		} else {
			scale = scaleH
		}
	}

	dstW := max(int(float64(srcW)*scale), 1)
	dstH := max(int(float64(srcH)*scale), 1)
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))

	for y := range dstH {
		srcY := min(int(float64(y)/scale), srcH-1)
		for x := range dstW {
			srcX := min(int(float64(x)/scale), srcW-1)
			r, g, b, a := src.At(bounds.Min.X+srcX, bounds.Min.Y+srcY).RGBA()
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r >> 8), G: uint8(g >> 8),
				B: uint8(b >> 8), A: uint8(a >> 8),
			})
		}
	}
	return dst
}
