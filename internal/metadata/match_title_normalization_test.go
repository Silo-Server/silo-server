package metadata

import "testing"

const normalizedSomeShowTitle = "Some Show"

func TestNormalizeCandidateTitleForYear_TrailingYearVariants(t *testing.T) {
	cases := []struct {
		name  string
		title string
		year  int
		want  string
	}{
		{"bare year stripped", "Some Show 2026", 2026, normalizedSomeShowTitle},
		{"parenthesised year stripped", "24 stjerners julikalender (DK) (2026)", 2026, "24 stjerners julikalender (DK)"},
		{"bracketed year stripped", "Some Show [2026]", 2026, normalizedSomeShowTitle},
		{"different year kept", "Some Show (1999)", 2026, "Some Show (1999)"},
		{"no year hint keeps title", "Some Show (2026)", 0, "Some Show (2026)"},
		{"single token never stripped", "2026", 2026, "2026"},
		{"mid-title year kept", "2001 A Space Odyssey", 1968, "2001 A Space Odyssey"},
	}
	for _, tc := range cases {
		if got := normalizeCandidateTitleForYear(tc.title, tc.year); got != tc.want {
			t.Errorf("%s: normalizeCandidateTitleForYear(%q, %d) = %q, want %q",
				tc.name, tc.title, tc.year, got, tc.want)
		}
	}
}

func TestInferTitleSimilarity_FoldsDiacritics(t *testing.T) {
	cases := []struct {
		name  string
		left  string
		right string
		year  int
		want  float64
	}{
		{"combining marks", "Ze do Caixao", "Zé do Caixão", 2015, 1},
		{"turkish letters", "Konusanlar", "Konuşanlar", 2020, 1},
		{"capital eszett", "STRASSE", "STRAẞE", 0, 1},
		{"eszett", "Strasse", "Straße", 0, 1},
		{"o slash", "Forbrydelsen Kobenhavn", "Forbrydelsen København", 0, 1},
		{"ae ligature", "Baerbar", "Bærbar", 0, 1},
		{"oe ligature", "Coeur", "Cœur", 0, 1},
		{"unrelated titles stay distinct", "Miniverse", "Universe", 0, 0},
	}
	for _, tc := range cases {
		if got := inferTitleSimilarity(tc.left, tc.right, tc.year); got != tc.want {
			t.Errorf("%s: inferTitleSimilarity(%q, %q, %d) = %v, want %v",
				tc.name, tc.left, tc.right, tc.year, got, tc.want)
		}
	}
}

func TestInferTitleSimilarity_PreservesKanaVoicing(t *testing.T) {
	if got := inferTitleSimilarity("ガキの使い", "カキの使い", 0); got != 0 {
		t.Fatalf("kana titles differing by voicing must stay distinct, got %v", got)
	}
	if got := inferTitleSimilarity("ガキの使い", "カ゛キの使い", 0); got != 1 {
		t.Fatalf("spacing dakuten must normalize to the voiced kana, got %v", got)
	}
	if got := inferTitleSimilarity("カ゛キの使い", "カキの使い", 0); got != 0 {
		t.Fatalf("spacing dakuten must remain distinct from unvoiced kana, got %v", got)
	}
}

func TestInferTitleSimilarity_PreservesNonLatinCombiningMarks(t *testing.T) {
	// Thai mai ek changes the word from ปา (throw) to ป่า (forest). It is a
	// combining mark, but unlike a Latin accent it must remain match-significant.
	if got := inferTitleSimilarity("ปา", "ป่า", 0); got != 0 {
		t.Fatalf("Thai titles differing by a lexical combining mark must stay distinct, got %v", got)
	}
}

// The dedicated episode-title fold was removed in favor of the shared
// normalizer; episode comparison must keep folding diacritics.
func TestEpisodeTitleSimilarity_StillFoldsDiacritics(t *testing.T) {
	if got := episodeTitleSimilarity("Cafe con Leche", "Café con Leche"); got != 1 {
		t.Fatalf("episodeTitleSimilarity should fold diacritics, got %v", got)
	}
}
