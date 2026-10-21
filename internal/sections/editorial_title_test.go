package sections

import "testing"

func TestEditorialSpotlightDisplayTitle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, base, subject, want string
	}{
		{"appends resolved subject", "Actor Spotlight", "Liam Neeson", "Actor Spotlight - Liam Neeson"},
		{"avoids duplicate subject", "Actor Spotlight - Liam Neeson", "Liam Neeson", "Actor Spotlight - Liam Neeson"},
		{"falls back to base title", "Actor Spotlight", "", "Actor Spotlight"},
	} {
		if got := editorialSpotlightDisplayTitle(tc.base, tc.subject); got != tc.want {
			t.Fatalf("%s: editorialSpotlightDisplayTitle = %q, want %q", tc.name, got, tc.want)
		}
	}
}
