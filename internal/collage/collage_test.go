package collage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"runtime"
	"testing"
)

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
