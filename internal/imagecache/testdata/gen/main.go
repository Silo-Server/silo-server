//go:build ignore

// Command gen writes the tiny PNG fixtures the portable-format golden test
// encodes. Run it only to add or replace a fixture:
//
//	go run internal/imagecache/testdata/gen/main.go
//	go test ./internal/imagecache -run TestPortableFormatGolden -update
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

func write(path string, w, h int, alpha bool) {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if alpha && (x+y)%3 == 0 {
				a = 96
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8((x * 7) % 256),
				G: uint8((y * 11) % 256),
				B: uint8((x*y + 17) % 256),
				A: a,
			})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(f, img); err != nil {
		panic(err)
	}
}

func main() {
	const dir = "internal/imagecache/testdata/fixtures/"
	write(dir+"poster.png", 24, 36, false)
	write(dir+"backdrop.png", 48, 27, false)
	write(dir+"logo.png", 32, 16, true)
}
