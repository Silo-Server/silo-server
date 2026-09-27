package imageutil

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
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

func TestThumbhashDeterministic(t *testing.T) {
	data := largeTestJPEG(t, 1200, 800)
	first, err := Thumbhash(data)
	if err != nil {
		t.Fatalf("Thumbhash: %v", err)
	}
	if first == "" {
		t.Fatal("Thumbhash returned empty hash")
	}
	second, err := Thumbhash(data)
	if err != nil {
		t.Fatalf("Thumbhash (second call): %v", err)
	}
	if first != second {
		t.Fatalf("Thumbhash not deterministic: %q vs %q", first, second)
	}
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
	if _, err := Thumbhash(data); err != nil {
		t.Fatalf("Thumbhash: %v", err)
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

func TestUndecodableSource(t *testing.T) {
	full := testPNG(t, 600, 900)
	for name, tc := range map[string]struct {
		data []byte
		want bool
	}{
		"valid png":     {full, false},
		"truncated png": {full[:len(full)/2], true},
		"unknown":       {[]byte("not an image"), false},
		"huge header":   {withPNGDimensions(t, full[:len(full)/2], 100_000, 100_000), false},
		"just over cap": {withPNGDimensions(t, full[:len(full)/2], 5_001, 5_000), false},
		"one tall row":  {withPNGDimensions(t, full[:len(full)/2], 1, maxFallbackDecodePixels+1), false},
	} {
		if got := undecodableSource(tc.data); got != tc.want {
			t.Errorf("%s: undecodableSource = %v, want %v", name, got, tc.want)
		}
	}
}

// Linux libvips reads a truncated PNG's header in Size and fails only in
// Process; other builds may tolerate the damage and return variants.
func TestGenerateVariantsTruncatedPNGIsInvalidWhenRejected(t *testing.T) {
	full := testPNG(t, 600, 900)
	_, err := GenerateVariants(full[:len(full)/2], []int{300})
	if err != nil && !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("err = %v, want nil or ErrInvalidImage", err)
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

func TestProcessErrorClassifiesUndecodableSource(t *testing.T) {
	full := testPNG(t, 600, 900)
	cause := errors.New("vips failure")

	if err := processError(full[:len(full)/2], "resize to w300", cause); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("truncated source: err = %v, want ErrInvalidImage", err)
	}
	err := processError(full, "resize to w300", cause)
	if errors.Is(err, ErrInvalidImage) || !errors.Is(err, cause) || err.Error() != "imageutil: resize to w300: vips failure" {
		t.Fatalf("decodable source: err = %v, want the wrapped server error", err)
	}
}
