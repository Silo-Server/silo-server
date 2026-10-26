package jellycompat

import (
	"math"
	"testing"
)

// TestSlicePageIDs locks slicePageIDs to sliceBaseItems' exact bounds
// semantics — the two slice the same page in parallel in
// writeNextUpResponse, and any divergence would attach detail upgrades to
// the wrong DTOs. Both share slicePage, so this also covers it, including a
// client Limit large enough to overflow start+limit.
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
		{"max limit from second item", 1, math.MaxInt, []string{"b", "c", "d"}},
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

// TestPageFetchLimitSaturates pins that the rows read from offset 0 to serve a
// page saturate instead of wrapping negative. The wrapped Next Up fetch made
// ListNextUp fall back to its 20-row default.
func TestPageFetchLimitSaturates(t *testing.T) {
	cases := []struct {
		startIndex, limit, want int
	}{
		{10, 24, 34},
		{1, math.MaxInt, math.MaxInt},
	}
	for _, tc := range cases {
		if got := pageFetchLimit(tc.startIndex, tc.limit); got != tc.want {
			t.Errorf("pageFetchLimit(%d, %d) = %d, want %d", tc.startIndex, tc.limit, got, tc.want)
		}
	}
}
