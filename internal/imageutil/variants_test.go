package imageutil

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/h2non/bimg"
)

func TestGenerateVariantsPreservesEncodes(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		wantSizes     [6][2]int
	}{
		{320, 180, [6][2]int{{320, 180}, {320, 180}, {320, 180}, {320, 180}, {320, 180}, {300, 169}}},
		{800, 4800, [6][2]int{{640, 3840}, {800, 4800}, {780, 4680}, {500, 3000}, {500, 3000}, {300, 1800}}},
		{4000, 2000, [6][2]int{{3840, 1920}, {1920, 960}, {780, 390}, {500, 250}, {500, 250}, {300, 150}}},
	} {
		t.Run(fmt.Sprintf("%dx%d", tc.width, tc.height), func(t *testing.T) {
			data := largeTestJPEG(t, tc.width, tc.height)
			widths := []int{300, 1920, 500, 780, 500}
			got, err := GenerateVariants(data, widths)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(widths, []int{300, 1920, 500, 780, 500}) {
				t.Fatal("mutated requested widths")
			}
			if got.Ext != ".webp" || len(got.Variants) != 6 {
				t.Fatalf("unexpected result: %#v", got)
			}
			for i, key := range []string{"original", "w1920", "w780", "w500", "w500", "w300"} {
				variant := got.Variants[i]
				if variant.Key != key || bimg.DetermineImageType(variant.Data) != bimg.WEBP {
					t.Fatalf("variant %d: key = %q, want %q with WebP data", i, variant.Key, key)
				}
				size, err := bimg.NewImage(variant.Data).Size()
				if err != nil {
					t.Fatal(err)
				}
				if want := tc.wantSizes[i]; size.Width != want[0] || size.Height != want[1] {
					t.Fatalf("variant %s = %dx%d, want %dx%d", key, size.Width, size.Height, want[0], want[1])
				}
			}
			before := bytes.Clone(got.Variants[4].Data)
			got.Variants[3].Data[0] ^= 0xff
			if !bytes.Equal(got.Variants[4].Data, before) {
				t.Fatal("duplicate variants share mutable buffers")
			}
		})
	}
}

// TestGenerateSquareVariantsBoundsTheSquare pins the square crop, including
// one taken after EXIF rotation, and the limits that keep an upload from
// making libvips decode or libwebp encode a huge raster.
func TestGenerateSquareVariantsBoundsTheSquare(t *testing.T) {
	grayPNG := func(width, height int) []byte {
		var buf bytes.Buffer
		if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, width, height))); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	for _, tc := range []struct {
		name         string
		data         []byte
		wantOriginal int // 0 when the source is rejected
	}{
		{"portrait", largeTestJPEG(t, 300, 500), 300},
		{"smaller than the variant", largeTestJPEG(t, 100, 150), 100},
		{"rotated by EXIF", withEXIFOrientation6(largeTestJPEG(t, 400, 300)), 300},
		{"over the original cap", grayPNG(4000, 4100), MaxCachedOriginalDimension},
		{"over the pixel budget", grayPNG(10_001, 10_000), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GenerateSquareVariants(tc.data, []int{256})
			if tc.wantOriginal == 0 {
				if err == nil {
					t.Fatal("accepted a source over the pixel budget")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range []int{tc.wantOriginal, 256} {
				size, err := bimg.NewImage(got.Variants[i].Data).Size()
				if err != nil {
					t.Fatal(err)
				}
				if size.Width != want || size.Height != want {
					t.Errorf("%s = %dx%d, want %dx%d", got.Variants[i].Key, size.Width, size.Height, want, want)
				}
			}
		})
	}
}

// withEXIFOrientation6 inserts an EXIF segment after the JPEG start marker
// that tells viewers to turn the image 90° clockwise, as phone cameras do.
func withEXIFOrientation6(jpg []byte) []byte {
	exif := []byte{
		0xFF, 0xE1, 0, 34, 'E', 'x', 'i', 'f', 0, 0, // APP1 and its length
		'M', 'M', 0, 42, 0, 0, 0, 8, // big-endian TIFF header, IFD at offset 8
		0, 1, // one IFD entry
		0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0, // Orientation, one SHORT: 6
		0, 0, 0, 0, // no next IFD
	}
	return slices.Concat(jpg[:2], exif, jpg[2:])
}

