package catalog

import (
	"os"
	"strings"
	"testing"
)

// The frame poster cleanup finds frame posters through a partial index whose
// predicate spells out the frame prefix. The query's pattern, the index
// predicate and StillSourceFramePrefix must stay the same, or the cleanup
// silently reads all of media_items again.
func TestFrameSourcePatternMatchesPrefixAndIndex(t *testing.T) {
	if want := "'" + StillSourceFramePrefix + "%'"; frameSourcePatternSQL != want {
		t.Fatalf("frameSourcePatternSQL = %s, want %s", frameSourcePatternSQL, want)
	}
	migration, err := os.ReadFile("../../migrations/sql/20261116235542_add_media_items_frame_poster_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	if want := "WHERE poster_source_path LIKE " + frameSourcePatternSQL + ";"; !strings.Contains(string(migration), want) {
		t.Fatalf("index migration does not contain %q", want)
	}
}
