package imageutil

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
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
