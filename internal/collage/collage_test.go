package collage

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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
	if err != ErrNotEnoughImages {
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
