//go:build !silo_node

package imageutil

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

// TestDecodeBudgetReturnsFreedMemoryOnceIdle pins when malloc's free pages go
// back to the operating system: only once the budget has stayed empty for a
// whole idleReleaseDelay. A reservation taken during the delay, whether still
// held or already released, cancels that release.
func TestDecodeBudgetReturnsFreedMemoryOnceIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		budget := newDecodeBudget(int64(1) << 30)
		var frees atomic.Int32
		budget.freeMemory = func() { frees.Add(1) }
		reserve := func() func() {
			t.Helper()
			release, err := budget.reserve(bimg.ImageSize{Width: 10, Height: 10})
			if err != nil {
				t.Fatal(err)
			}
			return release
		}
		wait := func(d time.Duration, wantFrees int32, when string) {
			t.Helper()
			time.Sleep(d) //nolint:forbidigo // synctest fake clock: sleeping advances time; nothing waits on the real clock.
			synctest.Wait()
			if got := frees.Load(); got != wantFrees {
				t.Fatalf("%s: %d frees, want %d", when, got, wantFrees)
			}
		}

		reserve()()
		wait(time.Second, 0, "before the delay ended")
		held := reserve()
		wait(idleReleaseDelay, 0, "a decode held the budget when the delay ended")

		held()
		wait(idleReleaseDelay/2, 0, "halfway through the delay")
		reserve()()
		wait(idleReleaseDelay/2+time.Second, 0, "a decode ran during the delay")
		wait(idleReleaseDelay/2, 1, "the budget stayed empty for the whole delay")
	})
}
