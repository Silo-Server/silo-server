package bookid

import (
	"maps"
	"testing"
)

// Only audiobooks and ebooks split: a manga or movie item keeps a key named
// like a work key as an ordinary provider ID.
func TestSplitWorkIDsAppliesToBookTypesOnly(t *testing.T) {
	ids := map[string]string{"isbn": "9780306406157", " Hardcover ": " 9 ", "openlibrary_work": ""}
	for _, tc := range []struct {
		itemType      string
		edition, work map[string]string
	}{
		{"ebook", map[string]string{"isbn": "9780306406157"}, map[string]string{"hardcover": "9"}},
		{"manga", ids, nil},
	} {
		edition, work := SplitWorkIDs(tc.itemType, ids)
		if !maps.Equal(edition, tc.edition) || !maps.Equal(work, tc.work) {
			t.Errorf("SplitWorkIDs(%s) = %v, %v; want %v, %v", tc.itemType, edition, work, tc.edition, tc.work)
		}
	}
}
