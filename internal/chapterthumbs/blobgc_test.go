package chapterthumbs

import (
	"testing"
)

func TestImagesGroup(t *testing.T) {
	tests := map[string]string{
		"chapter-images/42/0/w300.webp":         "chapter-images/42/",
		"chapter-images/42/":                    "chapter-images/42/",
		"chapter-images/7/12/original.webp":     "chapter-images/7/",
		"chapter-images/042/0/w300.webp":        "",
		"chapter-images/0/0/w300.webp":          "",
		"chapter-images/-3/0/w300.webp":         "",
		"chapter-images/abc/0/w300.webp":        "",
		"chapter-images/42":                     "",
		"chapter-images/99999999999999999999/0": "",
		"trickplay/42/1/0.1.jpg":                "",
	}
	for key, want := range tests {
		got, ok := imagesGroup(key)
		if got != want || ok != (want != "") {
			t.Errorf("imagesGroup(%q) = %q, %t; want %q", key, got, ok, want)
		}
	}
}
