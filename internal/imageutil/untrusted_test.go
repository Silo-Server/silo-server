package imageutil

import (
	"errors"
	"testing"

	"github.com/h2non/bimg"
)

// PDF is the untrusted loader bimg reaches whichever ImageMagick libvips links.
func TestUntrustedLoadersAreBlocked(t *testing.T) {
	if !bimg.IsTypeSupported(bimg.PDF) {
		t.Skip("libvips was built without a PDF loader")
	}
	pdf := []byte("%PDF-1.4\n" +
		"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
		"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 8 8]>>endobj\n" +
		"trailer<</Root 1 0 R>>\n%%EOF\n")
	_, err := GenerateVariants(pdf, []int{4})
	if !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("err = %v, want ErrInvalidImage", err)
	}
}

// SVG is the one untrusted libvips loader Silo re-enables, for provider logos.
func TestLoadersKeptAfterUntrustedBlock(t *testing.T) {
	cases := map[string][]byte{
		"png":  testPNG(t, 8, 8),
		"jpeg": largeTestJPEG(t, 8, 8),
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><rect width="8" height="8"/></svg>`),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := GenerateVariants(data, []int{4}); err != nil {
				t.Fatalf("GenerateVariants: %v", err)
			}
		})
	}
}
