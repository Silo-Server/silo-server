package jellycompat

import "testing"

// TestSlicePageIDs locks slicePageIDs to sliceBaseItems' exact bounds
// semantics — the two slice the same page in parallel in
// writeNextUpResponse, and any divergence would attach detail upgrades to
// the wrong DTOs.
func TestSlicePageIDs(t *testing.T) {
	ids := []string{"a", "b", "c", "d"}
	cases := []struct {
		name       string
		startIndex int
		limit      int
		want       []string
	}{
		{"plain page", 0, 2, []string{"a", "b"}},
		{"offset page", 1, 2, []string{"b", "c"}},
		{"limit past end clamps", 2, 10, []string{"c", "d"}},
		{"start past end is empty", 9, 2, []string{}},
		{"zero limit returns rest", 1, 0, []string{"b", "c", "d"}},
		{"negative start clamps to zero", -3, 2, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := slicePageIDs(ids, tc.startIndex, tc.limit)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
			// Mirror against sliceBaseItems on a same-length DTO slice.
			dtos := make([]baseItemDTO, len(ids))
			for i := range ids {
				dtos[i] = baseItemDTO{ID: ids[i]}
			}
			sliced := sliceBaseItems(dtos, tc.startIndex, tc.limit)
			if len(sliced) != len(got) {
				t.Fatalf("sliceBaseItems len %d diverges from slicePageIDs len %d", len(sliced), len(got))
			}
			for i := range sliced {
				if sliced[i].ID != got[i] {
					t.Errorf("alignment broken at %d: dto %q vs id %q", i, sliced[i].ID, got[i])
				}
			}
		})
	}
}
