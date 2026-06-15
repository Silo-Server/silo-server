package catalog

import "testing"

// TestNormalizeTitleForComparison pins the Go mirror of the SQL function
// public.normalize_search_text(). If Go-side normalization drifts from SQL,
// ExactTitleHint will fail to match the title_normalized generated column.
func TestNormalizeTitleForComparison(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ampersand collapses", "Law & Order", "law order"},
		{"and word strips", "Law and Order", "law order"},
		{"mixed punctuation", "Law and / & Order!", "law order"},
		{"and inside word preserved", "Random Sandwich", "random sandwich"},
		{"leading and stripped", "And Then There Were None", "then there were none"},
		{"trailing and stripped", "Order and", "order"},
		{"only and reduces to empty", "and", ""},
		{"single token kept", "and AND And", ""},
		{"consecutive and tokens", "rock and and roll", "rock roll"},
		{"acronym with ampersand", "S&P 500", "s p 500"},
		{"unicode letters", "Café & Crème", "café crème"},
		{"number word", "Dune: Part Two", "dune part 2"},
		{"number digit", "Dune Part 2", "dune part 2"},
		{"ordinal word", "Second Act", "2 act"},
		{"ordinal digit suffix", "2nd Act", "2 act"},
		{"thirteenth word", "Friday the Thirteenth", "friday the 13"},
		{"thirteenth digit suffix", "Friday the 13th", "friday the 13"},
		{"twenty word", "Twenty", "20"},
		{"composed numbers not folded", "Twenty One", "20 1"},
		{"embedded digit word unchanged", "Se7en", "se7en"},
		{"empty stays empty", "", ""},
		{"whitespace only", "   ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeTitleForComparison(tc.in)
			if got != tc.want {
				t.Fatalf("normalizeTitleForComparison(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseSearchQuery_ExactTitleHint_NormalizesSearchText ensures that the
// higher-level parser propagates search normalization into ExactTitleHint, so
// callers building SQL with the hint get the form that matches title_normalized.
func TestParseSearchQuery_ExactTitleHint_NormalizesSearchText(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Law & Order", "law order"},
		{"Law and Order", "law order"},
		{"\"Law and Order\" 2010", "law order"},
		{"Dune: Part Two", "dune part 2"},
		{"\"Dune: Part Two\" 2024", "dune part 2"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			parsed := parseSearchQuery(tc.in)
			if parsed.ExactTitleHint != tc.want {
				t.Fatalf("ExactTitleHint = %q, want %q (parsed = %+v)", parsed.ExactTitleHint, tc.want, parsed)
			}
		})
	}
}

func TestBuildTitlePrefixTsQuery(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Pride and P", "pride & p:*"},
		{"Law & Ord", "law & ord:*"},
		{"Dune: Part Two", "dune & part & 2:*"},
		{"and", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := buildTitlePrefixTsQuery(tc.in)
			if got != tc.want {
				t.Fatalf("buildTitlePrefixTsQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
