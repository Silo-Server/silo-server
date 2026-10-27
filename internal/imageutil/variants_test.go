package imageutil

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"reflect"
	"slices"
	"testing"

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
