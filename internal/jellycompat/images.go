package jellycompat

import (
	"bytes"
	"crypto/sha1"
	"image"
	"image/color"
	"image/png"
)

func placeholderAvatarPNG(seed string) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 128, 128))
	sum := sha1.Sum([]byte(seed))
	fill := color.RGBA{R: sum[0], G: sum[1], B: sum[2], A: 255}
	for y := range 128 {
		for x := range 128 {
			img.Set(x, y, fill)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
