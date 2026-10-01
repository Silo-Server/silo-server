package catalog

import "testing"

func TestCanonicalSubtitleFacetValues(t *testing.T) {
	got := canonicalSubtitleFacetValues([]string{"en", "eng", "legacy-token-!", "pt-br", "zh-hant"})
	want := []string{"en", "legacy-token-!", "pt-BR", "zh-Hant"}
	if len(got) != len(want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("values = %v, want %v", got, want)
		}
	}
}
