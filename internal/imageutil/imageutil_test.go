//go:build !silo_node

package imageutil

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/h2non/bimg"
)

// largeTestJPEG encodes a width×height gradient so the bytes are a real,
// decodable JPEG of meaningful dimensions rather than a fixture file.
func largeTestJPEG(t testing.TB, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 255 / width),
				G: uint8(y * 255 / height),
				B: uint8((x + y) * 255 / (width + height)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}
	return buf.Bytes()
}

func BenchmarkThumbhashLargeJPEG(b *testing.B) {
	data := largeTestJPEG(b, 6000, 4000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Thumbhash(data); err != nil {
			b.Fatalf("Thumbhash: %v", err)
		}
	}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 99, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// withPNGDimensions rewrites the IHDR width and height of a PNG, keeping the
// chunk CRC valid, so a small file declares an arbitrarily large raster.
func withPNGDimensions(t *testing.T, data []byte, width, height uint32) []byte {
	t.Helper()
	out := bytes.Clone(data)
	// Signature (8) + length (4) + "IHDR" (4), then width, height.
	const ihdr = 8 + 4
	if string(out[ihdr:ihdr+4]) != "IHDR" {
		t.Fatal("IHDR is not the first chunk")
	}
	binary.BigEndian.PutUint32(out[ihdr+4:], width)
	binary.BigEndian.PutUint32(out[ihdr+8:], height)
	binary.BigEndian.PutUint32(out[ihdr+4+13:], crc32.ChecksumIEEE(out[ihdr:ihdr+4+13]))
	return out
}

// TMDB serves logos as SVGs whose canvas is a few dozen units wide. Drawn at
// that canvas, every rung of a logo was a 54px image. The size asked for is
// small enough for any node's decode budget, so the result does not depend on
// the test machine's memory.
func TestRasterizeWithinDrawsSVGAtTheRequestedSize(t *testing.T) {
	const maxDim = 1000
	cases := map[string]struct {
		svg                 string
		wantWidth, wantHigh int
	}{
		"viewBox only": {
			svg:       `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 53.921 24"><rect width="53.921" height="24" fill="#f64312"/></svg>`,
			wantWidth: maxDim, wantHigh: 445,
		},
		// The declared size rounds to 1x1; the raster must still be maxDim.
		"fractional canvas": {
			svg:       `<svg xmlns="http://www.w3.org/2000/svg" width="1.49" height="1.49"><rect width="1.49" height="1.49"/></svg>`,
			wantWidth: maxDim, wantHigh: maxDim,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rasterized, err := RasterizeWithin([]byte(tc.svg), maxDim)
			if err != nil {
				t.Fatalf("RasterizeWithin: %v", err)
			}
			size, err := bimg.NewImage(rasterized).Size()
			if err != nil {
				t.Fatalf("Size: %v", err)
			}
			// libvips rounds each edge, so allow a pixel either way.
			if abs(size.Width-tc.wantWidth) > 1 || abs(size.Height-tc.wantHigh) > 1 {
				t.Fatalf("rasterized to %dx%d, want %dx%d", size.Width, size.Height, tc.wantWidth, tc.wantHigh)
			}
		})
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
