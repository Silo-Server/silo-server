package imageutil

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"runtime"
	"testing"
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

func TestThumbhashRejectsGarbage(t *testing.T) {
	if _, err := Thumbhash([]byte("not an image at all")); err == nil {
		t.Fatal("Thumbhash accepted garbage input")
	}
}

// TestThumbhashDoesNotDecodeFullRasterInGo pins the reason the vips downscale
// runs before the Go decode: hashing must not materialize the original's full
// raster on the Go heap. A 6000×4000 JPEG decodes to ≥36 MiB in pure Go, and
// under tens of concurrent image-cache workers that is an OOM risk; through
// the vips path the Go side only ever decodes a ≤100px PNG. The 15 MiB bound
// is far above the new path's real footprint and far below the old one's.
func TestThumbhashDoesNotDecodeFullRasterInGo(t *testing.T) {
	data := largeTestJPEG(t, 6000, 4000)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	hash, err := Thumbhash(data)
	if err != nil {
		t.Fatalf("Thumbhash: %v", err)
	}
	if hash == "" {
		t.Fatal("Thumbhash returned empty hash")
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > 15<<20 {
		t.Fatalf("Thumbhash allocated %d bytes on the Go heap; the full raster is being decoded in Go", allocated)
	}
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

func TestGenerateVariantsWrapsErrInvalidImage(t *testing.T) {
	_, err := GenerateVariants([]byte("not an image"), []int{300})
	if !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("err = %v, want ErrInvalidImage", err)
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

func TestPixelDataUndecodable(t *testing.T) {
	fullPNG := testPNG(t, 600, 900)
	fullJPEG := largeTestJPEG(t, 600, 900)
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, image.NewPaletted(image.Rect(0, 0, 600, 900), palette.Plan9), nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	fullGIF := gifBuf.Bytes()

	for name, tc := range map[string]struct {
		data []byte
		want bool
	}{
		"valid png":      {fullPNG, false},
		"valid jpeg":     {fullJPEG, false},
		"truncated png":  {fullPNG[:len(fullPNG)/2], true},
		"truncated jpeg": {fullJPEG[:len(fullJPEG)/2], true},
		"truncated gif":  {fullGIF[:len(fullGIF)/2], false},
		"unknown":        {[]byte("not an image"), false},
		"huge header":    {withPNGDimensions(t, fullPNG[:len(fullPNG)/2], 100_000, 100_000), false},
		"just over cap":  {withPNGDimensions(t, fullPNG[:len(fullPNG)/2], 2_001, 2_000), false},
		"one tall row":   {withPNGDimensions(t, fullPNG[:len(fullPNG)/2], 1, maxPixelCheckPixels+1), false},
	} {
		if got := PixelDataUndecodable(tc.data); got != tc.want {
			t.Errorf("%s: PixelDataUndecodable = %v, want %v", name, got, tc.want)
		}
	}
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
