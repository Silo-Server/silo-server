package collage

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

// makeTestImage creates a solid-color JPEG image of the given size.
func makeTestImage(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestComposePoster_AllInvalid(t *testing.T) {
	_, err := ComposePoster([][]byte{{0, 1, 2}, {3, 4, 5}})
	if !errors.Is(err, ErrNotEnoughImages) {
		t.Fatalf("expected ErrNotEnoughImages, got %v", err)
	}
}

func TestComposePoster_FourImages(t *testing.T) {
	colors := []color.NRGBA{
		{R: 255, A: 255},
		{G: 255, A: 255},
		{B: 255, A: 255},
		{R: 255, G: 255, A: 255},
	}
	images := make([][]byte, len(colors))
	for i, c := range colors {
		images[i] = makeTestImage(t, 300, 450, c)
	}
	result, err := ComposePoster(images)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(result))
	if err != nil {
		t.Fatal(err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != posterWidth || bounds.Dy() != posterHeight {
		t.Fatalf("expected %dx%d, got %dx%d", posterWidth, posterHeight, bounds.Dx(), bounds.Dy())
	}
}

func TestComposePoster_SkipsInvalidImages(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 200, 300))
	for y := range 300 {
		for x := range 200 {
			img.SetNRGBA(x, y, color.NRGBA{G: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	valid := buf.Bytes()
	result, err := ComposePoster([][]byte{{0, 1, 2}, valid, {3, 4, 5}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
}

// TestComposePosterBoundsMemory pins that a source costs about its own small
// raster. Scaling a thin poster whole to cover its tile used to allocate
// 600x120000 here, and decoding a header allocates the raster it declares
// before reading pixel data. The 16 MiB bound is far above a real collage.
func TestComposePosterBoundsMemory(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 200))); err != nil {
		t.Fatal(err)
	}
	thin := buf.Bytes()
	// Declare 20000x20000 in the IHDR chunk and keep its CRC valid.
	huge := bytes.Clone(thin)
	binary.BigEndian.PutUint32(huge[16:], 20_000)
	binary.BigEndian.PutUint32(huge[20:], 20_000)
	binary.BigEndian.PutUint32(huge[29:], crc32.ChecksumIEEE(huge[12:29]))

	for _, tc := range []struct {
		name    string
		data    []byte
		wantErr error
	}{
		{"thin source", thin, nil},
		{"oversized header", huge, ErrNotEnoughImages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := ComposePoster([][]byte{tc.data})
			var after runtime.MemStats
			runtime.ReadMemStats(&after)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
				t.Fatalf("ComposePoster allocated %d bytes", allocated)
			}
		})
	}
}
