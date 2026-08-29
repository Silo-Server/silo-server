package imageutil

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"runtime"
	"strings"
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
func TestCheckSourceDimensions(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		wantErr       bool
	}{
		{"poster", 2000, 3000, false},
		{"8k backdrop", 7680, 4320, false},
		{"zero width", 0, 100, true},
		{"negative height", 100, -1, true},
		{"decompression bomb", 40000, 40000, true},
		{"overflow-sized", 1 << 30, 1 << 30, true},
	}
	for _, tc := range cases {
		err := checkSourceDimensions(tc.width, tc.height)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: checkSourceDimensions(%d, %d) = %v, wantErr %v", tc.name, tc.width, tc.height, err, tc.wantErr)
		}
	}
}

// TestThumbhashRejectsOversizedHeaderBeforeDecoding feeds a syntactically valid
// PNG header declaring 40000x40000 pixels with no pixel data behind it. The
// dimension check must fire on the header alone — reaching the full decode
// would attempt a multi-gigabyte allocation.
func TestThumbhashRejectsOversizedHeaderBeforeDecoding(t *testing.T) {
	if _, err := Thumbhash(forgedHugePNG()); err == nil || !strings.Contains(err.Error(), "pixel limit") {
		t.Fatalf("Thumbhash on a 1.6-gigapixel header = %v, want the pixel-limit error", err)
	}
}

// forgedHugePNG builds the PNG signature plus an IHDR chunk claiming
// 40000x40000 8-bit RGBA. image.DecodeConfig parses exactly this much.
func forgedHugePNG() []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 40000) // width
	binary.BigEndian.PutUint32(ihdr[4:], 40000) // height
	ihdr[8] = 8                                 // bit depth
	ihdr[9] = 6                                 // color type RGBA
	// compression, filter, interlace = 0

	chunk := append([]byte("IHDR"), ihdr...)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = binary.BigEndian.AppendUint32(out, uint32(len(ihdr)))
	out = append(out, chunk...)
	out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
	return out
}
