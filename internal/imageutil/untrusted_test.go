package imageutil

import (
	"errors"
	"testing"
)

// PDF, and the formats only ImageMagick reads, are refused by their leading
// bytes, whatever loaders libvips was built with.
func TestUntrustedFormatsAreRefused(t *testing.T) {
	pdf := []byte("%PDF-1.4\n" +
		"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
		"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 8 8]>>endobj\n" +
		"trailer<</Root 1 0 R>>\n%%EOF\n")
	bmp := append([]byte("BM"), make([]byte, 64)...)
	for name, data := range map[string][]byte{"pdf": pdf, "bmp": bmp} {
		t.Run(name, func(t *testing.T) {
			if decodableSignature(data) {
				t.Fatal("recognized as a format Silo decodes")
			}
			if _, err := GenerateVariants(data, []int{4}); !errors.Is(err, ErrInvalidImage) {
				t.Fatalf("err = %v, want ErrInvalidImage", err)
			}
		})
	}
}

// SVG is the one untrusted libvips loader Silo re-enables, for provider logos.
// A profile's own artwork may not be one, and no decode stays in libvips's
// operation cache.
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
			if err := CheckProfileImage(data); (name == "svg") != errors.Is(err, ErrInvalidImage) {
				t.Fatalf("CheckProfileImage: %v", err)
			}
		})
	}
	if n := cachedOperations(); n != 0 {
		t.Fatalf("libvips holds %d finished operations, want none", n)
	}
}
