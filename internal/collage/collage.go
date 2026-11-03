// Package collage composes poster collage images from multiple source images.
package collage

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// ErrNotEnoughImages is returned when no source images are usable.
var ErrNotEnoughImages = errors.New("collage: no usable source images")

const (
	posterWidth  = 600
	posterHeight = 900
	jpegQuality  = 92

	// maxSourcePixels bounds the raster image.Decode may allocate for one
	// source. Sources are small poster variants, about 300x450, and even a
	// full-size poster is about 6 megapixels, but a few-kilobyte file can
	// declare far more, and the decoder allocates the declared raster before
	// it reads the pixel data.
	maxSourcePixels = 16_000_000
)

// ComposePoster builds a poster collage from raw image bytes.
// Returns JPEG bytes suitable for feeding into processCollectionImage.
//
// Layout:
//   - 1 image: scale-fill the full canvas
//   - 2-3 images: 2 side-by-side columns
//   - 4+ images: 2×2 grid (first 4 used)
func ComposePoster(images [][]byte) ([]byte, error) {
	decoded := make([]image.Image, 0, len(images))
	for _, data := range images {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || int64(cfg.Width)*int64(cfg.Height) > maxSourcePixels {
			continue
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		decoded = append(decoded, img)
	}
	if len(decoded) == 0 {
		return nil, ErrNotEnoughImages
	}
	if len(decoded) > 4 {
		decoded = decoded[:4]
	}

	canvas := image.NewNRGBA(image.Rect(0, 0, posterWidth, posterHeight))

	// Fill with dark background in case images don't cover fully.
	for y := range posterHeight {
		for x := range posterWidth {
			canvas.SetNRGBA(x, y, color.NRGBA{R: 24, G: 24, B: 27, A: 255})
		}
	}

	switch len(decoded) {
	case 1:
		scaleFill(canvas, decoded[0], image.Rect(0, 0, posterWidth, posterHeight))
	case 2, 3:
		halfW := posterWidth / 2
		scaleFill(canvas, decoded[0], image.Rect(0, 0, halfW, posterHeight))
		scaleFill(canvas, decoded[1], image.Rect(halfW, 0, posterWidth, posterHeight))
	default:
		halfW := posterWidth / 2
		halfH := posterHeight / 2
		scaleFill(canvas, decoded[0], image.Rect(0, 0, halfW, halfH))
		scaleFill(canvas, decoded[1], image.Rect(halfW, 0, posterWidth, halfH))
		scaleFill(canvas, decoded[2], image.Rect(0, halfH, halfW, posterHeight))
		scaleFill(canvas, decoded[3], image.Rect(halfW, halfH, posterWidth, posterHeight))
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// scaleFill scales src to cover dst completely (crop-to-fill), centering the
// source image so excess is cropped equally from both sides. It crops in
// source coordinates and scales only the kept region: scaling the whole source
// first would allocate it at cover size, gigabytes for a source one pixel wide.
func scaleFill(dst *image.NRGBA, src image.Image, rect image.Rectangle) {
	dstW := rect.Dx()
	dstH := rect.Dy()
	crop := src.Bounds()
	srcW := crop.Dx()
	srcH := crop.Dy()

	// Trim the source to the destination's aspect ratio.
	if srcW*dstH > srcH*dstW {
		w := max(srcH*dstW/dstH, 1)
		crop.Min.X += (srcW - w) / 2
		crop.Max.X = crop.Min.X + w
	} else {
		h := max(srcW*dstH/dstW, 1)
		crop.Min.Y += (srcH - h) / 2
		crop.Max.Y = crop.Min.Y + h
	}

	draw.BiLinear.Scale(dst, rect, src, crop, draw.Over, nil)
}