func TestEncodeWebPWidthMatchesVariantRung(t *testing.T) {
	for _, size := range [][2]int{{1920, 1080}, {200, 120}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			data := largeTestJPEG(t, size[0], size[1])
			variants, err := GenerateVariants(data, []int{300})
			if err != nil {
				t.Fatal(err)
			}
			got, err := EncodeWebPWidth(data, 300)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, variants.Variants[1].Data) {
				t.Fatal("single-width encode differs from the w300 variant")
			}
			decoded, err := bimg.NewImage(got).Size()
			if err != nil {
				t.Fatal(err)
			}
			if want := min(size[0], 300); decoded.Width != want {
				t.Fatalf("width = %d, want %d", decoded.Width, want)
			}
		})
	}
}

func TestEncodeWebPWidthRejectsGarbage(t *testing.T) {
	if _, err := EncodeWebPWidth([]byte("not an image"), 300); err == nil {
		t.Fatal("expected an error for invalid image data")
	}
}

func TestEncodePNGWithinFitsBoxWithoutUpscaling(t *testing.T) {
	for _, tc := range []struct{ w, h, wantW, wantH int }{
		{640, 128, 480, 96}, // width-bound
		{640, 320, 192, 96}, // height-bound
		{120, 40, 120, 40},  // already fits: no upscale
	} {
		t.Run(fmt.Sprintf("%dx%d", tc.w, tc.h), func(t *testing.T) {
			webp, err := EncodeWebPWidth(largeTestJPEG(t, tc.w, tc.h), tc.w)
			if err != nil {
				t.Fatal(err)
			}
			out, w, h, err := EncodePNGWithin(webp, 480, 96)
			if err != nil {
				t.Fatal(err)
			}
			if w != tc.wantW || h != tc.wantH {
				t.Fatalf("size = %dx%d, want %dx%d", w, h, tc.wantW, tc.wantH)
			}
			if bimg.DetermineImageType(out) != bimg.PNG {
				t.Fatal("output is not a PNG")
			}
		})
	}
}

func TestEncodePNGWithinRejectsGarbage(t *testing.T) {
	if _, _, _, err := EncodePNGWithin([]byte("not an image"), 480, 96); err == nil {
		t.Fatal("expected an error for invalid image data")
	}
}

// useDecodeBudget replaces the process's decode budget with one sized for a
// node with memoryBytes of memory, for the length of the test.
func useDecodeBudget(t *testing.T, memoryBytes int64) *decodeBudget {
	t.Helper()
	budget, previous := newDecodeBudget(memoryBytes), decodes
	decodes = func() *decodeBudget { return budget }
	t.Cleanup(func() { decodes = previous })
	return budget
}

// TestSourcesOverThePixelLimitAreRejected pins the limit on declared pixels
// for every function that hands source bytes to libvips. A small file can
// declare a raster of gigabytes, so the check has to come before the decode
// on each path, not only on the avatar one.
func TestSourcesOverThePixelLimitAreRejected(t *testing.T) {
	assertSourceRejected(t, withPNGDimensions(t, testPNG(t, 64, 64), 10_001, 10_000))
}

