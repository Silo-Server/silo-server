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
	"sort"

	"github.com/h2non/bimg"
	"go.n16f.net/thumbhash"
)

const (
	webpQuality                = 90
	maxCachedOriginalDimension = 1920
	thumbhashSourceDimension   = 100
)

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

	variants := make([]Variant, 0, len(widths)+1)

	// Original — re-encode as WebP, strip metadata, and cap very large provider
	// artwork so cached originals do not preserve oversized source dimensions.
	originalOptions := bimg.Options{
		Type:          bimg.WEBP,
		Quality:       webpQuality,
		StripMetadata: true,
	}
	fitWithin(&originalOptions, size, maxCachedOriginalDimension)
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
func Thumbhash(data []byte) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		normalized, normalizeErr := normalizeThumbhashSource(data)
		if normalizeErr != nil {
			return "", fmt.Errorf("imageutil: decode for thumbhash: %w", err)
		}
		img, _, err = image.Decode(bytes.NewReader(normalized))
		if err != nil {
			return "", fmt.Errorf("imageutil: decode normalized thumbhash source: %w", err)
		}
	}

	scaled := scaleImage(img, 100)
	hashBytes := thumbhash.EncodeImage(scaled)
	return base64.StdEncoding.EncodeToString(hashBytes), nil
}

func normalizeThumbhashSource(data []byte) ([]byte, error) {
	img := bimg.NewImage(data)
	size, err := img.Size()
	if err != nil {
		return nil, fmt.Errorf("imageutil: invalid image: %w", err)
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
