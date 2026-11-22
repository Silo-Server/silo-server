package recommendations

import (
	"fmt"
)

// anchorList returns n items named prefix0, prefix1, … .
func anchorList(prefix string, n int) []ScoredItem {
	items := make([]ScoredItem, n)
	for i := range items {
		items[i] = ScoredItem{MediaItemID: fmt.Sprintf("%s%d", prefix, i)}
	}
	return items
}