// TestFormatsTheBudgetCannotBoundAreRefused pins that no upload path decodes
// a format whose width and height do not bound its cost: an SVG can embed
// images larger than its canvas, a TIFF can carry any number of samples per
// pixel, and an AVIF or HEIF can declare a canvas smaller than its coded
// image.
func TestFormatsTheBudgetCannotBoundAreRefused(t *testing.T) {
	t.Run("svg", func(t *testing.T) {
		assertSourceRejected(t, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><rect width="8" height="8"/></svg>`))
	})
	t.Run("tiff", func(t *testing.T) {
		tiff, err := bimg.NewImage(testPNG(t, 8, 8)).Process(bimg.Options{Type: bimg.TIFF})
		if err != nil {
			t.Fatal(err)
		}
		if bimg.DetermineImageType(tiff) != bimg.TIFF {
			t.Fatal("fixture is not a TIFF")
		}
		assertSourceRejected(t, tiff)
	})
	t.Run("avif", func(t *testing.T) {
		avif, err := base64.StdEncoding.DecodeString(tinyAVIF)
		if err != nil {
			t.Fatal(err)
		}
		if bimg.DetermineImageType(avif) != bimg.AVIF {
			t.Fatal("fixture is not an AVIF")
		}
		assertSourceRejected(t, avif)
	})
}

// tinyAVIF is an 8x8 AVIF, base64-encoded.
const tinyAVIF = "AAAAHGZ0eXBhdmlmAAAAAG1pZjFhdmlmbWlhZgAAARdtZXRhAAAAAAAAACFoZGxyAAAAAAAAAABwaWN0AAAAAAAAAAAAAAAAAAAAADRpbG9jAAAAAERAAAIAAQAAAAABOwABAAAAAAAAACMAAgAAAAABXgABAAAAAAAAAL4AAAA4aWluZgAAAAAAAgAAABVpbmZlAgAAAAABAABhdjAxAAAAABVpbmZlAgAAAQACAABFeGlmAAAAAA5waXRtAAAAAAABAAAAVmlwcnAAAAA4aXBjbwAAAAxhdjFDgQAMAAAAABRpc3BlAAAAAAAAAAgAAAAIAAAAEHBpeGkAAAAAAwgICAAAABZpcG1hAAAAAAAAAAEAAQOBAgMAAAAaaXJlZgAAAAAAAAAOY2RzYwACAAEAAQAAAOltZGF0EgAKCBgIv2yAhoNCMhUcR4eF3cAALAAAAJA1jjx+mJmzC2oAAAAGRXhpZgAASUkqAAgAAAAGABIBAwABAAAAAQAAABoBBQABAAAAVgAAABsBBQABAAAAXgAAACgBAwABAAAAAgAAABMCAwABAAAAAQAAAGmHBAABAAAAZgAAAAAAAAAvGQEA6AMAAC8ZAQDoAwAABgAAkAcABAAAADAyMTABkQcABAAAAAECAwAAoAcABAAAADAxMDABoAMAAQAAAP//AAACoAQAAQAAAAgAAAADoAQAAQAAAAgAAAAAAAAA"

func assertSourceRejected(t *testing.T, data []byte) {
	t.Helper()
	for name, process := range map[string]func() error{
		"GenerateVariants":       func() error { _, err := GenerateVariants(data, []int{300}); return err },
		"GenerateSquareVariants": func() error { _, err := GenerateSquareVariants(data, []int{256}); return err },
		"EncodeWebPWidth":        func() error { _, err := EncodeWebPWidth(data, 300); return err },
		"EncodeWebPWithin":       func() error { _, _, _, err := EncodeWebPWithin(data, 300, 300); return err },
		"EncodePNGWithin":        func() error { _, _, _, err := EncodePNGWithin(data, 300, 300); return err },
		"Thumbhash":              func() error { _, err := Thumbhash(data); return err },
		"CheckImage":             func() error { return CheckImage(data) },
	} {
		if err := process(); !errors.Is(err, ErrInvalidImage) {
			t.Errorf("%s: err = %v, want ErrInvalidImage", name, err)
		}
	}
}

// TestSmallNodeAcceptsOnlyWhatItCanHold pins the limit a node's memory sets
// below the general one: a source must fit the node's whole decode budget.
func TestSmallNodeAcceptsOnlyWhatItCanHold(t *testing.T) {
	useDecodeBudget(t, int64(1)<<30)
	assertSourceRejected(t, withPNGDimensions(t, testPNG(t, 64, 64), 6_000, 5_000))
	if _, err := GenerateVariants(largeTestJPEG(t, 1920, 1080), []int{300}); err != nil {
		t.Fatalf("a 1080p source on a 1 GiB node: %v", err)
	}
}

// TestDecodeBudgetBoundsSourcesInFlight pins the bound on pixels being
// decoded at once: with the budget held, another source waits, gives up with
// ErrBusy and decodes nothing, and is accepted once the budget is released.
func TestDecodeBudgetBoundsSourcesInFlight(t *testing.T) {
	budget := useDecodeBudget(t, int64(1)<<30)
	previousWait := maxDecodeWait
	maxDecodeWait = time.Millisecond
	t.Cleanup(func() { maxDecodeWait = previousWait })

	release, err := budget.reserve(bimg.ImageSize{Width: int(budget.pixels), Height: 1})
	if err != nil {
		t.Fatal(err)
	}
	data := largeTestJPEG(t, 320, 180)
	if _, err := GenerateVariants(data, []int{300}); !errors.Is(err, ErrBusy) {
		t.Fatalf("with the budget held: err = %v, want ErrBusy", err)
	}
	if _, err := GenerateVariants(data[:len(data)/2], []int{300}); !errors.Is(err, ErrBusy) || SourceUnusable(err, data[:len(data)/2]) {
		t.Fatalf("damaged source with the budget held: err = %v, want ErrBusy and not the source's fault", err)
	}
	if _, err := Thumbhash(data); !errors.Is(err, ErrBusy) {
		t.Fatalf("thumbhash with the budget held: err = %v, want ErrBusy", err)
	}
	release()
	if _, err := GenerateVariants(data, []int{300}); err != nil {
		t.Fatalf("after release: %v", err)
	}
}
