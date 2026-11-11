package imageutil

import (
	"bytes"
	"encoding/binary"
	"errors"
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

// grayPNG encodes a blank grayscale PNG.
func grayPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestGenerateSquareVariantsBoundsTheSquare pins the square crop, including
// one taken after EXIF rotation, and the cap that keeps libwebp from encoding
// a huge original.
func TestGenerateSquareVariantsBoundsTheSquare(t *testing.T) {
	for _, tc := range []struct {
		name         string
		data         []byte
		wantOriginal int
	}{
		{"portrait", largeTestJPEG(t, 300, 500), 300},
		{"smaller than the variant", largeTestJPEG(t, 100, 150), 100},
		{"rotated by EXIF", withEXIFOrientation6(largeTestJPEG(t, 400, 300)), 300},
		{"over the original cap", grayPNG(t, 4000, 4100), MaxCachedOriginalDimension},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GenerateSquareVariants(tc.data, []int{256})
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

// tinyFloatTIFF is a small multi-band fixture, over the band limit when
// bands is more than four, with no pixel data to decode.
func tinyFloatTIFF(bands int) []byte {
	le := binary.LittleEndian
	body := []byte{0, 0, 0, 0} // the strip, at file offset 8
	var ifd []byte
	fields := 0
	// field adds one directory entry of SHORT (3) or LONG (4) values, which
	// the entry holds itself when they fit in four bytes.
	field := func(id, kind uint16, values ...uint32) {
		var raw []byte
		for _, v := range values {
			if kind == 3 {
				raw = le.AppendUint16(raw, uint16(v))
			} else {
				raw = le.AppendUint32(raw, v)
			}
		}
		value := uint32(8 + len(body))
		if len(raw) <= 4 {
			value = le.Uint32(append(raw, 0, 0, 0))
		} else {
			body = append(body, raw...)
		}
		ifd = le.AppendUint16(ifd, id)
		ifd = le.AppendUint16(ifd, kind)
		ifd = le.AppendUint32(ifd, uint32(len(values)))
		ifd = le.AppendUint32(ifd, value)
		fields++
	}
	repeat := func(v uint32, n int) []uint32 { return slices.Repeat([]uint32{v}, n) }
	field(256, 4, 8)                     // width
	field(257, 4, 8)                     // height
	field(258, 3, repeat(32, bands)...)  // bits per sample
	field(259, 3, 1)                     // no compression
	field(262, 3, 2)                     // RGB
	field(273, 4, 8)                     // the strip's offset
	field(277, 3, uint32(bands))         // samples per pixel
	field(278, 4, 8)                     // rows per strip
	field(279, 4, 4)                     // the strip's size
	field(338, 3, repeat(0, bands-3)...) // the bands after RGB are unspecified
	field(339, 3, repeat(3, bands)...)   // IEEE float

	out := le.AppendUint32([]byte{'I', 'I', 42, 0}, uint32(8+len(body)))
	out = append(out, body...)
	out = le.AppendUint16(out, uint16(fields))
	out = append(out, ifd...)
	return le.AppendUint32(out, 0)
}

// jpegHeader is a JPEG of one component (grey) or three (color) cut off at
// its first scan: its header reads and it has no tables or pixel data, so it
// cannot be decoded. Frame marker 0xC0 declares a baseline JPEG and 0xC2 a
// progressive one.
func jpegHeader(frame byte, components, width, height int) []byte {
	out := []byte{
		0xFF, 0xD8, // start of image
		0xFF, frame, 0, byte(8 + 3*components), 8, byte(height >> 8), byte(height), byte(width >> 8), byte(width), byte(components),
	}
	for c := 1; c <= components; c++ {
		out = append(out, byte(c), 0x11, 0) // not subsampled
	}
	out = append(out, 0xFF, 0xDA, 0, byte(6+2*components), byte(components)) // start of scan
	for c := 1; c <= components; c++ {
		out = append(out, byte(c), 0)
	}
	return append(out, 0, 63, 0, 0xFF, 0xD9) // end of image
}

// TestSourcesOverTheLimitsAreRefused covers every entry that hands
// caller-supplied bytes to libvips, and each limit on what a header may
// declare. The sources are headers with no image behind them: a limit is
// applied to what the header declares, before anything is decoded.
func TestSourcesOverTheLimitsAreRefused(t *testing.T) {
	manyBands := tinyFloatTIFF(16)
	for name, call := range map[string]func() error{
		"GenerateVariants":       func() error { _, err := GenerateVariants(manyBands, []int{300}); return err },
		"GenerateSquareVariants": func() error { _, err := GenerateSquareVariants(manyBands, []int{256}); return err },
		"EncodeWebPWidth":        func() error { _, err := EncodeWebPWidth(manyBands, 300); return err },
		"EncodeWebPWithin":       func() error { _, _, _, err := EncodeWebPWithin(manyBands, 300, 300); return err },
		"Thumbhash":              func() error { _, err := Thumbhash(manyBands); return err },
		"CheckImage":             func() error { return CheckImage(manyBands) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrSourceTooLarge) {
				t.Fatalf("err = %v, want ErrSourceTooLarge", err)
			}
		})
	}
	for name, data := range map[string][]byte{
		"pixels":                 jpegHeader(0xC0, 1, 9000, 9000),
		"decoded bytes":          jpegHeader(0xC0, 3, 7600, 7600),
		"progressive JPEG bytes": jpegHeader(0xC2, 3, 4400, 4400),
	} {
		t.Run(name, func(t *testing.T) {
			if err := CheckImage(data); !errors.Is(err, ErrSourceTooLarge) {
				t.Fatalf("err = %v, want ErrSourceTooLarge", err)
			}
		})
	}
	// The same frame without the coefficients a progressive decode holds.
	if err := CheckImage(jpegHeader(0xC0, 3, 4400, 4400)); err != nil {
		t.Fatalf("baseline JPEG of the progressive one's size: %v", err)
	}
	// The byte limit counts each sample at the width it decodes to.
	if header, err := readSourceHeader(tinyFloatTIFF(4), bimg.TIFF); err != nil || header.bands != 4 || header.sampleBytes != 4 {
		t.Fatalf("float TIFF = %d bands of %d-byte samples (%v), want 4 of 4", header.bands, header.sampleBytes, err)
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
