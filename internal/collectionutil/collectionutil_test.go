package collectionutil

import (
	"math"
	"testing"
)

func TestSourceFetchLimit(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	cases := []struct {
		name  string
		limit *int
		want  int
	}{
		{"no limit reads the whole source", nil, 0},
		{"non-positive limit reads the whole source", intPtr(0), 0},
		{"small limit scans the minimum", intPtr(10), 100},
		{"limit scans four times as deep", intPtr(50), 200},
		{"large limit is not capped", intPtr(10000), 40000},
		{"overflowing scan reads the whole source", intPtr(math.MaxInt / 2), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SourceFetchLimit(tc.limit); got != tc.want {
				t.Fatalf("SourceFetchLimit = %d, want %d", got, tc.want)
			}
		})
	}
}
