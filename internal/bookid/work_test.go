package bookid

import (
	"maps"
	"testing"
)

// Only audiobooks and ebooks split: a manga item keeps a key named like a work
// key as an ordinary provider ID.
func TestSplitWorkIDsLeavesMangaIDsAlone(t *testing.T) {
	ids := map[string]string{"isbn": "9780306406157", "hardcover": "9"}
	edition, work := SplitWorkIDs("manga", ids)
	if !maps.Equal(edition, ids) || work != nil {
		t.Errorf("SplitWorkIDs(manga) = %v, %v; want %v, nil", edition, work, ids)
	}
}
