package imageutil

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/h2non/bimg"
)

func TestGenerateVariantsPreservesEncodes(t *testing.T) {
	for _, size := range [][2]int{{320, 180}, {500, 750}, {1920, 1080}, {500, 2400}, {2400, 1600}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			data := largeTestJPEG(t, size[0], size[1])
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
			for i, width := range []int{0, 1920, 780, 500, 500, 300} {
				opts := bimg.Options{Type: bimg.WEBP, Quality: webpQuality, StripMetadata: true}
				key := "original"
				if i == 0 {
					fitWithin(&opts, bimg.ImageSize{Width: size[0], Height: size[1]}, MaxCachedOriginalDimension)
				} else {
					key = fmt.Sprintf("w%d", width)
					if size[0] > width {
						opts.Width = width
					}
				}
				want, err := bimg.NewImage(data).Process(opts)
				if err != nil {
					t.Fatal(err)
				}
				if got.Variants[i].Key != key || !bytes.Equal(got.Variants[i].Data, want) {
					t.Fatalf("variant %d (%s) differs from independent encode", i, key)
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
