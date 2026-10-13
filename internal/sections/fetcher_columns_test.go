package sections

import (
	"slices"
	"testing"
)

// Jellyfin-compatible lists built from section rows (/Items/Latest) advertise
// banner, thumb, clear art and disc art from these paths, so the shared select
// list must carry them; scanMediaItems reads them last.
func TestItemColumnsListCarriesExtraArtworkPaths(t *testing.T) {
	cols := itemColumnsList("mi")
	want := []string{"mi.banner_path", "mi.thumb_path", "mi.clearart_path", "mi.disc_path"}
	if got := cols[len(cols)-len(want):]; !slices.Equal(got, want) {
		t.Fatalf("last columns = %q, want %q", got, want)
	}
}
