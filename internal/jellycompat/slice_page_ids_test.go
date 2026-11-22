package jellycompat

import (
	"math"
	"testing"
)

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
