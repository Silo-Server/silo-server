package imageutil

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/h2non/bimg"
)

func TestGenerateVariantsPreservesEncodes(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		wantSizes     [6][2]int
	}{
		{320, 180, [6][2]int{{320, 180}, {320, 180}, {320, 180}, {320, 180}, {320, 180}, {300, 169}}},
		{500, 750, [6][2]int{{500, 750}, {500, 750}, {500, 750}, {500, 750}, {500, 750}, {300, 450}}},
		{1920, 1080, [6][2]int{{1920, 1080}, {1920, 1080}, {780, 439}, {500, 281}, {500, 281}, {300, 169}}},
		{500, 2400, [6][2]int{{400, 1920}, {500, 2400}, {500, 2400}, {500, 2400}, {500, 2400}, {300, 1440}}},
		{2400, 1600, [6][2]int{{1920, 1280}, {1920, 1280}, {780, 520}, {500, 333}, {500, 333}, {300, 200}}},
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
