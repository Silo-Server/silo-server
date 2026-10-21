package catalog

import (
	"fmt"
	"strings"
	"testing"
)

func TestSeekAnchorsNeedAnIndexBoundedSort(t *testing.T) {
	e := &QueryExecutor{SeekAnchors: newSeekAnchorCache()}
	for _, tc := range []struct {
		field    string
		relation string
		anchored bool
	}{
		{"title", "", true},
		// Added order leads with a nullable term; walking from an anchor
		// would scan from the first row.
		{"added_at", "", false},
		// Episode titles may be NULL, so the episode relation isn't bounded.
		{"title", episodeCatalogBaseRelation, false},
	} {
		e.BaseRelationSQL = tc.relation
		plan, err := e.buildPreviewPagePlan(QueryDefinition{Sort: QuerySort{Field: tc.field, Order: "asc"}}, AccessFilter{}, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.canAnchorSeek(plan, seekAnchorStride); got != tc.anchored {
			t.Fatalf("%s on %q: canAnchorSeek = %v", tc.field, tc.relation, got)
		}
		// A seek before the first anchor is a short OFFSET already.
		if e.canAnchorSeek(plan, seekAnchorStride-1) {
			t.Fatalf("%s on %q: anchored a seek before the first anchor", tc.field, tc.relation)
		}
	}

	// A failed anchor pass (here: no pool) hands the seek back to OFFSET.
	e.BaseRelationSQL = ""
	plan, err := e.buildPreviewPagePlan(QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}}, AccessFilter{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, anchored, err := e.anchoredSeek(t.Context(), plan, seekAnchorStride); anchored || err != nil {
		t.Fatalf("failed anchor pass: anchored=%v err=%v, want an OFFSET fallback", anchored, err)
	}

	plan, err = e.buildPreviewPagePlan(QueryDefinition{Sort: QuerySort{Field: "title", Order: "desc"}}, AccessFilter{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := plan.seekAnchorSQL()
	if !strings.Contains(sql, "ROW_NUMBER() OVER ("+plan.orderBy+")") || !strings.Contains(sql, fmt.Sprintf("seek_position %% %d = 0", seekAnchorStride)) {
		t.Fatalf("anchor SQL = %s", sql)
	}
}
