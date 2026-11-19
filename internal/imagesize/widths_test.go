package imagesize

import "testing"

// Clients choose widths through the slot parameters only; image_size never
// accepts a width.
func TestParseRejectsWidthSizes(t *testing.T) {
	if _, err := Parse(string(Width(500))); err == nil {
		t.Fatal("Parse accepted a width size")
	}
}
