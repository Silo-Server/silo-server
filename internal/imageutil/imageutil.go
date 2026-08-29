// Package imageutil provides image resizing and thumbhash generation
// for collection poster and backdrop uploads.
package imageutil

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"sort"
	"strings"

	"github.com/h2non/bimg"
	"go.n16f.net/thumbhash"
)

// ValidateImage performs the same bounded dimension validation used before
// encoding and returns a safe image media type for resilient source delivery.
func ValidateImage(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("imageutil: empty image")
	}
	size, err := bimg.NewImage(data).Size()
	if err != nil {
		return "", fmt.Errorf("imageutil: invalid image: %w", err)
	}
	if err := checkSourceDimensions(size.Width, size.Height); err != nil {
		return "", err
	}
	mediaType := strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
	if !strings.HasPrefix(mediaType, "image/") || mediaType == "image/svg+xml" {
		return "", fmt.Errorf("imageutil: unsupported image media type %q", mediaType)
	}
	return mediaType, nil
}

const (
	webpQuality              = 90
	thumbhashSourceDimension = 100

	// maxSourcePixels bounds the decoded size of any source image before the
	// pipeline fully decodes it. Byte limits on uploads do not bound pixels: a
	// ~1 MB PNG can declare tens of thousands of pixels per side and cost
	// gigabytes of RAM and minutes of CPU to decode (a decompression bomb).
	// 80 MP comfortably exceeds 8K artwork (~33 MP) and current consumer
	// camera sensors while capping a single decode at a few hundred MB.
	maxSourcePixels = 80_000_000
)

// MaxCachedOriginalDimension caps the longest edge of the stored original.
// It is exported so the image-size capability reports the encoder's live
// contract instead of duplicating the number.
const MaxCachedOriginalDimension = 1920

// checkSourceDimensions rejects images whose header-declared dimensions are
// invalid or would decode to more than maxSourcePixels. Dimensions come from a
// cheap header read, so the check runs before any full decode.
func checkSourceDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("imageutil: invalid image dimensions %dx%d", width, height)
	}
	if int64(width)*int64(height) > maxSourcePixels {
		return fmt.Errorf("imageutil: image dimensions %dx%d exceed the %d-pixel limit", width, height, maxSourcePixels)
	}
	return nil
}

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
	img := bimg.NewImage(data)

	// Validate input by reading size.
	size, err := img.Size()
	if err != nil {
		return nil, fmt.Errorf("imageutil: invalid image: %w", err)
	}
	if err := checkSourceDimensions(size.Width, size.Height); err != nil {
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
	sorted := make([]int, len(widths))
	copy(sorted, widths)
	sort.Sort(sort.Reverse(sort.IntSlice(sorted)))

	for _, w := range sorted {
		size, _ := bimg.NewImage(data).Size()
		opts := bimg.Options{
			Type:          bimg.WEBP,
			Quality:       webpQuality,
			StripMetadata: true,
		}
		if size.Width > w {
			opts.Width = w
		}
		out, err := bimg.NewImage(data).Process(opts)
		if err != nil {
			return nil, fmt.Errorf("imageutil: resize to w%d: %w", w, err)
		}
		variants = append(variants, Variant{Key: fmt.Sprintf("w%d", w), Data: out})
	}

	return &VariantResult{Variants: variants, Ext: ".webp"}, nil
}

// GenerateSquareVariants center-crops the source image to a square and returns
// a square original plus resized square variants, all encoded as WebP.
func GenerateSquareVariants(data []byte, sizes []int) (*VariantResult, error) {
	img := bimg.NewImage(data)
	size, err := img.Size()
	if err != nil {
		return nil, fmt.Errorf("imageutil: invalid image: %w", err)
	}
	if err := checkSourceDimensions(size.Width, size.Height); err != nil {
		return nil, err
	}

	squareSize := size.Width
	if size.Height < squareSize {
		squareSize = size.Height
	}
	if squareSize <= 0 {
		return nil, fmt.Errorf("imageutil: invalid image size")
	}

	top := (size.Height - squareSize) / 2
	left := (size.Width - squareSize) / 2
	cropped, err := img.Extract(top, left, squareSize, squareSize)
	if err != nil {
		return nil, fmt.Errorf("imageutil: crop square: %w", err)
	}

	variants := make([]Variant, 0, len(sizes)+1)
	original, err := bimg.NewImage(cropped).Process(bimg.Options{
		Type:          bimg.WEBP,
		Quality:       webpQuality,
		StripMetadata: true,
	})
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
		opts := bimg.Options{
			Type:          bimg.WEBP,
			Quality:       webpQuality,
			StripMetadata: true,
			Width:         square,
			Height:        square,
			Force:         true,
			Enlarge:       squareSize < square,
		}
		out, err := bimg.NewImage(cropped).Process(opts)
		if err != nil {
			return nil, fmt.Errorf("imageutil: resize square to %d: %w", square, err)
		}
		variants = append(variants, Variant{Key: fmt.Sprintf("w%d", square), Data: out})
	}

	return &VariantResult{Variants: variants, Ext: ".webp"}, nil
}

// Thumbhash computes a base64-encoded thumbhash from raw image bytes.
// The image is scaled to max 100x100 before hashing.
//
// The downscale happens in libvips before the Go-side decode, not after:
// decoding a full provider original in Go materializes the whole raster on
// the heap — well over a hundred MiB for a large poster — per concurrent
// caller, while vips shrinks it to thumbhashSourceDimension with
// shrink-on-load and hands Go a raster of a few KiB. The pure-Go decode of
// the raw bytes remains as the fallback for anything vips cannot parse.
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
	if normalized, err := normalizeThumbhashSource(data); err == nil {
		if img, _, err := image.Decode(bytes.NewReader(normalized)); err == nil {
			return img, nil
		}
	}
	// The pure-Go fallback materializes the full raster, so bound the
	// header-declared dimensions first; vips never saw or already rejected
	// these bytes, and its shrink-on-load protection does not apply here.
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		if err := checkSourceDimensions(cfg.Width, cfg.Height); err != nil {
			return nil, err
		}
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("imageutil: decode for thumbhash: %w", err)
	}
	return img, nil
}

func normalizeThumbhashSource(data []byte) ([]byte, error) {
	img := bimg.NewImage(data)
	size, err := img.Size()
	if err != nil {
		return nil, fmt.Errorf("imageutil: invalid image: %w", err)
	}
	if err := checkSourceDimensions(size.Width, size.Height); err != nil {
		return nil, err
	}
	opts := bimg.Options{
		Type:          bimg.PNG,
		StripMetadata: true,
	}
	fitWithin(&opts, size, thumbhashSourceDimension)
	out, err := img.Process(opts)
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
